// Package health reports backup outcomes and a liveness heartbeat to a
// healthchecks.io-compatible monitor (our self-hosted instance or the SaaS).
//
// It is deliberately dependency-light: an *http.Client plus two already-resolved
// ping URLs (a "service alive" check and a "backup outcome" check). URL
// resolution (centralized fetch from the proxsave_server, or self-mode
// endpoint+slug assembly) lives elsewhere; this package only pings. Keeping it
// free of the logging package makes it trivially unit-testable; the daemon that
// wires it is responsible for registering the ping URLs as log secrets.
package health

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/version"
)

// Ping suffixes on a healthchecks check URL (same identifier, different suffix).
const (
	suffixStart = "/start" // run started (pair with ?rid for duration)
	suffixFail  = "/fail"  // definitive failure (used for the hang case)
	suffixLog   = "/log"   // records a ping WITHOUT changing check state (test only)
)

// healthchecks accepts a diagnostic body up to 100 kB (UTF-8).
const maxPingBody = 100 * 1024

// pingTimeout bounds a single ping; a slow/down monitor must never stall the daemon.
const pingTimeout = 10 * time.Second

var (
	// ErrNoAliveURL / ErrNoBackupURL are returned when a ping is attempted before
	// the corresponding check URL has been resolved (e.g. centralized fetch not yet
	// succeeded). The daemon treats these as "not reportable yet", not fatal.
	ErrNoAliveURL  = errors.New("healthcheck: alive ping url not configured")
	ErrNoBackupURL = errors.New("healthcheck: backup ping url not configured")
)

// Reporter pings a healthchecks-compatible monitor. Zero value is not usable;
// build one with NewReporter.
type Reporter struct {
	client    *http.Client
	aliveURL  string
	backupURL string
	sendLog   bool
}

// Config configures a Reporter. AliveURL/BackupURL are full ping URLs, e.g.
// "https://hc.proxsave.dev/ping/<uuid>". Either may be empty (the matching ping
// then returns ErrNo*URL). A nil Client gets a default with a bounded timeout.
type Config struct {
	Client    *http.Client
	AliveURL  string
	BackupURL string
	SendLog   bool // POST a log tail on non-success backup outcomes
}

// NewReporter builds a Reporter from Config.
func NewReporter(c Config) *Reporter {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: pingTimeout}
	}
	return &Reporter{
		client:    client,
		aliveURL:  strings.TrimRight(strings.TrimSpace(c.AliveURL), "/"),
		backupURL: strings.TrimRight(strings.TrimSpace(c.BackupURL), "/"),
		sendLog:   c.SendLog,
	}
}

// HasBackupURL reports whether the backup-outcome check URL is resolved.
func (r *Reporter) HasBackupURL() bool { return r.backupURL != "" }

// HasAliveURL reports whether the service-alive check URL is resolved.
func (r *Reporter) HasAliveURL() bool { return r.aliveURL != "" }

// Heartbeat pings the "service alive" check with a success ping. Called on a
// fixed interval from the daemon so silence (host down / daemon dead) is detected
// server-side.
func (r *Reporter) Heartbeat(ctx context.Context) error {
	if r.aliveURL == "" {
		return ErrNoAliveURL
	}
	return r.ping(ctx, r.aliveURL, "", "alive")
}

// RunStarted pings /start on the backup-outcome check. rid correlates this with
// the later outcome ping so the monitor can measure run duration.
func (r *Reporter) RunStarted(ctx context.Context, rid string) error {
	if r.backupURL == "" {
		return ErrNoBackupURL
	}
	return r.ping(ctx, pingURL(r.backupURL, suffixStart, rid), "", "start")
}

// RunFinished pings the backup-outcome check with the run's exit status
// (/<0-255>: 0 = success, non-zero = fail so the user is alerted). On a non-zero
// outcome, and when SendLog is set, the log tail is attached as the POST body.
func (r *Reporter) RunFinished(ctx context.Context, rid string, exitCode int, logTail string) error {
	if r.backupURL == "" {
		return ErrNoBackupURL
	}
	body := ""
	if r.sendLog && exitCode != 0 {
		body = logTail
	}
	suffix := "/" + strconv.Itoa(clampExit(exitCode))
	return r.ping(ctx, pingURL(r.backupURL, suffix, rid), body, "finish")
}

// RunHang pings /fail on the backup-outcome check when the supervised child
// exceeded its budget and was killed (no exit code to report). The body records
// the timeout so the monitor UI shows why.
func (r *Reporter) RunHang(ctx context.Context, rid string, timeout time.Duration, logTail string) error {
	if r.backupURL == "" {
		return ErrNoBackupURL
	}
	body := fmt.Sprintf("timed out after %s", timeout)
	if r.sendLog && logTail != "" {
		body = body + "\n\n" + logTail
	}
	return r.ping(ctx, pingURL(r.backupURL, suffixFail, rid), body, "hang")
}

// TestPing hits base + /log, which records a ping WITHOUT changing the check
// state, and asserts a 2xx. Used by the install wizard connectivity test so a
// "Verify" never leaves a spurious success/fail on the user's check.
func (r *Reporter) TestPing(ctx context.Context, base string) error {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ErrNoAliveURL
	}
	return r.ping(ctx, base+suffixLog, "", "log")
}

// ping does one POST to a check URL. POST (not GET) is used uniformly so an
// optional diagnostic body can ride along; an empty body is a plain ping. Errors
// carry only the label + status, never the URL (the check UUID is a low-capability
// secret).
func (r *Reporter) ping(ctx context.Context, u, body, label string) error {
	reqCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	var rdr io.Reader
	if body != "" {
		if len(body) > maxPingBody { // keep the TAIL (most recent lines)
			body = body[len(body)-maxPingBody:]
		}
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, u, rdr)
	if err != nil {
		return fmt.Errorf("healthcheck %s: %w", label, err)
	}
	req.Header.Set("User-Agent", "proxsave/"+version.String())

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck %s: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("healthcheck %s: HTTP %d", label, resp.StatusCode)
	}
	return nil
}

// pingURL joins a check base URL with a suffix and an optional rid query.
func pingURL(base, suffix, rid string) string {
	u := base + suffix
	if rid != "" {
		u += "?rid=" + url.QueryEscape(rid)
	}
	return u
}

// clampExit bounds an exit code into the /<0-255> ping range healthchecks accepts.
func clampExit(code int) int {
	if code < 0 {
		return 255
	}
	if code > 255 {
		return 255
	}
	return code
}

// NewRunID returns a random RFC-4122 v4 UUID string for use as a run correlation
// id (rid). Falls back to a timestamp-free zero-ish value only if the system CSPRNG
// fails, which in practice does not happen.
func NewRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
