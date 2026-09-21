package environment

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// pveOnlyCommandSeams makes pveversion the only command that answers, which is the
// live shape of the issue #315 host: PVE is installed and PBS is not.
func pveOnlyCommandSeams(t *testing.T) {
	t.Helper()
	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		if cmd == "pveversion" {
			return "/usr/bin/pveversion", nil
		}
		return "", errors.New("executable file not found in $PATH")
	})
	setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
		return "pve-manager/9.2.18/abcdef0123456789 (running kernel: 6.17.4-2-pve)", nil
	})
}

// TestLeftoverPBSDirectoryDoesNotMakeAPVEHostDual is issue #315. Removing
// proxmox-backup-server takes away everything the package ships (its binary, its
// share directory, its dpkg stanza) but leaves /etc/proxmox-backup and
// /var/lib/proxmox-backup behind: they are created at runtime, belong to no package,
// and the server postrm does not remove them even on purge (measured on PBS 3.4.9
// and 4.2.0). A directory that outlives the product it belonged to cannot be the
// evidence that the product is installed.
func TestLeftoverPBSDirectoryDoesNotMakeAPVEHostDual(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)
	pveOnlyCommandSeams(t)

	leftovers := []string{
		filepath.Join(tmp, "etc/proxmox-backup"),
		filepath.Join(tmp, "var/lib/proxmox-backup"),
	}
	for _, dir := range leftovers {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	setValue(t, &pbsDirCandidates, leftovers)

	info, err := detectEnvironmentInfo()
	if err != nil {
		t.Fatalf("detectEnvironmentInfo: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE: no PBS package is installed on this host", info.Type)
	}
	if info.PBSVersion != "" {
		t.Fatalf("PBSVersion = %q, want empty", info.PBSVersion)
	}
	if info.PBSSource != "" {
		t.Fatalf("PBSSource = %q, want no deciding marker", info.PBSSource)
	}
}

// TestSecondLeftoverDirectoryDoesNotDecideEither guards the ladder walking its whole
// candidate list: deleting /etc/proxmox-backup while /var/lib/proxmox-backup stays
// used to leave the verdict unchanged, so an operator who followed the obvious
// remedy saw no difference.
func TestSecondLeftoverDirectoryDoesNotDecideEither(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)
	pveOnlyCommandSeams(t)

	varLib := filepath.Join(tmp, "var/lib/proxmox-backup")
	if err := os.MkdirAll(varLib, 0o755); err != nil {
		t.Fatal(err)
	}
	setValue(t, &pbsDirCandidates, []string{filepath.Join(tmp, "etc/proxmox-backup"), varLib})

	info, err := detectEnvironmentInfo()
	if err != nil {
		t.Fatalf("detectEnvironmentInfo: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
}

// TestResidueIsReportedEvenThoughItDoesNotDecide: the leftovers are the whole reason
// the operator expected a different verdict, so detection must still name them.
// Dropping them silently would answer the question with nothing to check.
func TestResidueIsReportedEvenThoughItDoesNotDecide(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)
	pveOnlyCommandSeams(t)

	leftover := filepath.Join(tmp, "etc/proxmox-backup")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatal(err)
	}
	setValue(t, &pbsDirCandidates, []string{leftover})

	info, err := detectEnvironmentInfo()
	if err != nil {
		t.Fatalf("detectEnvironmentInfo: %v", err)
	}
	if !strings.Contains(info.PBSResidual, leftover) {
		t.Fatalf("PBSResidual = %q, want it to name %s", info.PBSResidual, leftover)
	}

	step, ok := stepFor(info.Steps, productPBS, "directory")
	if !ok {
		t.Fatal("no PBS directory step recorded: the rung must still be walked")
	}
	if step.Hit {
		t.Fatalf("PBS directory step = %v, want it recorded as residue, not a hit", step)
	}
	if !step.Residual {
		t.Fatalf("PBS directory step = %v, want Residual set", step)
	}
	if !strings.Contains(step.String(), "residue") {
		t.Fatalf("step line = %q, want it to read as residue", step.String())
	}
}

