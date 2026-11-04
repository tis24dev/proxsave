package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// --upgrade exited 2, "configuration error", when GitHub did not answer and when the
// confirmation got Ctrl+C, SIGTERM, end of input or the idle timeout. Now: an
// interruption exits 130 as backup and restore do, a prompt left without an answer is
// a cancel (exit 0), and an unreachable GitHub is a failed run (exit 1), like a failed
// download.
func TestUpgradeExitCodesForAbortsAndFetchFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		cancel   bool
		wantCode int
		wantLine string
	}{
		{"github 502", http.StatusBadGateway, false, types.ExitGenericError.Int(), "failed to fetch latest release: status 502 Bad Gateway"},
		{"interrupted during the check", http.StatusOK, true, exitCodeInterrupted, ""},
		{"end of input at the prompt", http.StatusOK, false, types.ExitSuccess.Int(), "Upgrade cancelled by user; no changes were made."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"tag_name":"v99.0.0","body":""}`))
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			prevTransport := http.DefaultTransport
			http.DefaultTransport = rewriteToServer{target: target, base: prevTransport}
			defer func() { http.DefaultTransport = prevTransport }()

			stdinR, stdinW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = stdinW.Close()
			prevStdin := os.Stdin
			os.Stdin = stdinR
			defer func() { os.Stdin = prevStdin; _ = stdinR.Close() }()

			var out bytes.Buffer
			bootstrap := logging.NewBootstrapLogger()
			bootstrap.SetConsoleQuiet(true)
			mirror := logging.New(types.LogLevelDebug, false)
			mirror.SetOutput(&out)
			bootstrap.SetMirrorLogger(mirror)

			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancel {
				cancel()
			}
			defer cancel()
			_, _, code, terminal, _ := upgradeAcquireBinary(ctx, &cli.Args{Upgrade: true}, bootstrap, t.TempDir(), "0.1.0")
			if !terminal || code != tc.wantCode {
				t.Fatalf("terminal=%v code=%d, want %d; log:\n%s", terminal, code, tc.wantCode, out.String())
			}
			if tc.wantLine != "" && !strings.Contains(out.String(), tc.wantLine) {
				t.Fatalf("no line %q; log:\n%s", tc.wantLine, out.String())
			}
			if tc.cancel && strings.Contains(out.String(), "ERROR") {
				t.Fatalf("an interruption printed an ERROR; log:\n%s", out.String())
			}
		})
	}
}
