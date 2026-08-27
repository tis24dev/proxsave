package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// seedTwoOwnedArchives lays out two archives this host wrote, each with the
// completion sidecar retention requires (an archive with no sidecar is inert for
// retention, partitionRetentionEligible, and the counts under test would then be
// right for the wrong reason).
func seedTwoOwnedArchives(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seeds := []struct {
		name string
		when time.Time
	}{
		{"pve.home.arpa-backup-20250102-100000.tar.zst", time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)},
		{"pve.home.arpa-backup-20250101-100000.tar.zst", time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)},
	}
	for _, seed := range seeds {
		p := filepath.Join(dir, seed.name)
		if err := os.WriteFile(p, []byte("archive"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", seed.name, err)
		}
		if err := os.WriteFile(p+".sha256", []byte("h  archive\n"), 0o600); err != nil {
			t.Fatalf("seed sidecar for %s: %v", seed.name, err)
		}
		manifest := fmt.Sprintf(`{"hostname":"pve.home.arpa","created_at":%q}`, seed.when.Format(time.RFC3339))
		if err := os.WriteFile(p+".metadata", []byte(manifest), 0o600); err != nil {
			t.Fatalf("seed manifest for %s: %v", seed.name, err)
		}
		if err := os.Chtimes(p, seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name, err)
		}
	}
	return dir
}

// TestASummaryFromNoPassIsTellableFromAFinishedOne pins the contract
// LastRetentionSummary had to carry and did not.
//
// The two states below are the ones that used to be indistinguishable through a
// public interface. A backend nobody has run reports zeros. A backend whose pass
// finished with everything already inside the limit reports the SAME zeros: the
// early return in ApplyRetention leaves lastRet at the value the reset gave it. A
// caller receiving that pair had no field to separate them, so it could either
// print a fabricated "0 deleted, 0 remaining" for a pass that never happened, or
// suppress the real numbers of the healthy run that is the common case.
//
// The zero value is asserted whole, not field by field, because that is the claim:
// a backend that has never run retention returns exactly RetentionSummary{}, and
// the meaning of that value is now written down (see RetentionReporter).
func TestASummaryFromNoPassIsTellableFromAFinishedOne(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	fresh, err := NewLocalStorage(&config.Config{BackupPath: t.TempDir()}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	if got := fresh.LastRetentionSummary(); got != (RetentionSummary{}) {
		t.Fatalf("a backend that has never run retention reported %+v, want the zero value: anything else is a number invented for a pass that did not happen", got)
	}

	dir := seedTwoOwnedArchives(t)
	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted %d archive(s) under a limit of 10; this test needs the steady state, where a finished pass publishes the same zero counts a backend that never ran does", deleted)
	}

	summary := l.LastRetentionSummary()
	if !summary.PassCompleted {
		t.Fatalf("a retention pass ran to completion and the summary still says it did not: %+v. Every count in it is zero, exactly like a backend nobody has run, so PassCompleted is the only thing that tells a caller these numbers are real", summary)
	}
	if summary.BackupsDeleted != 0 || summary.BackupsRemaining != 0 {
		t.Fatalf("the steady-state pass published counts %+v; this test is built on that pass publishing zeros, so the fixture has drifted rather than the code", summary)
	}
}

// TestASummaryDisownsThePassThatBailed is the other half, and the sharper one: a
// pass that failed must not leave "these numbers describe a finished pass"
// standing. It runs a real pass that deletes archives first, so a regression that
// drops the reset at the top of ApplyRetention, or publishes completion
// unconditionally, has something wrong to report rather than zeros that happen to
// look harmless.
func TestASummaryDisownsThePassThatBailed(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := seedTwoOwnedArchives(t)
	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	if _, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
		t.Fatalf("first ApplyRetention: %v", err)
	}
	if first := l.LastRetentionSummary(); !first.PassCompleted || first.BackupsDeleted != 1 {
		t.Fatalf("first pass published %+v, want one deletion from a completed pass; the second half of this test means nothing without it", first)
	}

	// A cancelled context bails at the first statement of the pass, before the
	// listing, so nothing at all was learned about this location.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.ApplyRetention(ctx, RetentionConfig{Policy: "simple", MaxBackups: 1}); err == nil {
		t.Fatal("ApplyRetention on a cancelled context returned no error; this test needs the bail")
	}

	summary := l.LastRetentionSummary()
	if summary.PassCompleted {
		t.Fatalf("a pass that returned an error published %+v as a finished pass; a caller reading those counts would report a retention run that never got past its first statement", summary)
	}
	if summary.BackupsDeleted != 0 || summary.BackupsRemaining != 0 {
		t.Fatalf("the failed pass left the previous pass's counts standing: %+v. One struct, two different ages, is exactly what the reset at the top of ApplyRetention exists to prevent", summary)
	}
}

