package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Regression for deferred-trace-err-shadowed (2026-06-09 audit): downloadAndInstallLatest
// declared `var err error` for its deferred trace, but each download/verify/install step
// used `if err := <call>; err != nil { return ... }`, shadowing a local err. On failure the
// outer err stayed nil (last set by a successful os.OpenRoot), so the deferred trace logged
// the span as "ok" even though the function returned an error. Written after switching to a
// named return so the trace reflects the real outcome.
func TestDownloadAndInstallLatest_TraceReportsErrorOnFailure(t *testing.T) {
	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetLevel(types.LogLevelDebug)

	var mirrorBuf bytes.Buffer
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(&mirrorBuf)
	bootstrap.SetMirrorLogger(mirror)

	// A cancelled context makes the first (shadowed) step, downloadFile, fail fast
	// via http.NewRequestWithContext/client.Do without any real network call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := downloadAndInstallLatest(ctx, filepath.Join(t.TempDir(), "proxsave"), bootstrap, "v9.9.9", "9.9.9")
	if err == nil {
		t.Fatal("expected downloadAndInstallLatest to fail with a cancelled context")
	}

	out := mirrorBuf.String()
	if !strings.Contains(out, "End upgrade download/install (error=") {
		t.Errorf("deferred trace should report the span as failed; trace:\n%s", out)
	}
	if strings.Contains(out, "End upgrade download/install (ok") {
		t.Errorf("deferred trace must NOT report the span as ok when a step failed; trace:\n%s", out)
	}
}