// TestLeftoverPBSDirectoryUnderPrefixDoesNotMakeAHostDual is the same host reached
// through SYSTEM_ROOT_PREFIX (issue #255), where command probes are skipped and only
// the offline markers answer. PVE is proved by its dpkg stanza.
func TestLeftoverPBSDirectoryUnderPrefixDoesNotMakeAHostDual(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "var/lib/dpkg/status"), dpkgStanza("pve-manager", "9.2.18"))
	for _, dir := range []string{"etc/proxmox-backup", "var/lib/proxmox-backup"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
	if info.PVEVersion != "9.2.18" {
		t.Fatalf("PVEVersion = %q, want 9.2.18", info.PVEVersion)
	}
}

// TestInstalledPBSUnderPrefixIsStillDetected is the non-regression for issue #255:
// the markers a real PBS carries must still classify it without running a command.
// Measured on PBS 3.4.9 and 4.2.0: the manager binary is in /usr/sbin, the share
// directory belongs to proxmox-backup-server, and /etc/proxmox-backup/version does
// not exist on either release.
func TestInstalledPBSUnderPrefixIsStillDetected(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "var/lib/dpkg/status"), dpkgStanza("proxmox-backup-server", "4.2.0-1"))
	writeFile(t, filepath.Join(root, "usr/sbin/proxmox-backup-manager"), "#!/bin/sh\n")
	if err := os.MkdirAll(filepath.Join(root, "usr/share/proxmox-backup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc/proxmox-backup"), 0o700); err != nil {
		t.Fatal(err)
	}

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxBS {
		t.Fatalf("Type = %v, want ProxmoxBS", info.Type)
	}
	if info.PBSVersion != "4.2.0-1" {
		t.Fatalf("PBSVersion = %q, want 4.2.0-1", info.PBSVersion)
	}
}

// TestCoinstalledHostIsStillDual is the non-regression for issue #197, the request
// that produced dual support: a host where both products answer must keep reporting
// both. pve-test is exactly this shape, PVE 9.1.9 and PBS 4.2.0 on one node.
func TestCoinstalledHostIsStillDual(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)
	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		switch cmd {
		case "pveversion":
			return "/usr/bin/pveversion", nil
		case "proxmox-backup-manager":
			return "/usr/sbin/proxmox-backup-manager", nil
		}
		return "", errors.New("not found")
	})
	setValue(t, &runCommandFunc, func(cmd string, _ ...string) (string, error) {
		if strings.HasSuffix(cmd, "pveversion") {
			return "pve-manager/9.1.9/ee7bad0a3d1546c9 (running kernel: 7.0.2-2-pve)", nil
		}
		return "proxmox-backup-server 4.2.5-1 running version: 4.2.0", nil
	})

	info, err := detectEnvironmentInfo()
	if err != nil {
		t.Fatalf("detectEnvironmentInfo: %v", err)
	}
	if info.Type != types.ProxmoxDual {
		t.Fatalf("Type = %v, want ProxmoxDual", info.Type)
	}
	if info.Version != "pve=9.1.9,pbs=4.2.0" {
		t.Fatalf("Version = %q, want the two versions combined", info.Version)
	}
}

// TestEmptyPBSVersionFileIsResidue: an existing but empty version file used to count
// as a PBS hit carrying the version "unknown". A file with nothing in it proves
// nothing about what is installed.
func TestEmptyPBSVersionFileIsResidue(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)
	pveOnlyCommandSeams(t)

	versionFile := filepath.Join(tmp, "etc/proxmox-backup/version")
	writeFile(t, versionFile, "")
	setValue(t, &pbsVersionFile, versionFile)

	info, err := detectEnvironmentInfo()
	if err != nil {
		t.Fatalf("detectEnvironmentInfo: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
}

// dpkgStanza renders the two fields dpkgPackageInstalled reads, in the layout the
// real /var/lib/dpkg/status uses (blank-line separated stanzas).
func dpkgStanza(pkg, version string) string {
	return "Package: " + pkg + "\nStatus: install ok installed\nVersion: " + version + "\n\n"
}

// TestMarkerTableReportsBothDpkgProbesEitherWay: the table is an inventory, and every
// other marker in it reports YES or NO. These two used to print only when the package
// was installed, so on the host in issue #315 the line that would have said
// proxmox-backup-server is absent was simply missing, and an absent line reads as a
// check that never ran rather than as a negative answer.
func TestMarkerTableReportsBothDpkgProbesEitherWay(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "var/lib/dpkg/status"), dpkgStanza("pve-manager", "9.2.18"))

	joined := strings.Join(MarkerSnapshot(DetectOptions{RootPrefix: root}), "\n")
	for _, want := range []string{
		"dpkg pve-manager: installed (9.2.18)",
		"dpkg proxmox-backup-server: not installed",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, joined)
		}
	}
}

