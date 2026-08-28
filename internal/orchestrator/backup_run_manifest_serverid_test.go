package orchestrator

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// runServerID is a well formed server identity of the shape internal/identity mints:
// exactly sixteen decimal digits. Ownership compares it as bytes and never parses it,
// so nothing about its numeric value matters.
// runServerID carries a LEADING ZERO on purpose. identity.normalizeServerID left-pads
// to sixteen digits, so this is a shape the minting side really produces, and it is
// the only shape that survives being sent through a numeric parse and back. With a
// zero-free value every assertion in this file stays green under a plumb that parses
// the identity as a number somewhere along the way, which silently turns
// "0123456789012345" into "123456789012345" and stops it matching anything.
const runServerID = "0123456789012345"

// seedArchiveForManifestWrite writes the archive file the manifest describes and
// returns the run context, artifacts and workspace the write path is given, all
// pointing at one temp directory.
//
// It builds the stats through InitializeBackupStats rather than by hand, because that
// is the hop before the manifest: it is where the identity package main resolved and
// handed to the orchestrator through SetIdentity becomes stats.ServerID. A hand built
// BackupStats would skip it and the test would pin one link of a two link chain.
func seedArchiveForManifestWrite(t *testing.T, serverID string) (*Orchestrator, *backupRunContext, *backupArtifacts, *backupWorkspace) {
	t.Helper()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "pve.home.arpa-backup-20250102-100000.tar.zst")
	if err := os.WriteFile(archivePath, []byte("archive"), 0o600); err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	// Encryption stays off so passphraseSaltForManifest returns early: this test is
	// about the identity field, and a salt failure would abort the whole write.
	//
	// BundleAssociatedFiles is TRUE because that is what internal/config/templates
	// backup.env ships, and because the flag is reachable from the write site through
	// o.cfg. With it false, a change that dropped the identity on exactly the
	// installations running the default would be invisible here: the fixture would be
	// the one shape the bug spares.
	cfg := &config.Config{BackupPath: dir, BundleAssociatedFiles: true}
	o := &Orchestrator{logger: logging.New(types.LogLevelError, false), cfg: cfg}

	stats := InitializeBackupStats(
		"pve.home.arpa",
		nil,
		"1.0.0",
		time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC),
		cfg,
		types.CompressionZstd,
		"auto",
		3,
		1,
		dir,
		serverID,
		"bc:24:11:41:0d:18",
	)

	run := &backupRunContext{ctx: context.Background(), stats: stats}
	artifacts := &backupArtifacts{archivePath: archivePath, checksumPath: archivePath + ".sha256"}
	// osFS, not a fake: the readers this test asserts with open real files, and the
	// bundle step below tars whatever is really on disk.
	workspace := &backupWorkspace{fs: osFS{}}
	return o, run, artifacts, workspace
}

// manifestFromWrittenBundle returns the manifest payload a bundle carries, looked up
// by the entry name storage.manifestFromBundle computes for itself:
// <archive base>.metadata. The name is the contract between the two packages, so
// looking it up the same way is what makes this assertion mean "a retention pass
// would find it" rather than "some entry in the tar has it".
func manifestFromWrittenBundle(t *testing.T, bundlePath, archivePath string) []byte {
	t.Helper()

	file, err := os.Open(bundlePath) //nolint:gosec // path built by the test in its own temp dir
	if err != nil {
		t.Fatalf("open bundle %s: %v", filepath.Base(bundlePath), err)
	}
	defer func() { _ = file.Close() }()

	want := filepath.Base(archivePath) + ".metadata"
	reader := tar.NewReader(file)
	for {
		hdr, err := reader.Next()
		if errors.Is(err, io.EOF) {
			t.Fatalf("the bundle carries no %q entry. That is the only name storage.manifestFromBundle looks for, so a bundled backup would be unattributable and retention would leave it alone for ever", want)
		}
		if err != nil {
			t.Fatalf("read bundle %s: %v", filepath.Base(bundlePath), err)
		}
		if filepath.Base(hdr.Name) != want {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read %s out of the bundle: %v", want, err)
		}
		return data
	}
}

