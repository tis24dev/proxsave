package storage

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// serverIDSeed describes one archive of a retention fixture: its name, its mtime, and
// what its manifest says about the machine that wrote it.
type serverIDSeed struct {
	name string
	when time.Time
	// manifestHost and manifestID both empty seed NO .metadata at all. A manifest
	// with an identity and no host is still written, and its archive is attributed
	// by the filename token.
	manifestHost string
	manifestID   string
}

// seedServerIDFixture writes a retention fixture into dir and returns the archive
// paths in the order given. Every archive carries a .sha256 because
// backupHasCompletionSidecar accepts .manifest.json, .sha256 or a bundle suffix but
// NOT .metadata, and an unverified entry is inert for retention: without it these
// tests would pass for the wrong reason.
func seedServerIDFixture(t *testing.T, dir string, seeds []serverIDSeed) []string {
	t.Helper()
	paths := make([]string, len(seeds))
	for i, seed := range seeds {
		path := filepath.Join(dir, seed.name)
		paths[i] = path
		if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", seed.name, err)
		}
		if err := os.WriteFile(path+".sha256", []byte("h  archive\n"), 0o600); err != nil {
			t.Fatalf("seed sidecar for %s: %v", seed.name, err)
		}
		if seed.manifestHost != "" || seed.manifestID != "" {
			// created_at matches the mtime set below: loadMetadata takes the
			// timestamp from the manifest and only falls back to ModTime when it is
			// zero, so letting the two disagree would make the ordering depend on
			// which source won.
			manifest := fmt.Sprintf(`{"created_at":%q`, seed.when.Format(time.RFC3339))
			if seed.manifestHost != "" {
				manifest += fmt.Sprintf(`,"hostname":%q`, seed.manifestHost)
			}
			if seed.manifestID != "" {
				manifest += fmt.Sprintf(`,"server_id":%q`, seed.manifestID)
			}
			manifest += "}"
			if err := os.WriteFile(path+".metadata", []byte(manifest), 0o600); err != nil {
				t.Fatalf("seed manifest for %s: %v", seed.name, err)
			}
		}
		if err := os.Chtimes(path, seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name, err)
		}
	}
	return paths
}

// newRecordingRetentionLogger returns a debug-level logger writing into a buffer, so
// a test can assert on what an operator would actually read as well as on what
// retention did.
func newRecordingRetentionLogger() (*logging.Logger, *bytes.Buffer) {
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	return logger, &buf
}

