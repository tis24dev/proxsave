package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// visibleOf returns the INFO and WARNING lines of a captured log, without timestamps.
func visibleOf(out string) []string {
	var lines []string
	for _, line := range strings.Split(stripTimes(out), "\n") {
		if strings.HasPrefix(line, "INFO") || strings.HasPrefix(line, "WARNING") {
			lines = append(lines, line)
		}
	}
	return lines
}

func writeArchiveFixture(t *testing.T) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "node-backup-20261003-100000.tar.zst")
	writeTestFile(t, archive, "archive")
	return archive
}

// RCLONE_RETRIES > 1 and the operation timeout ended the attempts before all of them
// ran: the attempt lines of the attempts that ran, the last one the timeout, and no
// "  Upload failed" after them.
func TestCloudUploadEndedByTheTimeoutIsReportedByItsAttempts(t *testing.T) {
	archive := writeArchiveFixture(t)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 3, RcloneTimeoutOperation: 1}
	cs, buf := newCloudWithCapturedLog(t, cfg, func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "copyto" {
			<-ctx.Done()
			return nil, errors.New("signal: killed")
		}
		return nil, nil
	})
	err := cs.Store(context.Background(), archive, nil)
	if err == nil || !AttemptsReported(err) {
		t.Fatalf("Store = %v, want a failure reported by its attempt lines", err)
	}
	got := visibleOf(buf.String())
	if want := "INFO       Attempt 1/3 failed: timed out after 1s"; strings.Join(got, "\n") != want {
		t.Fatalf("visible lines =\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
}

// The run cancelled during the backoff: the attempt line of the attempt that ran, and
// no "  Upload failed"; the step [8] log copy follows the same rule.
func TestCloudUploadCancelledDuringTheBackoffIsReportedByItsAttempts(t *testing.T) {
	const cause = "Failed to copyto: connection reset by peer"
	archive := writeArchiveFixture(t)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 3}
	failing := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "copyto" {
			return []byte("2026/10/03 10:00:00 " + cause + "\n"), errors.New("exit status 1")
		}
		return nil, nil
	}
	cs, buf := newCloudWithCapturedLog(t, cfg, failing)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs.waitForRetry = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	err := cs.Store(ctx, archive, nil)
	if !errors.Is(err, context.Canceled) || !AttemptsReported(err) {
		t.Fatalf("Store = %v, want context.Canceled reported by its attempt lines", err)
	}
	if got, want := strings.Join(visibleOf(buf.String()), "\n"), "INFO       Attempt 1/3 failed: "+cause; got != want {
		t.Fatalf("visible lines =\n%s\nwant\n%s", got, want)
	}

	cs, buf = newCloudWithCapturedLog(t, cfg, failing)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	cs.waitForRetry = func(context.Context, time.Duration) error {
		cancel2()
		return context.Canceled
	}
	err = cs.UploadToRemotePath(ctx2, archive, "remote:/logs/x.log", true)
	if !AttemptsReported(err) {
		t.Fatalf("UploadToRemotePath = %v, want it reported by its attempt lines", err)
	}
	if got, want := strings.Join(visibleOf(buf.String()), "\n"), "INFO       Attempt 1/3 failed: "+cause; got != want {
		t.Fatalf("log copy visible lines =\n%s\nwant\n%s", got, want)
	}
}

// A copy that succeeded and whose verification failed is "  Verification failed:
// <error>", not "  Upload failed".
func TestCloudVerificationFailureIsItsOwnFact(t *testing.T) {
	archive := writeArchiveFixture(t)
	name := filepath.Base(archive)
	for _, tc := range []struct {
		name   string
		mutate func(*config.Config)
		lsl    string
		hash   string
		want   string
	}{
		{"size mismatch", nil, "        5 2026-10-03 10:00:00.000000000 " + name, "",
			"INFO       Verification failed: size mismatch: local=7 remote=5"},
		{"checksum mismatch", func(c *config.Config) { c.CloudVerifyChecksum = true },
			"        7 2026-10-03 10:00:00.000000000 " + name, strings.Repeat("ab", 32) + "  " + name,
			"INFO       Verification failed: checksum mismatch: local="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 2}
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			cs, buf := newCloudWithCapturedLog(t, cfg, func(_ context.Context, _ string, args ...string) ([]byte, error) {
				switch args[0] {
				case "lsl":
					return []byte(tc.lsl + "\n"), nil
				case "hashsum":
					return []byte(tc.hash + "\n"), nil
				}
				return nil, nil
			})
			err := cs.Store(context.Background(), archive, nil)
			var se *StorageError
			if !errors.As(err, &se) || se.PrimarySaved || AttemptsReported(err) {
				t.Fatalf("Store = %v, want a primary failure not reported by attempt lines", err)
			}
			got := visibleOf(buf.String())
			if len(got) != 1 || !strings.HasPrefix(got[0], tc.want) || strings.Contains(buf.String(), "Upload failed") {
				t.Fatalf("visible lines =\n%s\nwant one line starting with %q", strings.Join(got, "\n"), tc.want)
			}
		})
	}
}
