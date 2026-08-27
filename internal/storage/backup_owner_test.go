package storage

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestBackupOwnerHost(t *testing.T) {
	tests := []struct {
		name string
		meta types.BackupMetadata
		want string
	}{
		{
			name: "the manifest is authoritative",
			meta: types.BackupMetadata{BackupFile: "server2-backup-20250102-100000.tar.zst", Hostname: "server1"},
			want: "server1",
		},
		{
			name: "no manifest falls back to the filename token",
			meta: types.BackupMetadata{BackupFile: "server1-backup-20250102-100000.tar.zst"},
			want: "server1",
		},
		{
			name: "the fallback reads the basename, not the path",
			meta: types.BackupMetadata{BackupFile: "/mnt/nas/server1-backup-20250102-100000.tar.zst"},
			want: "server1",
		},
		{
			// "proxmox" is the product name, not a host: attributing it would stop
			// every other machine from rotating its own legacy archives.
			name: "a legacy name carries no host token",
			meta: types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz"},
			want: "",
		},
		{
			name: "an unparsable name yields nothing",
			meta: types.BackupMetadata{BackupFile: "something-else.tar.gz"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backupOwnerHost(&tt.meta); got != tt.want {
				t.Fatalf("backupOwnerHost = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBackupBelongsToHost(t *testing.T) {
	tests := []struct {
		name     string
		meta     *types.BackupMetadata
		hostname string
		// written are the names this run's writer stamped into the archives it
		// produced. A nil slice reproduces the strict pre-alias rule exactly.
		written []string
		want    bool
	}{
		{name: "own backup", meta: &types.BackupMetadata{Hostname: "server1"}, hostname: "server1", want: true},
		{name: "case insensitive", meta: &types.BackupMetadata{Hostname: "SERVER1"}, hostname: "server1", want: true},
		{name: "another host", meta: &types.BackupMetadata{Hostname: "server2"}, hostname: "server1", want: false},
		// Fail-closed: an entry nobody can attribute is left alone rather than
		// deleted on a guess, and a machine that cannot name itself claims nothing.
		{name: "unattributable", meta: &types.BackupMetadata{BackupFile: "mystery.tar.gz"}, hostname: "server1", want: false},
		{name: "unknown local hostname", meta: &types.BackupMetadata{Hostname: "server1"}, hostname: "", want: false},
		{name: "nil entry", meta: nil, hostname: "server1", want: false},
		// A legacy archive carries no host token, so when its manifest names no host
		// either it is attributable to nobody. It used to be claimed by whoever
		// listed it, which on a shared directory or remote prefix is one machine
		// deleting another machine's backups (discussion #292). Nothing about the
		// location can change this answer, which is the point.
		{name: "an unattributable legacy name is nobody's", meta: &types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz"}, hostname: "server1", want: false},
		{name: "legacy name with a foreign manifest is not ours", meta: &types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz", Hostname: "server2"}, hostname: "server1", want: false},

		// A machine does not always spell its own name the same way: the writer
		// records what "hostname -f" returns while os.Hostname reports the kernel
		// short name. Retention answers to both, and to nothing else.
		{name: "an FQDN this run wrote under is ours", meta: &types.BackupMetadata{Hostname: "pve.home.arpa"}, hostname: "pve", written: []string{"pve.home.arpa"}, want: true},
		{name: "an FQDN filename token this run wrote under is ours", meta: &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst"}, hostname: "pve", written: []string{"pve.home.arpa"}, want: true},
		{name: "case and a trailing root dot are the same name", meta: &types.BackupMetadata{Hostname: "PVE.Home.Arpa."}, hostname: "pve", written: []string{"pve.home.arpa"}, want: true},
		{name: "a legacy name carrying this host's FQDN manifest is ours", meta: &types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz", Hostname: "pve.home.arpa"}, hostname: "pve", written: []string{"pve.home.arpa"}, want: true},

		// THE DATA-LOSS BOUNDARY. These rows are green before and after the alias
		// change, and they turn RED the moment ownership folds to the first label:
		// folding "pve" onto "pve.siteb.example" would let this host count and prune
		// another machine's archives out of a shared location.
		{name: "an FQDN this host never wrote under is another machine", meta: &types.BackupMetadata{Hostname: "pve.siteb.example"}, hostname: "pve", written: nil, want: false},
		{name: "a bare name this host never wrote under is another machine", meta: &types.BackupMetadata{Hostname: "pve"}, hostname: "pve.siteA.example", written: nil, want: false},
		{name: "a different domain is another machine even when this host is qualified", meta: &types.BackupMetadata{Hostname: "pve.siteb.example"}, hostname: "pve", written: []string{"pve.sitea.example"}, want: false},
		// A machine that could not name itself must not become everyone's owner.
		{name: "the unknown sentinel is not a name this host answers to", meta: &types.BackupMetadata{Hostname: "unknown"}, hostname: "pve", written: []string{"unknown"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backupBelongsToHost(tt.meta, tt.hostname, tt.written...); got != tt.want {
				t.Fatalf("backupBelongsToHost = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestScopeRetentionToHostKeepsSingleHostLocations is the no-regression pin: on the
// ordinary layout - one host writing into its own directory - scoping must be a
// no-op, or retention would silently stop pruning and the location would grow
// without bound.
func TestScopeRetentionToHostKeepsSingleHostLocations(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve1-backup-20250103-100000.tar.zst", Hostname: "pve1"},
		{BackupFile: "pve1-backup-20250102-100000.tar.zst", Hostname: "pve1"},
		{BackupFile: "pve1-backup-20250101-100000.tar.zst"}, // manifest unreadable
	}

	owned, foreign := scopeRetentionToHost(backups, "pve1")

	if len(owned) != len(backups) {
		t.Fatalf("kept %d of %d; a single-host location must not shrink: foreign=%+v", len(owned), len(backups), foreign)
	}
}

// TestScopeRetentionToHostKeepsFQDNSingleHostLocations is the FQDN twin of the pin
// above, and it is the reporter's symptom at unit level. The writer stamps what
// "hostname -f" returns (pve.home.arpa) while retention reads os.Hostname (pve), so
// a perfectly ordinary single-host location scoped to nothing: owned was 0, foreign
// was 3, and retention stopped pruning entirely.
func TestScopeRetentionToHostKeepsFQDNSingleHostLocations(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve.home.arpa-backup-20250103-100000.tar.zst", Hostname: "pve.home.arpa"},
		{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa"},
		{BackupFile: "pve.home.arpa-backup-20250101-100000.tar.zst"}, // manifest unreadable
	}

	owned, foreign := scopeRetentionToHost(backups, "pve", "pve.home.arpa")

	if len(owned) != len(backups) || len(foreign) != 0 {
		t.Fatalf("kept %d of %d (foreign=%d); every archive here was written by this machine", len(owned), len(backups), len(foreign))
	}
}

// TestScopeRetentionToHostLeavesSameShortNameForeignHostAlone is the data-loss
// boundary at scope level. This host is a stock node called "pve" whose "hostname -f"
// fails, so it has no aliases at all, and it shares a location with a second machine
// that resolves to "pve.siteb.example". The two share a short label and are NOT the
// same machine. Ownership must stay an exact match against the names this machine
// itself answers to: a fold to the first label turns this test red and lets retention
// count and delete the other machine's archives.
func TestScopeRetentionToHostLeavesSameShortNameForeignHostAlone(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve-backup-20250103-100000.tar.zst", Hostname: "pve"},
		{BackupFile: "pve.siteb.example-backup-20250102-100000.tar.zst", Hostname: "pve.siteb.example"},
	}

	owned, foreign := scopeRetentionToHost(backups, "pve")

	if len(owned) != 1 || owned[0].Hostname != "pve" {
		t.Fatalf("owned = %+v, want only this host's own archive", owned)
	}
	if len(foreign) != 1 || foreign[0].Hostname != "pve.siteb.example" {
		t.Fatalf("foreign = %+v, want the other machine's archive left out of scope", foreign)
	}
}

// TestRetentionHostAliases pins what does and does not join the identity set. The
// empty case is the important one: a machine with no domain must end up with no
// aliases, so its behaviour is byte-identical to the strict rule it had before.
func TestRetentionHostAliases(t *testing.T) {
	got := retentionHostAliases("pve", []string{"pve.home.arpa", "PVE", "", " ", "unknown", "PVE.Home.Arpa."})
	want := []string{"pve.home.arpa"}

	if len(got) != len(want) {
		t.Fatalf("aliases = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("aliases = %q, want %q", got, want)
		}
	}
	if aliases := retentionHostAliases("pve", nil); len(aliases) != 0 {
		t.Fatalf("aliases = %q, want none: a machine with no domain must stay exactly as strict as before", aliases)
	}
}

// TestRetentionSpellingMismatchesCountsLikelySelf pins the reporting helper behind
// the second warning line. It never decides ownership: it only tells the operator
// that some out-of-scope archives carry this host's short name under a spelling this
// run cannot confirm, which is the one case the fix deliberately declines to solve.
func TestRetentionSpellingMismatchesCountsLikelySelf(t *testing.T) {
	foreign := []*types.BackupMetadata{
		{Hostname: "pve.siteb.example", BackupFile: "pve.siteb.example-backup-20250102-100000.tar.zst"},
		{Hostname: "pbs.home.arpa", BackupFile: "pbs.home.arpa-backup-20250102-100000.tar.zst"},
		nil,
	}

	if got := retentionSpellingMismatches(foreign, "pve"); got != 1 {
		t.Fatalf("mismatches = %d, want 1", got)
	}
	if got := retentionSpellingMismatches(foreign, ""); got != 0 {
		t.Fatalf("mismatches = %d, want 0 when this machine cannot name itself", got)
	}
}

// TestApplyRetentionHostScopeDeletesNothingWhenThisMachineCannotNameItself pins the
// fail-closed branch, the only branch in the retention path whose failure mode is
// deleting other machines' archives rather than deleting nothing. os.Hostname
// failing is rare on Linux, which is why this is a guard rather than a hot path,
// but the shipped CLOUD_REMOTE_PATH default is a shared root
// (internal/config/templates/backup.env) and the documented secondary layout is a
// NAS several hosts write into, so "scope by nothing" means "prune everything in
// the listing, including theirs".
//
// It asserts the warning as well as the empty result, and both halves are load
// bearing. Returning the listing instead of nil is caught by the length; deleting
// the guard outright is NOT, because backupBelongsToHost already refuses a blank
// hostname, so scoping still yields nothing. What that mutation really removes is
// this warning, which is the operator's only signal that retention is off for the
// run. Rewording the message therefore turns this test red on purpose: read it as
// a prompt to check who else quotes the wording, not as a retention bug.
//
// Both entries carry a non-empty Hostname on purpose: they are attributable, so the
// only reason they are out of scope is the blank local name.
func TestApplyRetentionHostScopeDeletesNothingWhenThisMachineCannotNameItself(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve-backup-20250101-100000.tar.zst", Hostname: "pve"},
		{BackupFile: "other-backup-20250101-100000.tar.zst", Hostname: "other"},
	}

	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	scoped, _ := applyRetentionHostScope("Local storage", "", nil, backups, logger)

	if len(scoped) != 0 {
		t.Errorf("scoped %d of %d entries; a machine that cannot name itself must delete nothing, not everything: on a shared location these are another machine's backups", len(scoped), len(backups))
	}
	if !strings.Contains(buf.String(), "the local hostname is unknown") {
		t.Errorf("the blank-hostname guard printed no warning; retention silently doing nothing is indistinguishable from retention working. Got: %s", buf.String())
	}
}

// TestNewLocalStorageRecordsWrittenHostnames is the wiring pin at the receiving end:
// the name package main resolved for this run has to land where retention reads it.
func TestNewLocalStorageRecordsWrittenHostnames(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	l, err := NewLocalStorage(&config.Config{BackupPath: t.TempDir()}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	if l.hostname != "pve" {
		t.Fatalf("hostname = %q, want pve", l.hostname)
	}
	if len(l.hostAliases) != 1 || l.hostAliases[0] != "pve.home.arpa" {
		t.Fatalf("hostAliases = %q, want [pve.home.arpa]", l.hostAliases)
	}

	strict, err := NewLocalStorage(&config.Config{BackupPath: t.TempDir()}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	if len(strict.hostAliases) != 0 {
		t.Fatalf("hostAliases = %q, want none when no written name is supplied", strict.hostAliases)
	}
}

// TestNewSecondaryStorageRecordsWrittenHostnames is the wiring pin for the
// secondary backend. A shared NAS is one of the two places the reported bug was
// seen, and the run's own FQDN only reaches retention if the constructor stores
// it: nothing else in the tree observes this, because initializeSecondaryStorage
// returns only a *FilesystemInfo and the field retention reads is unexported.
func TestNewSecondaryStorageRecordsWrittenHostnames(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	s, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	if s.hostname != "pve" {
		t.Fatalf("hostname = %q, want pve", s.hostname)
	}
	if len(s.hostAliases) != 1 || s.hostAliases[0] != "pve.home.arpa" {
		t.Fatalf("hostAliases = %q, want [pve.home.arpa]", s.hostAliases)
	}

	strict, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	if len(strict.hostAliases) != 0 {
		t.Fatalf("hostAliases = %q, want none when no written name is supplied", strict.hostAliases)
	}
}

// TestNewCloudStorageRecordsWrittenHostnames is the wiring pin for the cloud
// backend. The shipped CLOUD_REMOTE_PATH is a shared root (backup.env:171) and
// cloud is where the reporter saw the warning, so this is the backend that most
// needs the alias to arrive.
func TestNewCloudStorageRecordsWrittenHostnames(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	c, err := NewCloudStorage(cfg, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	if c.hostname != "pve" {
		t.Fatalf("hostname = %q, want pve", c.hostname)
	}
	if len(c.hostAliases) != 1 || c.hostAliases[0] != "pve.home.arpa" {
		t.Fatalf("hostAliases = %q, want [pve.home.arpa]", c.hostAliases)
	}

	strict, err := NewCloudStorage(cfg, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	if len(strict.hostAliases) != 0 {
		t.Fatalf("hostAliases = %q, want none when no written name is supplied", strict.hostAliases)
	}
}

// TestApplyRetentionDoesNotDeleteOtherHostsBackups is the end-to-end regression pin
// for the reported data-loss bug. host "server1" is listed at a remote root that
// also holds server2's and server3's archives; with MaxBackups=1 those two are the
// "oldest" and were deleted, irreversibly and with only a Debug line naming them.
func TestApplyRetentionDoesNotDeleteOtherHostsBackups(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	cs := newCloudStorageForTest(cfg)
	cs.hostname = "server1"

	listing := "" +
		"      100 2025-01-03 10:00:00.000000000 server1-backup-20250103-100000.tar.zst\n" +
		"       10 2025-01-03 10:00:00.000000000 server1-backup-20250103-100000.tar.zst.sha256\n" +
		"      100 2025-01-02 10:00:00.000000000 server1-backup-20250102-100000.tar.zst\n" +
		"       10 2025-01-02 10:00:00.000000000 server1-backup-20250102-100000.tar.zst.sha256\n" +
		"      100 2025-01-02 10:00:00.000000000 server2-backup-20250102-100000.tar.zst\n" +
		"       10 2025-01-02 10:00:00.000000000 server2-backup-20250102-100000.tar.zst.sha256\n" +
		"      100 2025-01-01 10:00:00.000000000 server3-backup-20250101-100000.tar.zst\n" +
		"       10 2025-01-01 10:00:00.000000000 server3-backup-20250101-100000.tar.zst.sha256\n"

	var calls []commandCall
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, commandCall{name: name, args: append([]string(nil), args...)})
		for _, a := range args {
			if a == "lsl" {
				return []byte(listing), nil
			}
		}
		// Every `cat` returns nothing usable, so attribution degrades to the
		// filename token - the path an operator with unreadable manifests is on.
		return nil, nil
	}

	if _, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	deletedForeign := false
	deletedOwn := false
	for _, call := range calls {
		joined := strings.Join(call.args, " ")
		if !strings.Contains(joined, "delete") {
			continue
		}
		if strings.Contains(joined, "server2") || strings.Contains(joined, "server3") {
			deletedForeign = true
		}
		if strings.Contains(joined, "server1") {
			deletedOwn = true
		}
	}
	if deletedForeign {
		t.Errorf("retention deleted another host's backup: %+v", calls)
	}
	// The scoping must not have disabled retention altogether: this host is over
	// its own limit of 1 and its older archive still has to go.
	if !deletedOwn {
		t.Errorf("retention deleted nothing of this host's own: %+v", calls)
	}
}
