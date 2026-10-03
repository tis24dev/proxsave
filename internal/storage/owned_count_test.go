package storage

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestOwnedBackupCountMatchesRetentionScope pins ownedBackupCount to the number
// ApplyRetention publishes as Owned before it deletes anything: the scoped slice
// applyRetentionHostScope returns plus its unmanaged tally. The two are separate
// functions only because the retention one also reports, so every population the
// scope rule distinguishes is in the fixture.
func TestOwnedBackupCountMatchesRetentionScope(t *testing.T) {
	const ourID = "1234567890123456"
	id := retentionIdentity{hostname: "pve", aliases: []string{"pve.home.arpa"}, serverID: ourID}
	listing := []*types.BackupMetadata{
		{BackupFile: "pve.home.arpa-backup-20250105-100000.tar.zst", Hostname: "pve.home.arpa"},
		{BackupFile: "pve-backup-20250104-100000.tar.zst"},
		{BackupFile: "nas.siteb.example-backup-20250103-100000.tar.zst", Hostname: "nas.siteb.example"},
		{BackupFile: "proxmox-backup-20240101-100000.tar.gz"},
		{BackupFile: "pve.old.example-backup-20250102-100000.tar.zst", Hostname: "pve.old.example"},
		{BackupFile: "pve.lost.example-backup-20250101-100000.tar.zst", Hostname: "pve.lost.example", ServerID: ourID},
		nil,
	}

	scoped, unmanaged := scopeListing(t, "Test storage", id, listing, nil)
	want := len(scoped) + unmanaged
	if got := ownedBackupCount(listing, id); got != want {
		t.Fatalf("ownedBackupCount = %d, retention scope publishes %d (scoped %d + unmanaged %d): the startup count would mean something else than the count a successful run prints", got, want, len(scoped), unmanaged)
	}
	if want == len(listing)-1 {
		t.Fatalf("fixture owns every entry (%d), so it cannot tell a scoped count from an unscoped one", want)
	}
}

// TestCountOwnedBackupsLocalEqualsWhatRetentionPublishes runs the real local backend
// over a location two hosts share: the startup count must equal the Owned a pass
// with nothing to delete publishes, and it must log nothing above Debug, because a
// WARNING at startup is counted by ParseLogCounts and pins the run at exit 1.
func TestCountOwnedBackupsLocalEqualsWhatRetentionPublishes(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := seedSharedLocation(t)
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, logger, "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	listing, err := l.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	before := buf.Len()

	owned, ok := CountOwnedBackups(context.Background(), l, listing)
	if !ok || owned != 3 {
		t.Fatalf("CountOwnedBackups = (%d, %v), want (3, true): three of the five archives are this host's", owned, ok)
	}
	if logged := buf.String()[before:]; strings.Contains(logged, "WARNING") || strings.Contains(logged, "INFO") {
		t.Fatalf("CountOwnedBackups logged above Debug:\n%s", logged)
	}

	if _, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if summary := l.LastRetentionSummary(); summary.Owned != owned {
		t.Fatalf("retention publishes Owned=%d, the startup count is %d: the failure path would print a number with another meaning", summary.Owned, owned)
	}
}

// TestCountOwnedBackupsCloudReadsManifestsWithoutTouchingTheListing pins the two
// things the cloud arm adds. The owner comes from the manifest (one cat per archive,
// as in retention), so an archive whose FILENAME names another host but whose
// manifest names this one is counted. And the listing the caller passed is left as
// it was: the owner lookup writes into the entries it is given.
func TestCountOwnedBackupsCloudReadsManifestsWithoutTouchingTheListing(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	cs, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: "gdrive"}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	var mu sync.Mutex
	var cats []string
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) == 0 || args[0] != "cat" {
			t.Errorf("unexpected rclone call: %v", args)
			return nil, context.Canceled
		}
		path := args[len(args)-1]
		mu.Lock()
		cats = append(cats, path)
		mu.Unlock()
		if strings.Contains(path, "nas.siteb.example") {
			return []byte(`{"hostname":"nas.siteb.example"}`), nil
		}
		return []byte(`{"hostname":"pve.home.arpa"}`), nil
	}

	listing := []*types.BackupMetadata{
		{BackupFile: "pve.home.arpa-backup-20250103-100000.tar.zst"},
		{BackupFile: "server9-backup-20250102-100000.tar.zst"},
		{BackupFile: "nas.siteb.example-backup-20250101-100000.tar.zst"},
	}
	snapshot := make([]types.BackupMetadata, len(listing))
	for i, b := range listing {
		snapshot[i] = *b
	}

	owned, ok := CountOwnedBackups(context.Background(), cs, listing)
	if !ok || owned != 2 {
		t.Fatalf("CountOwnedBackups = (%d, %v), want (2, true): the manifest names this host for two archives", owned, ok)
	}
	if len(cats) != len(listing) {
		t.Fatalf("manifest reads = %v, want one per archive", cats)
	}
	for i, b := range listing {
		if !reflect.DeepEqual(*b, snapshot[i]) {
			t.Fatalf("listing entry %d changed: %+v, was %+v", i, *b, snapshot[i])
		}
	}
}

// TestCountOwnedBackupsRefusesWhatRetentionWouldNotScope pins the false answers: a
// host that cannot name itself (ApplyRetention leaves ScopeValid unset there), a
// nil backend pointer, and a backend this package does not implement. Each makes
// the caller keep the unscoped total.
func TestCountOwnedBackupsRefusesWhatRetentionWouldNotScope(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "", nil }
	defer func() { retentionHostname = original }()

	dir := seedSharedLocation(t)
	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	listing, err := l.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if owned, ok := CountOwnedBackups(context.Background(), l, listing); ok {
		t.Fatalf("unnamed host: CountOwnedBackups = (%d, true), want ok=false", owned)
	}
	var nilLocal *LocalStorage
	if _, ok := CountOwnedBackups(context.Background(), nilLocal, listing); ok {
		t.Fatal("nil *LocalStorage: want ok=false")
	}
	if _, ok := CountOwnedBackups(context.Background(), nil, listing); ok {
		t.Fatal("nil backend: want ok=false")
	}
}