// TestATimedOutCommandStillGetsItsVersionFromDpkg is measured, not hypothetical:
// pveversion takes 4.4 to 5.2 seconds on the lab host and commandTimeout is 5, so it
// times out on nothing more unusual than a busy node. The rung used to answer
// "installed, version unknown" and stop the ladder one step above dpkg, which holds
// the real version, leaving the run to report a host with no version at all.
func TestATimedOutCommandStillGetsItsVersionFromDpkg(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)

	dpkgStatus := filepath.Join(tmp, "dpkg-status")
	writeFile(t, dpkgStatus, dpkgStanza("pve-manager", "9.2.18"))
	setValue(t, &dpkgStatusFile, dpkgStatus)

	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		if cmd == "pveversion" {
			return "/usr/bin/pveversion", nil
		}
		return "", errors.New("not found")
	})
	setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
		return "", errors.New("command pveversion timed out")
	})

	info, err := detectEnvironmentInfo()
	if err != nil {
		t.Fatalf("detectEnvironmentInfo: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
	if info.PVEVersion != "9.2.18" {
		t.Fatalf("PVEVersion = %q, want 9.2.18 recovered from dpkg", info.PVEVersion)
	}
}

// TestAVersionlessCommandStillProvesTheInstall: if every version-bearing marker is
// gone too, the host is still PVE. The binary on PATH is the proof; only the version
// is missing.
func TestAVersionlessCommandStillProvesTheInstall(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)

	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		if cmd == "pveversion" {
			return "/usr/bin/pveversion", nil
		}
		return "", errors.New("not found")
	})
	setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
		return "no version in this output", nil
	})

	info, err := detectEnvironmentInfo()
	if err != nil {
		t.Fatalf("detectEnvironmentInfo: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
}

// TestProvenanceNamesTheRungThatDecided: the versionless-command rung is a hit that
// keeps walking, and decidedBy returning the first hit named it as the decider while
// dpkg one rung down supplied the version. An operator reading "PVE decided by command
// (pveversion)" above a step list showing that probe produced nothing was pointed at
// the wrong marker.
func TestProvenanceNamesTheRungThatDecided(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)

	dpkgStatus := filepath.Join(tmp, "dpkg-status")
	writeFile(t, dpkgStatus, dpkgStanza("pve-manager", "9.2.18")+dpkgStanza("proxmox-backup-server", "4.2.0-1"))
	setValue(t, &dpkgStatusFile, dpkgStatus)

	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		switch cmd {
		case "pveversion":
			return "/usr/bin/pveversion", nil
		case "proxmox-backup-manager":
			return "/usr/sbin/proxmox-backup-manager", nil
		}
		return "", errors.New("not found")
	})
	setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
		return "", errors.New("command timed out")
	})

	info, err := detectEnvironmentInfo()
	if err != nil {
		t.Fatalf("detectEnvironmentInfo: %v", err)
	}
	if info.Type != types.ProxmoxDual {
		t.Fatalf("Type = %v, want ProxmoxDual", info.Type)
	}
	if !strings.HasPrefix(info.PVESource, "dpkg pve-manager") {
		t.Fatalf("PVESource = %q, want the dpkg rung that supplied the version", info.PVESource)
	}
	if !strings.HasPrefix(info.PBSSource, "dpkg proxmox-backup-server") {
		t.Fatalf("PBSSource = %q, want the dpkg rung that supplied the version", info.PBSSource)
	}
}

