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

// The ladder refuses to run host commands under a prefix (TestDetectWithPrefixSkipsCommandProbes).
// The snapshot has to refuse them too: it is read to explain a detection the ladder got wrong,
// and an appliance path printed there is evidence about the wrong machine.
func TestMarkerSnapshotSkipsCommandProbesUnderPrefix(t *testing.T) {
	root := t.TempDir()
	setValue(t, &lookPathFunc, func(string) (string, error) {
		t.Fatal("lookPathFunc must not be called under a root prefix")
		return "", nil
	})

	joined := strings.Join(MarkerSnapshot(DetectOptions{RootPrefix: root}), "\n")
	for _, want := range []string{
		"command -v pveversion: skipped (answers for the appliance, not the mounted host)",
		"command -v proxmox-backup-manager: skipped (answers for the appliance, not the mounted host)",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, joined)
		}
	}
}

// Without a prefix the command probes are the whole point of the section, so they still run.
func TestMarkerSnapshotRunsCommandProbesWithoutPrefix(t *testing.T) {
	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		return "/usr/bin/" + cmd, nil
	})

	joined := strings.Join(MarkerSnapshot(DetectOptions{}), "\n")
	if !strings.Contains(joined, "command -v pveversion: /usr/bin/pveversion") {
		t.Fatalf("snapshot did not run the command probe without a prefix:\n%s", joined)
	}
}

// A binary the MOUNTED HOST has and can run must read executable: YES. Stating the bare
// literal answered for the appliance, which does not have it, so the line said NO about a
// file the host could run.
func TestMarkerSnapshotReportsHostBinaryAsExecutable(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "usr/sbin/pveversion")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 8.2.2\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(MarkerSnapshot(DetectOptions{RootPrefix: root}), "\n")
	for _, want := range []string{
		bin + " exists: YES",
		bin + " executable: YES",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, joined)
		}
	}
}

// The other direction: the APPLIANCE has the binary and the mounted host does not. The line
// carries the host path, so it has to answer for the host.
func TestMarkerSnapshotDoesNotReportApplianceBinaryAsHostBinary(t *testing.T) {
	root := t.TempDir()
	realStat := statFunc
	setValue(t, &statFunc, func(path string) (os.FileInfo, error) {
		if path == "/usr/bin/pveversion" {
			// an appliance that really carries the binary, executable bit and all
			return realStat("/bin/sh")
		}
		return realStat(path)
	})

	joined := strings.Join(MarkerSnapshot(DetectOptions{RootPrefix: root}), "\n")
	host := filepath.Join(root, "usr/bin/pveversion")
	for _, want := range []string{
		host + " exists: NO",
		host + " executable: NO",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, joined)
		}
	}
}

// The marker table reports the command binaries with an executable bit, the offline section
// only with existence, so the two lists are separate. They must not disagree about WHERE a
// binary lives: PBS installs proxmox-backup-manager in /usr/sbin, and the table used to name
// /usr/bin alone, so a real PBS host read "exists: NO" for a binary it has.
func TestCommandBinaryCandidatesCoverTheOfflineOnes(t *testing.T) {
	covered := make(map[string]struct{}, len(commandBinaryCandidates))
	for _, bin := range commandBinaryCandidates {
		covered[bin] = struct{}{}
	}

	probed := map[string]struct{}{"pveversion": {}, "proxmox-backup-manager": {}}
	for _, bin := range append(append([]string{}, pveBinaryCandidates...), pbsBinaryCandidates...) {
		if _, ok := probed[filepath.Base(bin)]; !ok {
			continue
		}
		if _, ok := covered[bin]; !ok {
			t.Fatalf("%s is an offline candidate for a command-probed binary but the marker table never stats it", bin)
		}
	}
}

// PBS 4.2.5 installs proxmox-backup-manager in /usr/sbin. Verified on a live PBS host: the
// table named /usr/bin alone, so it reported "exists: NO" while the offline section, which
// probes both, found the binary two sections below.
func TestMarkerSnapshotStatsThePBSManagerWherePBSInstallsIt(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "usr/sbin/proxmox-backup-manager")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 4.2.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(MarkerSnapshot(DetectOptions{RootPrefix: root}), "\n")
	for _, want := range []string{
		bin + " exists: YES",
		bin + " executable: YES",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, joined)
		}
	}
}