// lostFQDNSeeds is the discussion #292 shape: three archives this machine wrote under
// the name "hostname -f" returned, on a machine that now resolves only the kernel
// short name.
func lostFQDNSeeds(serverID string) []serverIDSeed {
	return []serverIDSeed{
		{name: "pve.home.arpa-backup-20250103-100000.tar.zst", when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa", manifestID: serverID},
		{name: "pve.home.arpa-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa", manifestID: serverID},
		{name: "pve.home.arpa-backup-20250101-100000.tar.zst", when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa", manifestID: serverID},
	}
}

// TestLocalRetentionAdoptsArchivesWrittenUnderALostFQDN is discussion #292 end to end
// on the backend every user has, in the state the reporter is actually in: the writer
// stamped "pve.home.arpa" into the archives, the machine no longer resolves that name
// at all, so this run has NO alias to match them with and before this change scoping
// left nothing owned and the directory grew for ever.
//
// The archives carry this host's own server identity, and the same server identity is
// the same server, so retention adopts them and they rotate again.
//
// It runs through NewLocalStorage and asserts on the filesystem rather than on the
// struct, so it observes the whole chain: cfg.ServerID reaches the backend, the
// manifest's server_id reaches BackupMetadata through loadMetadata, and ApplyRetention
// reads both. The unit table cannot see any of those seams.
func TestLocalRetentionAdoptsArchivesWrittenUnderALostFQDN(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	paths := seedServerIDFixture(t, dir, lostFQDNSeeds(ourServerID))

	logger, buf := newRecordingRetentionLogger()
	// The written hostname is deliberately empty: this machine can no longer resolve
	// the name the archives carry, which is the whole point of the fixture.
	l, err := NewLocalStorage(&config.Config{BackupPath: dir, ServerID: ourServerID}, logger, "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	if _, err := os.Stat(paths[0]); err != nil {
		t.Errorf("retention deleted the archive it was told to keep: %v", err)
	}
	for _, path := range paths[1:] {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("retention spared this host's own surplus archive %s: it names a spelling this host lost but carries this host's own server identity, so it must rotate again (stat err=%v)", filepath.Base(path), err)
		}
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2; the count feeds the run summary and the retention report", deleted)
	}
	if strings.Contains(buf.String(), "not rotated") {
		t.Errorf("retention still reported these archives as not rotated after adopting them. The outcome then promotes the run to exit 1 for a condition that no longer exists. Log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "  Adopted: 3 backups named pve.home.arpa, same server identity") {
		t.Errorf("nothing said the archives had been brought back. The recovery is invisible to the operator otherwise. Log: %s", buf.String())
	}
	if n := logger.WarningCount(); n != 0 {
		t.Errorf("%d WARNING line(s) for a location this host now fully manages; every one of them promotes an otherwise clean run to exit 1. Log: %s", n, buf.String())
	}
}

// TestLocalRetentionLeavesTheSameFixtureAloneWithoutAServerIdentity is the control for
// the test above, and it is what proves the adoption really came from the identity
// rather than from something that widened the hostname rule. The fixture is identical
// except that the archives record no server identity, which is every archive written
// before this change: nothing is deleted and the not-rotated fact names them.
func TestLocalRetentionLeavesTheSameFixtureAloneWithoutAServerIdentity(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	paths := seedServerIDFixture(t, dir, lostFQDNSeeds(""))

	logger, buf := newRecordingRetentionLogger()
	l, err := NewLocalStorage(&config.Config{BackupPath: dir, ServerID: ourServerID}, logger, "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("retention deleted %s. It names a spelling this host does not answer to and records no identity, so from here it is indistinguishable from a second machine's work (stat err=%v)", filepath.Base(path), err)
		}
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0: an archive with no server identity must be classified exactly as it was before the field existed", deleted)
	}
	if !strings.Contains(buf.String(), "  Named pve.home.arpa, not rotated: 3 backups") {
		t.Errorf("the not-rotated fact stopped firing for a population nothing has claimed. The operator's only signal that rotation has stopped is that line. Log: %s", buf.String())
	}
	if summary := l.LastRetentionSummary(); summary.NotRotated != 3 || summary.NotRotatedNames != "pve.home.arpa" {
		t.Errorf("summary not rotated = %d %q, want 3 \"pve.home.arpa\": the outcome line reads them", summary.NotRotated, summary.NotRotatedNames)
	}
}

// TestLocalRetentionRotatesEveryArchiveCarryingOurServerIdentity is the rule end to
// end, on real files: the same server identity is the same server, whatever name the
// archive carries. Each case is a name the hostname rule alone refuses, and each is
// run twice: with this host's identity the archives rotate and the run says they were
// adopted; with another machine's identity nothing is deleted, exactly as before.
//
// The competing-spelling case is the clone: this host still resolves its own FQDN and
// the archives name a third spelling of its short name. A clone with this host's
// identity writing to the same location has its backups rotated by this host, which
// was stated and accepted when the rule was decided.
func TestLocalRetentionRotatesEveryArchiveCarryingOurServerIdentity(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	cases := []struct {
		name string
		// written is the name this run's writer stamps, the alias retention answers to.
		written string
		// file is the token the archive names carry; manifestHost what the manifest says.
		file         string
		manifestHost string
		// adoptedName is the name the adoption fact prints.
		adoptedName string
		// notRotatedOtherwise is the fact printed with another machine's identity, ""
		// when the archives are another host's by name and get no line.
		notRotatedOtherwise string
	}{
		{name: "a competing spelling of our short name", written: "pve.home.arpa", file: "pve.siteb.example", manifestHost: "pve.siteb.example", adoptedName: "pve.siteb.example", notRotatedOtherwise: "  Named pve.siteb.example, not rotated: 3 backups"},
		{name: "another short label", file: "nas.lan", manifestHost: "nas.lan", adoptedName: "nas.lan"},
		{name: "a bare name", file: "srv", manifestHost: "srv", adoptedName: "srv"},
		{name: "a manifest with no host", file: "pve.home.arpa", adoptedName: "pve.home.arpa", notRotatedOtherwise: "  Named pve.home.arpa, not rotated: 3 backups"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seeds := func(serverID string) []serverIDSeed {
				var out []serverIDSeed
				for day := 3; day >= 1; day-- {
					out = append(out, serverIDSeed{
						name:         fmt.Sprintf("%s-backup-2025010%d-100000.tar.zst", tc.file, day),
						when:         time.Date(2025, 1, day, 10, 0, 0, 0, time.UTC),
						manifestHost: tc.manifestHost,
						manifestID:   serverID,
					})
				}
				return out
			}
			run := func(t *testing.T, archiveID string) (deleted int, survivors []string, log string, warnings int64) {
				t.Helper()
				dir := t.TempDir()
				paths := seedServerIDFixture(t, dir, seeds(archiveID))
				logger, buf := newRecordingRetentionLogger()
				l, err := NewLocalStorage(&config.Config{BackupPath: dir, ServerID: ourServerID}, logger, tc.written)
				if err != nil {
					t.Fatalf("NewLocalStorage: %v", err)
				}
				deleted, err = l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
				if err != nil {
					t.Fatalf("ApplyRetention: %v", err)
				}
				for _, path := range paths {
					if _, err := os.Stat(path); err == nil {
						survivors = append(survivors, filepath.Base(path))
					}
				}
				return deleted, survivors, buf.String(), logger.WarningCount()
			}

			deleted, survivors, log, warnings := run(t, ourServerID)
			if want := fmt.Sprintf("%s-backup-20250103-100000.tar.zst", tc.file); deleted != 2 || strings.Join(survivors, ",") != want {
				t.Errorf("with this host's identity: deleted %d, left %v; want 2 deleted and only %s left. Same server identity, same server", deleted, survivors, want)
			}
			if want := "  Adopted: 3 backups named " + tc.adoptedName + ", same server identity"; !strings.Contains(log, want) {
				t.Errorf("the adoption fact %q is missing. Log: %s", want, log)
			}
			if strings.Contains(log, "not rotated") {
				t.Errorf("archives this host rotates were reported as not rotated. Log: %s", log)
			}
			if warnings != 0 {
				t.Errorf("%d WARNING line(s) from a pass that manages every archive here. Log: %s", warnings, log)
			}

			deleted, survivors, log, _ = run(t, anotherServerID)
			if deleted != 0 || len(survivors) != 3 {
				t.Errorf("with another machine's identity: deleted %d, left %v; want nothing deleted, exactly as before the rule", deleted, survivors)
			}
			if strings.Contains(log, "Adopted:") {
				t.Errorf("archives carrying another machine's identity were adopted. Log: %s", log)
			}
			if tc.notRotatedOtherwise != "" && !strings.Contains(log, tc.notRotatedOtherwise) {
				t.Errorf("the not-rotated fact %q is missing. Log: %s", tc.notRotatedOtherwise, log)
			}
			if tc.notRotatedOtherwise == "" && strings.Contains(log, "not rotated") {
				t.Errorf("another host's archives were reported. Log: %s", log)
			}
		})
	}
}

