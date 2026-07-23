package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
)

const relayTestSecret = "3h64-dyi8-q3d6-wcm5"

func TestProvisionRelaySecretBestEffortGating(t *testing.T) {
	orig := provisionRelaySecretFn
	t.Cleanup(func() { provisionRelaySecretFn = orig })
	called := 0
	provisionRelaySecretFn = func(ctx context.Context, host, id, baseDir string, logger *logging.Logger) (bool, error) {
		called++
		return false, nil
	}
	cent := func(baseDir, serverID string) *config.Config {
		return &config.Config{HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeCentralized, BaseDir: baseDir, ServerID: serverID, ServerAPIHost: "https://h"}
	}

	// self mode -> never attempts.
	if got := provisionRelaySecretBestEffort(context.Background(), &config.Config{HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeSelf, BaseDir: t.TempDir(), ServerID: "1"}, nil); got != "" || called != 0 {
		t.Fatalf("self mode must not provision: got=%q called=%d", got, called)
	}
	// disabled -> never attempts.
	if got := provisionRelaySecretBestEffort(context.Background(), &config.Config{HealthcheckEnabled: false, HealthcheckMode: config.HealthcheckModeCentralized, BaseDir: t.TempDir(), ServerID: "1"}, nil); got != "" || called != 0 {
		t.Fatalf("disabled must not provision: got=%q called=%d", got, called)
	}
	// centralized, no ServerID -> never attempts.
	if got := provisionRelaySecretBestEffort(context.Background(), cent(t.TempDir(), ""), nil); got != "" || called != 0 {
		t.Fatalf("no ServerID must not provision: got=%q called=%d", got, called)
	}
	// centralized + ServerID + no secret -> attempts exactly once.
	if got := provisionRelaySecretBestEffort(context.Background(), cent(t.TempDir(), "123456789012"), nil); got != "" || called != 1 {
		t.Fatalf("centralized+id+no-secret must attempt once: got=%q called=%d", got, called)
	}
}

func TestProvisionRelaySecretBestEffortSkipsWhenSecretPresent(t *testing.T) {
	orig := provisionRelaySecretFn
	t.Cleanup(func() { provisionRelaySecretFn = orig })
	called := 0
	provisionRelaySecretFn = func(ctx context.Context, host, id, baseDir string, logger *logging.Logger) (bool, error) {
		called++
		return false, nil
	}
	base := t.TempDir()
	if err := identity.PersistNotifySecret(context.Background(), base, relayTestSecret, nil); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	cfg := &config.Config{HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeCentralized, BaseDir: base, ServerID: "123456789012", ServerAPIHost: "https://h"}
	if got := provisionRelaySecretBestEffort(context.Background(), cfg, nil); got != relayTestSecret {
		t.Fatalf("existing secret should be returned unchanged, got %q", got)
	}
	if called != 0 {
		t.Fatalf("must NOT attempt provisioning when a secret is present, called=%d", called)
	}
}

func TestProvisionRelaySecretBestEffortSuccessReturnsPersistedSecret(t *testing.T) {
	orig := provisionRelaySecretFn
	t.Cleanup(func() { provisionRelaySecretFn = orig })
	base := t.TempDir()
	provisionRelaySecretFn = func(ctx context.Context, host, id, baseDir string, logger *logging.Logger) (bool, error) {
		// Emulate the real provisioner persisting the secret before returning true.
		if err := identity.PersistNotifySecret(ctx, baseDir, relayTestSecret, nil); err != nil {
			return false, err
		}
		return true, nil
	}
	cfg := &config.Config{HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeCentralized, BaseDir: base, ServerID: "123456789012", ServerAPIHost: "https://h"}
	if got := provisionRelaySecretBestEffort(context.Background(), cfg, nil); got != relayTestSecret {
		t.Fatalf("want freshly persisted secret %q, got %q", relayTestSecret, got)
	}
}

// A definitive server auth rejection (403 -> health.ErrHCAuth) means the on-disk relay
// secret no longer matches the server hash. buildReporter must clear it so the next
// provisioning cycle mints a fresh one (self-heal), instead of leaving the host stuck.
func TestBuildReporterClearsSecretOnAuthReject(t *testing.T) {
	base := t.TempDir()
	if err := identity.PersistNotifySecret(context.Background(), base, relayTestSecret, nil); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // reject the secret -> ErrHCAuth
	}))
	defer server.Close()

	d := &daemon{
		cfg: &config.Config{
			HealthcheckEnabled: true,
			HealthcheckMode:    config.HealthcheckModeCentralized,
			BaseDir:            base,
			ServerID:           "123456789012",
			ServerAPIHost:      server.URL,
		},
		now: time.Now,
	}
	if r := d.buildReporter(context.Background()); r != nil {
		t.Fatalf("expected nil reporter after auth reject (no cached URLs), got %v", r)
	}
	if s, _ := identity.LoadNotifySecret(base); s != "" {
		t.Fatalf("relay secret must be cleared after ErrHCAuth, still on disk: %q", s)
	}
}