// TestArchiveManifestCarriesTheRunsServerIdentityIntoEverySidecarRetentionReads is the
// write side of discussion #292: the one place a run's server identity reaches the
// archives it produces. Everything else in the feature is a reader, so if this hop is
// dropped no archive ever records an identity and all four read paths keep working
// on nothing. The failure is silent by construction, because retention then falls
// back to the hostname rule and keeps pruning exactly as it did before.
//
// It asserts the value where a LATER RUN will look for it, on all three sinks the
// write path produces, and with the same readers those runs use:
//
//	<archive>.manifest.json  read with backup.LoadManifest. It is the SECOND suffix
//	                         the cloud path cats, and the completion sidecar the
//	                         verify step checks. No LOCAL reader ever opens it for an
//	                         identity, so it is a sink in its own right rather than
//	                         the one LocalStorage.loadMetadata uses.
//	<archive>.metadata       the ONLY file LocalStorage.loadMetadata and
//	                         manifestOwnerFromLocalArchive open, both through
//	                         backup.LoadManifest, and the FIRST suffix the cloud path
//	                         cats, through backup.OwnerFromManifestBytes. Asserted
//	                         with both readers below, because both consume it.
//	<archive>.bundle.tar     the shipped BUNDLE_ASSOCIATED_FILES=true default, where
//	                         the two sidecars above are removed after bundling and
//	                         the copy inside the tar is the ONLY manifest left
//
// The bundle leg is not an independent check of the VALUE, and saying so matters more
// than the reassurance would. createBundle adds base+".metadata" to the tar, and
// writeLegacyMetadataAlias produced that file by copying the manifest, so all three
// sinks are three views of the same bytes: a write that reached the sidecars but not
// the bundle is not a shape this field can take.
//
// What the leg checks on its own is the ENTRY NAME. createBundle must write the
// manifest under exactly the name storage.manifestFromBundle later demands, and the
// two live in different packages with no shared constant between them. With the
// shipped default the sidecars are deleted after bundling (see removeRawArtifacts),
// so the copy inside the tar is the only manifest left on disk, and a name the reader
// does not expect makes the identity unreadable on every installed host at once.
func TestArchiveManifestCarriesTheRunsServerIdentityIntoEverySidecarRetentionReads(t *testing.T) {
	o, run, artifacts, workspace := seedArchiveForManifestWrite(t, runServerID)

	const checksum = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := o.writeArchiveChecksum(workspace, artifacts, checksum); err != nil {
		t.Fatalf("writeArchiveChecksum: %v", err)
	}
	if err := o.writeArchiveManifest(run, artifacts, checksum); err != nil {
		t.Fatalf("writeArchiveManifest: %v", err)
	}
	o.writeLegacyMetadataAlias(workspace, artifacts)

	manifest, err := backup.LoadManifest(artifacts.manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if manifest.ServerID != runServerID {
		t.Errorf("the manifest beside the archive records server identity %q, want %q. This is the only moment a machine stamps its identity into its own work: without it, a host that later stops resolving the name recorded beside the archive can never recognise the archive as its own, retention stops rotating it, and the directory grows for ever (discussion #292)", manifest.ServerID, runServerID)
	}
	if manifest.Hostname != "pve.home.arpa" {
		t.Errorf("the manifest records hostname %q, want %q. The identity may only ever confirm a name, so an identity recorded beside the wrong name confirms nothing", manifest.Hostname, "pve.home.arpa")
	}

	alias, err := os.ReadFile(artifacts.archivePath + ".metadata")
	if err != nil {
		t.Fatalf("read the legacy .metadata alias: %v", err)
	}
	aliasHost, aliasID := backup.OwnerFromManifestBytes(alias)
	if aliasID != runServerID || aliasHost != "pve.home.arpa" {
		t.Errorf("the .metadata alias names writer (%q, %q), want (%q, %q). The cloud backend reads this file first, one rclone cat per archive, so an identity missing here is an identity cloud retention can never see", aliasHost, aliasID, "pve.home.arpa", runServerID)
	}
	// The same file, through the OTHER reader. backup.LoadManifest is what
	// LocalStorage.loadMetadata and manifestOwnerFromLocalArchive both call on this
	// exact path, and it is a different parser from OwnerFromManifestBytes: it tries
	// JSON first and falls back to the legacy KEY=VALUE form. They cannot diverge on
	// a JSON payload today, and this assertion is what would say so if they ever did.
	aliasManifest, err := backup.LoadManifest(artifacts.archivePath + ".metadata")
	if err != nil {
		t.Fatalf("load the .metadata alias with the reader local and secondary retention use: %v", err)
	}
	if aliasManifest.ServerID != runServerID {
		t.Errorf("the .metadata alias records server identity %q through backup.LoadManifest, want %q. That is the reader LocalStorage.loadMetadata and manifestOwnerFromLocalArchive both use, so an identity missing here is invisible to local AND secondary retention at once", aliasManifest.ServerID, runServerID)
	}

	bundlePath, err := o.createBundle(context.Background(), artifacts.archivePath)
	if err != nil {
		t.Fatalf("createBundle: %v", err)
	}
	bundledHost, bundledID := backup.OwnerFromManifestBytes(manifestFromWrittenBundle(t, bundlePath, artifacts.archivePath))
	if bundledID != runServerID || bundledHost != "pve.home.arpa" {
		t.Errorf("the manifest inside the bundle names writer (%q, %q), want (%q, %q). BUNDLE_ASSOCIATED_FILES ships as true, so the sidecars are removed after bundling and this copy is the only manifest a later run has left to read", bundledHost, bundledID, "pve.home.arpa", runServerID)
	}
}

// TestTheRunsServerIdentityReachesTheStatsTheManifestIsWrittenFrom closes the one hop
// between the wiring guard in package main and the manifest write above.
//
// cmd/proxsave hands the resolved identity to SetIdentity, and
// TestServerIdentityResolvedBeforeTheStorageBackends pins that it is resolved before
// the backends are built. From there the write side takes a different route from the
// read side: the backends read cfg.ServerID, while the manifest reads stats.ServerID,
// and initBackupRun is the only thing that joins o.serverID to it. Dropping that one
// argument compiles, leaves cfg.ServerID intact so every retention log line still
// reports an identity, and silently writes every archive from then on with none.
//
// It builds the stats through the real initBackupRun rather than calling
// InitializeBackupStats directly, because passing the identity is exactly what is
// being pinned.
func TestTheRunsServerIdentityReachesTheStatsTheManifestIsWrittenFrom(t *testing.T) {
	o := &Orchestrator{logger: logging.New(types.LogLevelError, false), cfg: &config.Config{}}
	o.SetIdentity(runServerID, "bc:24:11:41:0d:18")

	run := o.newBackupRunContext(context.Background(), nil, "pve.home.arpa")
	stats := o.initBackupRun(run)

	if stats.ServerID != runServerID {
		t.Errorf("the run's stats carry server identity %q, want %q. This is the only hop that joins the identity the orchestrator was given to the field the manifest is written from: cfg.ServerID stays correct without it, so retention keeps reporting an identity on every run while every archive it writes records none", stats.ServerID, runServerID)
	}
}

// TestArchiveManifestRecordsNoIdentityWhenTheRunHasNone is the control, and it is what
// makes the test above evidence about the RUN's identity rather than about a constant.
// A host that cannot determine its own identity must write the manifest it has always
// written, with no server_id key at all: every archive in the installed base is that
// shape, and a key appearing where there is nothing to record would be a manifest
// format change on hosts that gained nothing.
func TestArchiveManifestRecordsNoIdentityWhenTheRunHasNone(t *testing.T) {
	o, run, artifacts, workspace := seedArchiveForManifestWrite(t, "")

	const checksum = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := o.writeArchiveChecksum(workspace, artifacts, checksum); err != nil {
		t.Fatalf("writeArchiveChecksum: %v", err)
	}
	if err := o.writeArchiveManifest(run, artifacts, checksum); err != nil {
		t.Fatalf("writeArchiveManifest: %v", err)
	}

	data, err := os.ReadFile(artifacts.manifestPath)
	if err != nil {
		t.Fatalf("read the manifest: %v", err)
	}
	if strings.Contains(string(data), "server_id") {
		t.Errorf("a run with no server identity wrote the key anyway:\n%s\nThe field is omitempty precisely so a host that has no identity keeps producing the manifest it always produced", data)
	}
	if _, id := backup.OwnerFromManifestBytes(data); id != "" {
		t.Errorf("the manifest reads back server identity %q for a run that had none. An absent identity must stay absent: ownership treats two empty identities as \"cannot compare\", never as a match, and an invented one would be a claim on another machine's archives", id)
	}
}
