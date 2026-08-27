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
	// manifestHost empty seeds NO .metadata at all, so attribution falls through to
	// the filename token, which is the degraded path the adoption rule must refuse.
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
		if seed.manifestHost != "" {
			// created_at matches the mtime set below: loadMetadata takes the
			// timestamp from the manifest and only falls back to ModTime when it is
			// zero, so letting the two disagree would make the ordering depend on
			// which source won.
			manifest := fmt.Sprintf(`{"hostname":%q,"created_at":%q`, seed.manifestHost, seed.when.Format(time.RFC3339))
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
// The archives carry this host's own server identity, this host answers to their short
// label bare and to no other spelling of it, so they are this machine's own work under
// a name it lost, and retention brings them back into rotation.
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
	if strings.Contains(buf.String(), "different spelling") {
		t.Errorf("retention still reported these archives as an unresolvable spelling mismatch after adopting them. The warning promotes the run to exit 1 for a condition that no longer exists. Log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "back into rotation") {
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
// before this change: nothing is deleted and the existing warning still fires, word
// for word.
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
	if !strings.Contains(buf.String(), "different spelling") {
		t.Errorf("the pre-existing spelling-mismatch warning stopped firing for a population nothing has claimed. The operator's only signal that rotation has stopped is that line. Log: %s", buf.String())
	}
}

// TestLocalRetentionRefusesASecondSiteCarryingOurServerIdentity is the data-loss
// boundary end to end. This host still resolves its own FQDN, so it answers to two
// spellings of the short label "pve", and the archives on the shared mount name a
// THIRD spelling while carrying this host's identity.
//
// That is exactly what a clone or a restored container looks like from here, and
// inheriting the source machine's identity is expected, supported behaviour. Claiming
// these archives would be a clone pruning the source machine's backups, so retention
// refuses and says why.
func TestLocalRetentionRefusesASecondSiteCarryingOurServerIdentity(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	paths := seedServerIDFixture(t, dir, []serverIDSeed{
		{name: "pve.siteb.example-backup-20250103-100000.tar.zst", when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), manifestHost: "pve.siteb.example", manifestID: ourServerID},
		{name: "pve.siteb.example-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC), manifestHost: "pve.siteb.example", manifestID: ourServerID},
	})

	logger, buf := newRecordingRetentionLogger()
	// This host DOES still resolve its own qualified name, which is the competing
	// spelling that disqualifies it from adopting a third one.
	l, err := NewLocalStorage(&config.Config{BackupPath: dir, ServerID: ourServerID}, logger, "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("retention deleted %s. This host answers to another spelling of that short name, so these archives may be a second machine, or a clone of this one that inherited the identity, and deleting them is unrecoverable (stat err=%v)", filepath.Base(path), err)
		}
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
	if !strings.Contains(buf.String(), "carry this host's own server identity") {
		t.Errorf("nothing told the operator that the refused archives carry this host's own identity, which is the one fact that explains why they are being left alone. Log: %s", buf.String())
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