// A 410 SERVER_PARKED (ErrHCParked) is as definitive as an auth reject: the row was
// purged, so the on-disk secret must be cleared for re-provisioning (design 11.2).
func TestBuildReporterClearsSecretOnParked(t *testing.T) {
	base := t.TempDir()
	if err := identity.PersistNotifySecret(context.Background(), base, relayTestSecret, nil); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone) // parked -> ErrHCParked
		_, _ = io.WriteString(w, `{"error":"SERVER_PARKED"}`)
	}))
	defer server.Close()

	d := &daemon{
		cfg: &config.Config{
			HealthcheckEnabled: true,
			HealthcheckMode:    config.HealthcheckModeCentralized,
			BaseDir:            base,
			ServerID:           "123456789012",
			ServerAPIHost:      server.URL,
		},
		now: time.Now,
	}
	if r := d.buildReporter(context.Background()); r != nil {
		t.Fatalf("expected nil reporter after parked (no cached URLs), got %v", r)
	}
	if s, _ := identity.LoadNotifySecret(base); s != "" {
		t.Fatalf("relay secret must be cleared after ErrHCParked, still on disk: %q", s)
	}
}

// A NON-auth fetch failure (503 -> ErrHCNotReady) must NOT churn the on-disk secret: a
// possibly-good secret is preserved so a transient server hiccup does not force a re-mint.
func TestBuildReporterKeepsSecretOnTransientFailure(t *testing.T) {
	base := t.TempDir()
	if err := identity.PersistNotifySecret(context.Background(), base, relayTestSecret, nil); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // transient -> ErrHCNotReady, not auth
	}))
	defer server.Close()

	d := &daemon{
		cfg: &config.Config{
			HealthcheckEnabled: true,
			HealthcheckMode:    config.HealthcheckModeCentralized,
			BaseDir:            base,
			ServerID:           "123456789012",
			ServerAPIHost:      server.URL,
		},
		now: time.Now,
	}
	_ = d.buildReporter(context.Background())
	if s, _ := identity.LoadNotifySecret(base); s != relayTestSecret {
		t.Fatalf("relay secret must be preserved on a transient failure, got %q", s)
	}
}

func TestDaemonMaybeProvisionRelaySecretThrottle(t *testing.T) {
	orig := provisionRelaySecretFn
	t.Cleanup(func() { provisionRelaySecretFn = orig })
	called := 0
	provisionRelaySecretFn = func(ctx context.Context, host, id, baseDir string, logger *logging.Logger) (bool, error) {
		called++
		return false, nil
	}
	now := time.Unix(1_700_000_000, 0)
	d := &daemon{
		cfg: &config.Config{HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeCentralized, BaseDir: t.TempDir(), ServerID: "123456789012", ServerAPIHost: "https://h"},
		now: func() time.Time { return now },
	}
	d.maybeProvisionRelaySecret(context.Background())
	if called != 1 {
		t.Fatalf("first attempt must call the provisioner, called=%d", called)
	}
	// Immediate retry within the throttle window: no second call.
	d.maybeProvisionRelaySecret(context.Background())
	if called != 1 {
		t.Fatalf("within throttle window must NOT re-attempt, called=%d", called)
	}
	// Advance beyond the throttle window: attempts again.
	now = now.Add(daemonProvisionRetryInterval + time.Second)
	d.maybeProvisionRelaySecret(context.Background())
	if called != 2 {
		t.Fatalf("after throttle window must re-attempt, called=%d", called)
	}
}

func TestDaemonMaybeProvisionRelaySecretHonorsLongerServerBackoff(t *testing.T) {
	orig := provisionRelaySecretFn
	t.Cleanup(func() { provisionRelaySecretFn = orig })
	called := 0
	provisionRelaySecretFn = func(
		ctx context.Context, host, id, baseDir string,
		logger *logging.Logger,
	) (bool, error) {
		called++
		return false, &notify.RelayProvisionRateLimitError{
			RetryAfter: 2 * time.Hour,
		}
	}
	start := time.Unix(1_700_000_000, 0)
	now := start
	d := &daemon{
		cfg: &config.Config{
			HealthcheckEnabled: true,
			HealthcheckMode:    config.HealthcheckModeCentralized,
			BaseDir:            t.TempDir(),
			ServerID:           "1234567890123456",
			ServerAPIHost:      "https://h",
		},
		now: func() time.Time { return now },
	}

	d.maybeProvisionRelaySecret(context.Background())
	if called != 1 {
		t.Fatalf("first attempt must call the provisioner, called=%d", called)
	}
	now = start.Add(2*time.Hour - time.Second)
	d.maybeProvisionRelaySecret(context.Background())
	if called != 1 {
		t.Fatalf("must not retry before server Retry-After, called=%d", called)
	}
	// The deterministic anti-herd jitter is bounded to five minutes.
	now = start.Add(2*time.Hour + daemonProvisionMaxRetryJitter + time.Second)
	d.maybeProvisionRelaySecret(context.Background())
	if called != 2 {
		t.Fatalf("must retry after Retry-After plus max jitter, called=%d", called)
	}
}

func TestDaemonProvisionRetryJitterIsStableAndBounded(t *testing.T) {
	a := daemonProvisionRetryJitter("1234567890123456", 2*time.Hour)
	b := daemonProvisionRetryJitter("1234567890123456", 2*time.Hour)
	if a != b {
		t.Fatalf("jitter must be stable per server: %v != %v", a, b)
	}
	if a < 0 || a > daemonProvisionMaxRetryJitter {
		t.Fatalf("jitter %v outside [0,%v]", a, daemonProvisionMaxRetryJitter)
	}
}