// TestScopeValidIsNotAPassRanFlag is the guard against the fix nobody should make.
//
// ScopeValid is the closest existing field to "a pass has run" and it is the wrong
// one: it answers "did the scope account for the listing", so it is deliberately
// FALSE after a real, complete pass on a host that cannot name itself
// (applyRetentionHostScope owns nothing there and warns, and publishing 0 owned
// beside a limit would be a worse lie than the unscoped total). Reusing it as a
// has-a-pass-run flag would silently report exactly that machine as never having
// run retention.
//
// This test fails if PassCompleted is ever aliased to ScopeValid, in either
// direction, which is a one-token change that every other test here would accept.
func TestScopeValidIsNotAPassRanFlag(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "", nil }
	defer func() { retentionHostname = original }()

	dir := seedTwoOwnedArchives(t)
	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	if _, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	summary := l.LastRetentionSummary()
	if summary.ScopeValid {
		t.Fatalf("an unnamed host published an ownership scope of %d; that is what ScopeValid exists to withhold, and this test needs it withheld", summary.Owned)
	}
	if !summary.PassCompleted {
		t.Fatal("a complete retention pass on a host that cannot name itself is reported as no pass at all: the two fields answer different questions, and collapsing them hides a whole class of machine from any caller that asks whether retention ran")
	}
}

// TestEveryRetentionReporterAnswersTheSameContract pins the secondary and cloud
// backends, which carry their own copy of the summary machinery. Three copies of a
// publication is three places to forget it, and the local tests above cannot see
// either of the other two.
func TestEveryRetentionReporterAnswersTheSameContract(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	t.Run("secondary", func(t *testing.T) {
		fresh, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}, newTestLogger(), "pve.home.arpa")
		if err != nil {
			t.Fatalf("NewSecondaryStorage: %v", err)
		}
		if got := fresh.LastRetentionSummary(); got != (RetentionSummary{}) {
			t.Fatalf("a secondary backend that has never run retention reported %+v, want the zero value", got)
		}

		dir := seedTwoOwnedArchives(t)
		s, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: dir}, newTestLogger(), "pve.home.arpa")
		if err != nil {
			t.Fatalf("NewSecondaryStorage: %v", err)
		}
		if _, err := s.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10}); err != nil {
			t.Fatalf("ApplyRetention: %v", err)
		}
		if summary := s.LastRetentionSummary(); !summary.PassCompleted {
			t.Fatalf("the secondary backend ran a complete pass and reported %+v: on the shared NAS mount this is the location whose numbers an operator reads most often", summary)
		}
	})

	t.Run("cloud", func(t *testing.T) {
		cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
		fresh, err := NewCloudStorage(cfg, newTestLogger(), "pve.home.arpa")
		if err != nil {
			t.Fatalf("NewCloudStorage: %v", err)
		}
		if got := fresh.LastRetentionSummary(); got != (RetentionSummary{}) {
			t.Fatalf("a cloud backend that has never run retention reported %+v, want the zero value", got)
		}

		cs, err := NewCloudStorage(cfg, newTestLogger(), "pve.home.arpa")
		if err != nil {
			t.Fatalf("NewCloudStorage: %v", err)
		}
		listing := "" +
			"      100 2025-01-02 10:00:00.000000000 pve.home.arpa-backup-20250102-100000.tar.zst\n" +
			"       10 2025-01-02 10:00:00.000000000 pve.home.arpa-backup-20250102-100000.tar.zst.sha256\n" +
			"      100 2025-01-01 10:00:00.000000000 pve.home.arpa-backup-20250101-100000.tar.zst\n" +
			"       10 2025-01-01 10:00:00.000000000 pve.home.arpa-backup-20250101-100000.tar.zst.sha256\n"
		cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			for _, a := range args {
				if a == "lsl" {
					return []byte(listing), nil
				}
			}
			return nil, nil
		}
		cs.sleep = func(time.Duration) {}
		if _, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10}); err != nil {
			t.Fatalf("ApplyRetention: %v", err)
		}
		if summary := cs.LastRetentionSummary(); !summary.PassCompleted {
			t.Fatalf("the cloud backend ran a complete pass and reported %+v", summary)
		}
	})
}