// TestLocalRetentionIsUnchangedByAServerIdentityOnPreExistingArchives is the
// constraint that covers the entire installed base: no archive that exists today
// carries a server identity, so a host that gains one must classify, delete, count and
// report byte for byte as it did before.
//
// It runs the SAME five-archive fixture the pre-existing FQDN test uses, twice, once
// with cfg.ServerID empty and once with a valid identity, and compares the outcomes
// rather than restating them. Comparing is what makes the assertion total: any future
// change that moves a deletion, a published count or a severity in the presence of an
// identity fails here without anybody having to have predicted which one.
func TestLocalRetentionIsUnchangedByAServerIdentityOnPreExistingArchives(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	seeds := []serverIDSeed{
		{name: "pve.home.arpa-backup-20250105-100000.tar.zst", when: time.Date(2025, 1, 5, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa"},
		{name: "pve.siteb.example-backup-20250104-100000.tar.zst", when: time.Date(2025, 1, 4, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa"},
		{name: "pve.home.arpa-backup-20250103-100000.tar.zst", when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), manifestHost: "pve.siteb.example"},
		{name: "pve.home.arpa-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)},
		{name: "pve.siteb.example-backup-20250101-100000.tar.zst", when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)},
	}

	type outcome struct {
		deleted   int
		survivors []string
		owned     int
		scopeOK   bool
		warnings  int64
	}

	run := func(t *testing.T, serverID string) outcome {
		t.Helper()
		dir := t.TempDir()
		paths := seedServerIDFixture(t, dir, seeds)

		logger, _ := newRecordingRetentionLogger()
		l, err := NewLocalStorage(&config.Config{BackupPath: dir, ServerID: serverID}, logger, "pve.home.arpa")
		if err != nil {
			t.Fatalf("NewLocalStorage: %v", err)
		}
		deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
		if err != nil {
			t.Fatalf("ApplyRetention: %v", err)
		}

		var survivors []string
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				survivors = append(survivors, filepath.Base(path))
			}
		}
		summary := l.LastRetentionSummary()
		return outcome{deleted: deleted, survivors: survivors, owned: summary.Owned, scopeOK: summary.ScopeValid, warnings: logger.WarningCount()}
	}

	without := run(t, "")
	with := run(t, ourServerID)

	if without.deleted != with.deleted || strings.Join(without.survivors, ",") != strings.Join(with.survivors, ",") {
		t.Errorf("a host that gained a server identity pruned a different set of archives from the same directory.\n without identity: deleted=%d survivors=%v\n with identity:    deleted=%d survivors=%v\nNo archive here records one, so the identity has nothing to compare and must change nothing at all", without.deleted, without.survivors, with.deleted, with.survivors)
	}
	if without.owned != with.owned || without.scopeOK != with.scopeOK {
		t.Errorf("the published retention scope moved: owned %d (valid=%v) without an identity, %d (valid=%v) with one. That number is what the notification prints beside the configured limit", without.owned, without.scopeOK, with.owned, with.scopeOK)
	}
	if without.warnings != with.warnings {
		t.Errorf("the warning count moved from %d to %d. Every WARNING line promotes an otherwise clean run to exit 1 through applyIssueExitCode, so a severity change here is an exit-code change for every upgraded host", without.warnings, with.warnings)
	}
}
