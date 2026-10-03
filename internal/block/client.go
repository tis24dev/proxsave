package block

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

// pbsClientName is the PBS client, run through the safeexec allowlist.
const pbsClientName = "proxmox-backup-client"

// ClientNotFound is the fact for a host where proxmox-backup-client cannot be found.
func ClientNotFound(err error) *Fact {
	return &Fact{Kind: FactClientNotFound, Label: pbsClientName, Text: "not found", Err: err}
}

// Client runs proxmox-backup-client for one PBS storage.
//
// Every call gets the same treatment: the parent environment without any PBS_* variable,
// plus PBS_PASSWORD and, when the storage has one, PBS_FINGERPRINT (the secrets never
// travel in argv); stdin is /dev/null, so the client cannot stop on a question; stdout
// and stderr go to two in-memory buffers. There is no timeout of ProxSave's own: the
// client stops by itself after 10 s on a connection and 120 s on an HTTP answer, and
// the call ends when ctx does (the daemon watchdog, SIGINT/SIGTERM).
type Client struct {
	Password    string
	Fingerprint string
}

// ClientResult is one call of the client. Args are the arguments after the program
// name, as passed: they carry no secret.
type ClientResult struct {
	Args     []string
	Stdout   []byte
	Stderr   []byte
	ExitCode int // -1 when the client did not run or did not exit by itself
	Duration time.Duration
	// Err is nil only when the client ran and exited 0. For a non-zero exit it is the
	// *exec.ExitError; when ctx ended first it wraps ctx.Err().
	Err error
}

// NotFound reports that the client is not installed (not in PATH).
func (r ClientResult) NotFound() bool {
	return errors.Is(r.Err, exec.ErrNotFound)
}

// Run executes proxmox-backup-client with args.
func (c Client) Run(ctx context.Context, args ...string) ClientResult {
	result := ClientResult{Args: append([]string(nil), args...), ExitCode: -1}
	cmd, err := safeexec.CommandContext(ctx, pbsClientName, args...)
	if err != nil {
		result.Err = err
		return result
	}
	safeexec.ApplyWaitDelay(cmd)
	cmd.Env = clientEnv(os.Environ(), c.Password, c.Fingerprint)
	cmd.Stdin = nil // os/exec: a nil Stdin reads from the null device
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	result.Duration = time.Since(start)
	result.Stdout = stdout.Bytes()
	result.Stderr = stderr.Bytes()
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	result.Err = runErr
	if ctxErr := ctx.Err(); ctxErr != nil && runErr != nil {
		result.Err = fmt.Errorf("%s stopped: %w (%v)", pbsClientName, ctxErr, runErr)
	}
	return result
}

// clientEnv is base without any PBS_* variable, plus the storage's own secrets: a PBS_*
// exported for the collector (PBS_REPOSITORY, PBS_PASSWORD, PBS_FINGERPRINT, ...) must
// not reach the client that writes to this storage.
func clientEnv(base []string, password, fingerprint string) []string {
	env := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "PBS_") {
			continue
		}
		env = append(env, kv)
	}
	if password != "" {
		env = append(env, "PBS_PASSWORD="+password)
	}
	if fingerprint != "" {
		env = append(env, "PBS_FINGERPRINT="+fingerprint)
	}
	return env
}

// Version is a PBS client or server version, <major>.<minor>.<release>.
type Version struct {
	Major, Minor, Release int
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Release)
}

// atLeast reports v >= major.minor.release.
func (v Version) atLeast(major, minor, release int) bool {
	if v.Major != major {
		return v.Major > major
	}
	if v.Minor != minor {
		return v.Minor > minor
	}
	return v.Release >= release
}

// SupportsChangeDetectionMode reports whether the client accepts
// --change-detection-mode: measured, clients 3.2.4 and older reject it (rc 255,
// "schema does not allow additional properties"), 3.2.5 and newer accept it.
// --chunk-size is accepted by every measured client (from 3.0.1).
func (v Version) SupportsChangeDetectionMode() bool {
	return v.atLeast(3, 2, 5)
}

// Versions is the answer of "version --repository <repo> --output-format json".
type Versions struct {
	Client Version
	Server Version
}

type versionJSON struct {
	Version string `json:"version"`
	Release string `json:"release"`
}

// ParseVersionOutput reads the JSON of "version --output-format json": the client and
// the server each carry "version" (<major>.<minor>) and "release".
func ParseVersionOutput(output []byte) (Versions, error) {
	var parsed struct {
		Client versionJSON `json:"client"`
		Server versionJSON `json:"server"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		return Versions{}, fmt.Errorf("parse version output: %w", err)
	}
	client, err := parseVersion(parsed.Client)
	if err != nil {
		return Versions{}, fmt.Errorf("client version: %w", err)
	}
	server, err := parseVersion(parsed.Server)
	if err != nil {
		return Versions{}, fmt.Errorf("server version: %w", err)
	}
	return Versions{Client: client, Server: server}, nil
}

func parseVersion(v versionJSON) (Version, error) {
	majorText, minorText, ok := strings.Cut(v.Version, ".")
	major, errMajor := strconv.Atoi(majorText)
	minor, errMinor := strconv.Atoi(minorText)
	release, errRelease := strconv.Atoi(v.Release)
	if !ok || errMajor != nil || errMinor != nil || errRelease != nil {
		return Version{}, fmt.Errorf("unrecognized version %q release %q", v.Version, v.Release)
	}
	return Version{Major: major, Minor: minor, Release: release}, nil
}

// ProbeVersion asks the client and the server their versions. The error is the failed
// call (see ClientResult.Err) or output that is not the expected JSON; the result is
// returned in both cases for the caller's DEBUG lines and cause.
func (c Client) ProbeVersion(ctx context.Context, repository string) (Versions, ClientResult, error) {
	result := c.Run(ctx, "version", "--repository", repository, "--output-format", "json")
	if result.Err != nil {
		return Versions{}, result, result.Err
	}
	versions, err := ParseVersionOutput(result.Stdout)
	return versions, result, err
}
