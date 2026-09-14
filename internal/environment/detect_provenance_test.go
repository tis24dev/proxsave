package environment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// stepFor returns the recorded step for a product and marker, and whether it exists.
func stepFor(steps []DetectionStep, product, marker string) (DetectionStep, bool) {
	for _, step := range steps {
		if step.Product == product && step.Marker == marker {
			return step, true
		}
	}
	return DetectionStep{}, false
}

// TestDetectionProvenanceNamesDecidingMarker is the issue #315 case: a host whose
// only PBS marker is a leftover directory is reported as dual, and the trace has to
// say so - naming the directory that decided it and showing that every version-bearing
// PBS marker missed.
func TestDetectionProvenanceNamesDecidingMarker(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/pve-manager/version"), "8.2.2\n")
	if err := os.MkdirAll(filepath.Join(root, "var/lib/proxmox-backup"), 0o755); err != nil {
		t.Fatal(err)
	}

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxDual {
		t.Fatalf("Type = %v, want ProxmoxDual", info.Type)
	}

	if !strings.HasPrefix(info.PVESource, "version-file (") {
		t.Fatalf("PVESource = %q, want the version-file marker", info.PVESource)
	}
	wantDir := filepath.Join(root, "var/lib/proxmox-backup")
	if !strings.Contains(info.PBSSource, wantDir) {
		t.Fatalf("PBSSource = %q, want the deciding directory %s", info.PBSSource, wantDir)
	}

	versionStep, ok := stepFor(info.Steps, productPBS, "version-file")
	if !ok {
		t.Fatal("no PBS version-file step recorded")
	}
	if versionStep.Hit {
		t.Fatalf("PBS version-file step = %v, want a miss", versionStep)
	}
	dpkgStep, ok := stepFor(info.Steps, productPBS, "dpkg proxmox-backup-server")
	if !ok {
		t.Fatal("no PBS dpkg step recorded")
	}
	if dpkgStep.Hit {
		t.Fatalf("PBS dpkg step = %v, want a miss", dpkgStep)
	}
}

// TestDetectionProvenanceSkipsCommandUnderPrefix proves the command probe is recorded
// as skipped, not as a miss: under a prefix it is never run, and a log that called it
// a miss would read as "the host has no PBS command".
func TestDetectionProvenanceSkipsCommandUnderPrefix(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/pve-manager/version"), "8.2.2\n")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	step, ok := stepFor(info.Steps, productPVE, "command")
	if !ok {
		t.Fatal("no PVE command step recorded")
	}
	if !step.Skipped || step.Hit {
		t.Fatalf("PVE command step = %v, want skipped", step)
	}
	if !strings.Contains(step.String(), "skipped") {
		t.Fatalf("step line = %q, want it to read as skipped", step.String())
	}
}

// TestMarkerSnapshotReportsUncheckedMarkers covers what the ladder cannot: the ladder
// returns at the first hit, so only the snapshot can show a marker that sits past the
// winner. Here PBS wins on its version file while a second PBS marker, the share
// directory, also exists.
func TestMarkerSnapshotReportsUncheckedMarkers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/proxmox-backup/version"), "3.2.1\n")
	if err := os.MkdirAll(filepath.Join(root, "usr/share/proxmox-backup"), 0o755); err != nil {
		t.Fatal(err)
	}

	lines := MarkerSnapshot(DetectOptions{RootPrefix: root})
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"=== Command availability check ===",
		"=== Offline host markers check ===",
		filepath.Join(root, "usr/share/proxmox-backup") + " exists: YES",
		"PBS apt-source match: none",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, joined)
		}
	}
}

// TestMarkerSnapshotRestoresPrefix guards the package-level prefix seam: a snapshot
// taken under a prefix must not leave detection anchored there for the next caller.
func TestMarkerSnapshotRestoresPrefix(t *testing.T) {
	_ = MarkerSnapshot(DetectOptions{RootPrefix: t.TempDir()})
	if rootPrefix != "" {
		t.Fatalf("rootPrefix = %q after MarkerSnapshot, want it restored", rootPrefix)
	}
}