// TestProvenanceFallsBackToTheCommandWhenNothingElseAnswers: with no later marker, the
// rung that kept walking is the whole provenance there is, and an empty source line
// would be worse than naming it.
func TestProvenanceFallsBackToTheCommandWhenNothingElseAnswers(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)
	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		if cmd == "pveversion" {
			return "/usr/bin/pveversion", nil
		}
		return "", errors.New("not found")
	})
	setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
		return "no version here", nil
	})

	info, _ := detectEnvironmentInfo()
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
	if !strings.HasPrefix(info.PVESource, "command (") {
		t.Fatalf("PVESource = %q, want the command rung named as the only provenance", info.PVESource)
	}
}

// TestThePBSHalfOfTheVersionlessCommandFix: the PVE half had a test and the PBS half
// had none, so the symmetry was asserted nowhere.
func TestThePBSHalfOfTheVersionlessCommandFix(t *testing.T) {
	tmp := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	nullFilesystemMarkerSeams(t, tmp)

	dpkgStatus := filepath.Join(tmp, "dpkg-status")
	writeFile(t, dpkgStatus, dpkgStanza("proxmox-backup-server", "4.2.0-1"))
	setValue(t, &dpkgStatusFile, dpkgStatus)

	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		if cmd == "proxmox-backup-manager" {
			return "/usr/sbin/proxmox-backup-manager", nil
		}
		return "", errors.New("not found")
	})
	setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
		return "", errors.New("command proxmox-backup-manager timed out")
	})

	info, err := detectEnvironmentInfo()
	if err != nil {
		t.Fatalf("detectEnvironmentInfo: %v", err)
	}
	if info.Type != types.ProxmoxBS {
		t.Fatalf("Type = %v, want ProxmoxBS", info.Type)
	}
	if info.PBSVersion != "4.2.0-1" {
		t.Fatalf("PBSVersion = %q, want 4.2.0-1 recovered from dpkg", info.PBSVersion)
	}
}

// An unreadable dpkg status file is not evidence that a package is absent. The table
// used to print "not installed" for it, which reads as a checked fact rather than as
// a check that never ran, and on a SYSTEM_ROOT_PREFIX mount without /var/lib/dpkg/status
// that is the whole explanation an operator gets for the verdict.
func TestMarkerTableDoesNotCallAnUnreadableDpkgStatusProofOfAbsence(t *testing.T) {
	root := t.TempDir() // no var/lib/dpkg/status under it at all

	joined := strings.Join(MarkerSnapshot(DetectOptions{RootPrefix: root}), "\n")
	for _, pkg := range []string{"pve-manager", "proxmox-backup-server"} {
		want := "dpkg " + pkg + ": not proven installed ("
		if !strings.Contains(joined, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, joined)
		}
		if strings.Contains(joined, "dpkg "+pkg+": not installed") {
			t.Fatalf("snapshot still claims %s is not installed without having read dpkg:\n%s", pkg, joined)
		}
	}
}

// The marker table classifies both packages against ONE read of the dpkg status file.
// Re-reading per package reopens the very hole the "not proven installed" line closes:
// a status file that stops being readable after the first read would come back as a
// package that is simply not installed, which is a claim the run cannot support. The
// stub here serves the file once and refuses every later read of it.
func TestMarkerTableClassifiesBothPackagesFromOneDpkgRead(t *testing.T) {
	root := t.TempDir()
	statusPath := filepath.Join(root, "var/lib/dpkg/status")
	writeFile(t, statusPath, dpkgStanza("pve-manager", "9.2.18"))

	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	served := 0
	setValue(t, &readFileFunc, func(path string) ([]byte, error) {
		if path != statusPath {
			return os.ReadFile(path)
		}
		served++
		if served > 1 {
			return nil, errors.New("dpkg status: refused on purpose after the first read")
		}
		return status, nil
	})

	joined := strings.Join(MarkerSnapshot(DetectOptions{RootPrefix: root}), "\n")
	for _, want := range []string{
		"dpkg pve-manager: installed (9.2.18)",
		"dpkg proxmox-backup-server: not installed",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("snapshot missing %q after %d dpkg read(s):\n%s", want, served, joined)
		}
	}
	if served != 1 {
		t.Fatalf("dpkg status read %d times, want exactly 1: the verdict must come from the read it checked", served)
	}
}