// TestEveryReporterDisownsThePassThatBailed is the sharp case, run on the two
// backends the local tests above cannot see.
//
// It exists because the coverage was measured rather than assumed: making the
// secondary or the cloud closure publish completion UNCONDITIONALLY left the whole
// suite green, while the same mutation on the local backend turned it red. Three
// copies of one publication is three places to get it wrong, and only one of them
// was guarded. The happy-path rows in
// TestEveryRetentionReporterAnswersTheSameContract cannot catch it: a mutation that
// always says "completed" is indistinguishable from correct on a pass that did
// complete.
func TestEveryReporterDisownsThePassThatBailed(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	// Cancelled before the pass starts, so it bails at the first statement and
	// nothing at all is learned about the location.
	bailed := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	t.Run("secondary", func(t *testing.T) {
		dir := seedTwoOwnedArchives(t)
		s, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: dir}, newTestLogger(), "pve.home.arpa")
		if err != nil {
			t.Fatalf("NewSecondaryStorage: %v", err)
		}
		if _, err := s.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
			t.Fatalf("first ApplyRetention: %v", err)
		}
		if first := s.LastRetentionSummary(); !first.PassCompleted || first.BackupsDeleted != 1 {
			t.Fatalf("the secondary's first pass published %+v, want one deletion from a completed pass; the second half of this test means nothing without it", first)
		}
		if _, err := s.ApplyRetention(bailed(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err == nil {
			t.Fatal("secondary ApplyRetention on a cancelled context returned no error; this test needs the bail")
		}
		summary := s.LastRetentionSummary()
		if summary.PassCompleted {
			t.Fatalf("the secondary published %+v as a finished pass after returning an error. The secondary is the shared NAS mount, the location an operator is most likely to be reading numbers off", summary)
		}
		if summary.BackupsDeleted != 0 || summary.BackupsRemaining != 0 {
			t.Fatalf("the secondary's failed pass left the previous pass's counts standing: %+v", summary)
		}
	})

	t.Run("cloud", func(t *testing.T) {
		cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
		cs, err := NewCloudStorage(cfg, newTestLogger(), "pve.home.arpa")
		if err != nil {
			t.Fatalf("NewCloudStorage: %v", err)
		}
		listing := "" +
			"      100 2025-01-02 10:00:00.000000000 pve.home.arpa-backup-20250102-100000.tar.zst\n" +
			"       10 2025-01-02 10:00:00.000000000 pve.home.arpa-backup-20250102-100000.tar.zst.sha256\n" +
			"      100 2025-01-01 10:00:00.000000000 pve.home.arpa-backup-20250101-100000.tar.zst\n" +
			"       10 2025-01-01 10:00:00.000000000 pve.home.arpa-backup-20250101-100000.tar.zst.sha256\n"
		cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			for _, a := range args {
				if a == "lsl" {
					return []byte(listing), nil
				}
			}
			return nil, nil
		}
		cs.sleep = func(time.Duration) {}
		if _, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
			t.Fatalf("first ApplyRetention: %v", err)
		}
		if first := cs.LastRetentionSummary(); !first.PassCompleted || first.BackupsDeleted != 1 {
			t.Fatalf("the cloud's first pass published %+v, want one deletion from a completed pass", first)
		}
		if _, err := cs.ApplyRetention(bailed(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err == nil {
			t.Fatal("cloud ApplyRetention on a cancelled context returned no error; this test needs the bail")
		}
		summary := cs.LastRetentionSummary()
		if summary.PassCompleted {
			t.Fatalf("the cloud published %+v as a finished pass after returning an error", summary)
		}
		if summary.BackupsDeleted != 0 || summary.BackupsRemaining != 0 {
			t.Fatalf("the cloud's failed pass left the previous pass's counts standing: %+v", summary)
		}
	})
}

// TestScopeValidIsNotAPassRanFlagOnEveryBackend is the other sharp case on the two
// backends the local test cannot reach. Aliasing PassCompleted to ScopeValid is a
// one-token change, and it has to be refused in all three copies, not one.
func TestScopeValidIsNotAPassRanFlagOnEveryBackend(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "", nil }
	defer func() { retentionHostname = original }()

	t.Run("secondary", func(t *testing.T) {
		dir := seedTwoOwnedArchives(t)
		s, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: dir}, newTestLogger(), "")
		if err != nil {
			t.Fatalf("NewSecondaryStorage: %v", err)
		}
		if _, err := s.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
			t.Fatalf("ApplyRetention: %v", err)
		}
		summary := s.LastRetentionSummary()
		if summary.ScopeValid {
			t.Fatalf("an unnamed host published a secondary ownership scope of %d; that is what ScopeValid exists to withhold", summary.Owned)
		}
		if !summary.PassCompleted {
			t.Fatal("a complete secondary pass on a host that cannot name itself is reported as no pass at all: the two fields answer different questions")
		}
	})

	t.Run("cloud", func(t *testing.T) {
		cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
		cs, err := NewCloudStorage(cfg, newTestLogger(), "")
		if err != nil {
			t.Fatalf("NewCloudStorage: %v", err)
		}
		cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			for _, a := range args {
				if a == "lsl" {
					return []byte("      100 2025-01-01 10:00:00.000000000 pve.home.arpa-backup-20250101-100000.tar.zst\n"), nil
				}
			}
			return nil, nil
		}
		cs.sleep = func(time.Duration) {}
		if _, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
			t.Fatalf("ApplyRetention: %v", err)
		}
		summary := cs.LastRetentionSummary()
		if summary.ScopeValid {
			t.Fatalf("an unnamed host published a cloud ownership scope of %d", summary.Owned)
		}
		if !summary.PassCompleted {
			t.Fatal("a complete cloud pass on a host that cannot name itself is reported as no pass at all")
		}
	})
}
