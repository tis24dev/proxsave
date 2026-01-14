package environment

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
)

func setValue[T any](t *testing.T, target *T, value T) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}

func TestExtendPath_EmptyPATH(t *testing.T) {
	separator := string(os.PathListSeparator)
	setValue(t, &additionalPaths, []string{"/custom/bin", "/custom/sbin"})
	t.Setenv("PATH", "")

	extendPath()

	want := strings.Join(additionalPaths, separator)
	if got := os.Getenv("PATH"); got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestDetectPVEViaCommand_Branches(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })

		version, ok := detectPVEViaCommand()
		if ok || version != "" {
			t.Fatalf("detectPVEViaCommand() = (%q, %v), want (%q, %v)", version, ok, "", false)
		}
	})

	t.Run("command error", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "/fake/pveversion", nil })
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) { return "", errors.New("boom") })

		version, ok := detectPVEViaCommand()
		if !ok || version != "unknown" {
			t.Fatalf("detectPVEViaCommand() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("missing version in output", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "/fake/pveversion", nil })
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "no version here", nil
		})

		version, ok := detectPVEViaCommand()
		if !ok || version != "unknown" {
			t.Fatalf("detectPVEViaCommand() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("success", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "/fake/pveversion", nil })
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "pve-manager/7.4-3/d4a3b4a1 (running kernel: 5.15.35-1-pve)", nil
		})

		version, ok := detectPVEViaCommand()
		if !ok || version != "7.4-3" {
			t.Fatalf("detectPVEViaCommand() = (%q, %v), want (%q, %v)", version, ok, "7.4-3", true)
		}
	})
}

func TestDetectPBSViaCommand_Branches(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })

		version, ok := detectPBSViaCommand()
		if ok || version != "" {
			t.Fatalf("detectPBSViaCommand() = (%q, %v), want (%q, %v)", version, ok, "", false)
		}
	})

	t.Run("command error", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "/fake/proxmox-backup-manager", nil })
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) { return "", errors.New("boom") })

		version, ok := detectPBSViaCommand()
		if !ok || version != "unknown" {
			t.Fatalf("detectPBSViaCommand() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("missing version in output", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "/fake/proxmox-backup-manager", nil })
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "proxmox-backup-manager 2.4.1\nno version here", nil
		})

		version, ok := detectPBSViaCommand()
		if !ok || version != "unknown" {
			t.Fatalf("detectPBSViaCommand() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("success", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "/fake/proxmox-backup-manager", nil })
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "proxmox-backup-manager 2.4.1\nversion: 2.4.1", nil
		})

		version, ok := detectPBSViaCommand()
		if !ok || version != "2.4.1" {
			t.Fatalf("detectPBSViaCommand() = (%q, %v), want (%q, %v)", version, ok, "2.4.1", true)
		}
	})
}

func TestDetectPVEViaVersionFiles_Branches(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("pveVersionFile present", func(t *testing.T) {
		versionFile := filepath.Join(tmpDir, "pve-version")
		if err := os.WriteFile(versionFile, []byte("7.4-1\n"), 0644); err != nil {
			t.Fatal(err)
		}

		setValue(t, &pveVersionFile, versionFile)
		setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-legacy"))

		version, ok := detectPVEViaVersionFiles()
		if !ok || version != "7.4-1" {
			t.Fatalf("detectPVEViaVersionFiles() = (%q, %v), want (%q, %v)", version, ok, "7.4-1", true)
		}
	})

	t.Run("legacy present with match", func(t *testing.T) {
		versionFile := filepath.Join(tmpDir, "pve-version-empty")
		if err := os.WriteFile(versionFile, []byte("\n"), 0644); err != nil {
			t.Fatal(err)
		}
		legacyFile := filepath.Join(tmpDir, "pve-legacy")
		if err := os.WriteFile(legacyFile, []byte("pve-manager/7.4-3/d4a3b4a1"), 0644); err != nil {
			t.Fatal(err)
		}

		setValue(t, &pveVersionFile, versionFile)
		setValue(t, &pveLegacyFile, legacyFile)

		version, ok := detectPVEViaVersionFiles()
		if !ok || version != "7.4-3" {
			t.Fatalf("detectPVEViaVersionFiles() = (%q, %v), want (%q, %v)", version, ok, "7.4-3", true)
		}
	})

	t.Run("legacy present without match", func(t *testing.T) {
		legacyFile := filepath.Join(tmpDir, "pve-legacy-nomatch")
		if err := os.WriteFile(legacyFile, []byte("no version here"), 0644); err != nil {
			t.Fatal(err)
		}

		setValue(t, &pveVersionFile, filepath.Join(tmpDir, "missing-version"))
		setValue(t, &pveLegacyFile, legacyFile)

		version, ok := detectPVEViaVersionFiles()
		if !ok || version != "unknown" {
			t.Fatalf("detectPVEViaVersionFiles() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("no files", func(t *testing.T) {
		setValue(t, &pveVersionFile, filepath.Join(tmpDir, "missing-version-2"))
		setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-legacy-2"))

		version, ok := detectPVEViaVersionFiles()
		if ok || version != "" {
			t.Fatalf("detectPVEViaVersionFiles() = (%q, %v), want (%q, %v)", version, ok, "", false)
		}
	})
}

func TestDetectPBSViaVersionFile_Branches(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("present", func(t *testing.T) {
		versionFile := filepath.Join(tmpDir, "pbs-version")
		if err := os.WriteFile(versionFile, []byte("2.4-1\n"), 0644); err != nil {
			t.Fatal(err)
		}

		setValue(t, &pbsVersionFile, versionFile)

		version, ok := detectPBSViaVersionFile()
		if !ok || version != "2.4-1" {
			t.Fatalf("detectPBSViaVersionFile() = (%q, %v), want (%q, %v)", version, ok, "2.4-1", true)
		}
	})

	t.Run("present but empty", func(t *testing.T) {
		versionFile := filepath.Join(tmpDir, "pbs-version-empty")
		if err := os.WriteFile(versionFile, []byte("\n"), 0644); err != nil {
			t.Fatal(err)
		}

		setValue(t, &pbsVersionFile, versionFile)

		version, ok := detectPBSViaVersionFile()
		if !ok || version != "unknown" {
			t.Fatalf("detectPBSViaVersionFile() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("missing", func(t *testing.T) {
		setValue(t, &pbsVersionFile, filepath.Join(tmpDir, "missing-pbs-version"))

		version, ok := detectPBSViaVersionFile()
		if ok || version != "" {
			t.Fatalf("detectPBSViaVersionFile() = (%q, %v), want (%q, %v)", version, ok, "", false)
		}
	})
}

func TestDetectViaSources_Branches(t *testing.T) {
	tmpDir := t.TempDir()

	pveSource := filepath.Join(tmpDir, "pve.list")
	if err := os.WriteFile(pveSource, []byte("deb http://example.invalid pve pve-no-subscription"), 0644); err != nil {
		t.Fatal(err)
	}
	pbsSource := filepath.Join(tmpDir, "pbs.list")
	if err := os.WriteFile(pbsSource, []byte("deb http://example.invalid proxmox-backup pbs"), 0644); err != nil {
		t.Fatal(err)
	}

	setValue(t, &pveSourceFiles, []string{pveSource})
	setValue(t, &pbsSourceFiles, []string{pbsSource})

	if !detectPVEViaSources() {
		t.Fatal("detectPVEViaSources() should be true")
	}
	if !detectPBSViaSources() {
		t.Fatal("detectPBSViaSources() should be true")
	}

	emptySource := filepath.Join(tmpDir, "empty.list")
	if err := os.WriteFile(emptySource, []byte("deb http://example.invalid stable main"), 0644); err != nil {
		t.Fatal(err)
	}
	setValue(t, &pveSourceFiles, []string{emptySource})
	setValue(t, &pbsSourceFiles, []string{emptySource})

	if detectPVEViaSources() {
		t.Fatal("detectPVEViaSources() should be false")
	}
	if detectPBSViaSources() {
		t.Fatal("detectPBSViaSources() should be false")
	}
}

func TestDetectPVE_FallbackOrder(t *testing.T) {
	tmpDir := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	setValue(t, &pveSourceFiles, []string{})
	setValue(t, &pveDirCandidates, []string{})

	t.Run("via command", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "/fake/pveversion", nil })
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "pve-manager/7.4-3/d4a3b4a1", nil
		})

		version, ok := detectPVE()
		if !ok || version != "7.4-3" {
			t.Fatalf("detectPVE() = (%q, %v), want (%q, %v)", version, ok, "7.4-3", true)
		}
	})

	t.Run("via version files", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })

		versionFile := filepath.Join(tmpDir, "pve-version")
		if err := os.WriteFile(versionFile, []byte("7.4-1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		setValue(t, &pveVersionFile, versionFile)
		setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-legacy"))

		version, ok := detectPVE()
		if !ok || version != "7.4-1" {
			t.Fatalf("detectPVE() = (%q, %v), want (%q, %v)", version, ok, "7.4-1", true)
		}
	})

	t.Run("via sources", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })
		setValue(t, &pveVersionFile, filepath.Join(tmpDir, "missing-version"))
		setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-legacy"))

		sourceFile := filepath.Join(tmpDir, "pve.list")
		if err := os.WriteFile(sourceFile, []byte("pve-enterprise repository"), 0644); err != nil {
			t.Fatal(err)
		}
		setValue(t, &pveSourceFiles, []string{sourceFile})

		version, ok := detectPVE()
		if !ok || version != "unknown" {
			t.Fatalf("detectPVE() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("via directories", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })
		setValue(t, &pveVersionFile, filepath.Join(tmpDir, "missing-version-2"))
		setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-legacy-2"))
		setValue(t, &pveSourceFiles, []string{})

		dirCandidate := filepath.Join(tmpDir, "etc-pve")
		if err := os.MkdirAll(dirCandidate, 0755); err != nil {
			t.Fatal(err)
		}
		setValue(t, &pveDirCandidates, []string{dirCandidate})

		version, ok := detectPVE()
		if !ok || version != "unknown" {
			t.Fatalf("detectPVE() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("none", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })
		setValue(t, &pveVersionFile, filepath.Join(tmpDir, "missing-version-3"))
		setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-legacy-3"))
		setValue(t, &pveSourceFiles, []string{})
		setValue(t, &pveDirCandidates, []string{})

		version, ok := detectPVE()
		if ok || version != "" {
			t.Fatalf("detectPVE() = (%q, %v), want (%q, %v)", version, ok, "", false)
		}
	})
}

func TestDetectPBS_FallbackOrder(t *testing.T) {
	tmpDir := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	setValue(t, &pbsSourceFiles, []string{})
	setValue(t, &pbsDirCandidates, []string{})

	t.Run("via command", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "/fake/proxmox-backup-manager", nil })
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "version: 2.4.1", nil
		})

		version, ok := detectPBS()
		if !ok || version != "2.4.1" {
			t.Fatalf("detectPBS() = (%q, %v), want (%q, %v)", version, ok, "2.4.1", true)
		}
	})

	t.Run("via version file", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })

		versionFile := filepath.Join(tmpDir, "pbs-version")
		if err := os.WriteFile(versionFile, []byte("2.4-1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		setValue(t, &pbsVersionFile, versionFile)

		version, ok := detectPBS()
		if !ok || version != "2.4-1" {
			t.Fatalf("detectPBS() = (%q, %v), want (%q, %v)", version, ok, "2.4-1", true)
		}
	})

	t.Run("via sources", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })
		setValue(t, &pbsVersionFile, filepath.Join(tmpDir, "missing-pbs-version"))

		sourceFile := filepath.Join(tmpDir, "pbs.list")
		if err := os.WriteFile(sourceFile, []byte("proxmox-backup repository"), 0644); err != nil {
			t.Fatal(err)
		}
		setValue(t, &pbsSourceFiles, []string{sourceFile})

		version, ok := detectPBS()
		if !ok || version != "unknown" {
			t.Fatalf("detectPBS() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("via directories", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })
		setValue(t, &pbsVersionFile, filepath.Join(tmpDir, "missing-pbs-version-2"))
		setValue(t, &pbsSourceFiles, []string{})

		dirCandidate := filepath.Join(tmpDir, "etc-proxmox-backup")
		if err := os.MkdirAll(dirCandidate, 0755); err != nil {
			t.Fatal(err)
		}
		setValue(t, &pbsDirCandidates, []string{dirCandidate})

		version, ok := detectPBS()
		if !ok || version != "unknown" {
			t.Fatalf("detectPBS() = (%q, %v), want (%q, %v)", version, ok, "unknown", true)
		}
	})

	t.Run("none", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })
		setValue(t, &pbsVersionFile, filepath.Join(tmpDir, "missing-pbs-version-3"))
		setValue(t, &pbsSourceFiles, []string{})
		setValue(t, &pbsDirCandidates, []string{})

		version, ok := detectPBS()
		if ok || version != "" {
			t.Fatalf("detectPBS() = (%q, %v), want (%q, %v)", version, ok, "", false)
		}
	})
}

func TestDetectProxmox_Branches(t *testing.T) {
	tmpDir := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	setValue(t, &debugBaseDir, tmpDir)
	setValue(t, &pveSourceFiles, []string{})
	setValue(t, &pbsSourceFiles, []string{})
	setValue(t, &pveDirCandidates, []string{})
	setValue(t, &pbsDirCandidates, []string{})
	setValue(t, &pveVersionFile, filepath.Join(tmpDir, "missing-pve-version"))
	setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-pve-legacy"))
	setValue(t, &pbsVersionFile, filepath.Join(tmpDir, "missing-pbs-version"))

	t.Run("pve", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(binary string) (string, error) {
			if binary == "pveversion" {
				return "/fake/pveversion", nil
			}
			return "", errors.New("not found")
		})
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "pve-manager/7.4-3/d4a3b4a1", nil
		})

		pType, version, err := detectProxmox()
		if err != nil || pType != types.ProxmoxVE || version != "7.4-3" {
			t.Fatalf("detectProxmox() = (%v, %q, %v), want (%v, %q, %v)", pType, version, err, types.ProxmoxVE, "7.4-3", nil)
		}
	})

	t.Run("pbs", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(binary string) (string, error) {
			if binary == "proxmox-backup-manager" {
				return "/fake/proxmox-backup-manager", nil
			}
			return "", errors.New("not found")
		})
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "version: 2.4.1", nil
		})

		pType, version, err := detectProxmox()
		if err != nil || pType != types.ProxmoxBS || version != "2.4.1" {
			t.Fatalf("detectProxmox() = (%v, %q, %v), want (%v, %q, %v)", pType, version, err, types.ProxmoxBS, "2.4.1", nil)
		}
	})

	t.Run("unknown with debug", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })

		pType, version, err := detectProxmox()
		if pType != types.ProxmoxUnknown || version != "unknown" || err == nil {
			t.Fatalf("detectProxmox() = (%v, %q, %v), want (%v, %q, non-nil)", pType, version, err, types.ProxmoxUnknown, "unknown")
		}
		if !strings.Contains(err.Error(), "debug saved to") {
			t.Fatalf("expected error to contain %q, got %q", "debug saved to", err.Error())
		}
	})

	t.Run("unknown without debug", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })
		setValue(t, &mkdirAllFunc, func(string, os.FileMode) error { return errors.New("no perms") })

		pType, version, err := detectProxmox()
		if pType != types.ProxmoxUnknown || version != "unknown" || err == nil {
			t.Fatalf("detectProxmox() = (%v, %q, %v), want (%v, %q, non-nil)", pType, version, err, types.ProxmoxUnknown, "unknown")
		}
		if strings.Contains(err.Error(), "debug saved to") {
			t.Fatalf("expected error to NOT contain %q, got %q", "debug saved to", err.Error())
		}
	})
}

func TestDetect_Branches(t *testing.T) {
	tmpDir := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	setValue(t, &debugBaseDir, tmpDir)
	setValue(t, &pveSourceFiles, []string{})
	setValue(t, &pbsSourceFiles, []string{})
	setValue(t, &pveDirCandidates, []string{})
	setValue(t, &pbsDirCandidates, []string{})
	setValue(t, &pveVersionFile, filepath.Join(tmpDir, "missing-pve-version"))
	setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-pve-legacy"))
	setValue(t, &pbsVersionFile, filepath.Join(tmpDir, "missing-pbs-version"))

	t.Run("unknown", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })

		info, err := Detect()
		if info == nil {
			t.Fatal("Detect() returned nil info")
		}
		if info.Type != types.ProxmoxUnknown || info.Version != "unknown" {
			t.Fatalf("Detect() info = (%v, %q), want (%v, %q)", info.Type, info.Version, types.ProxmoxUnknown, "unknown")
		}
		if err == nil || err.Error() != "unable to detect Proxmox environment" {
			t.Fatalf("Detect() err = %v, want %q", err, "unable to detect Proxmox environment")
		}
	})

	t.Run("pve", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(binary string) (string, error) {
			if binary == "pveversion" {
				return "/fake/pveversion", nil
			}
			return "", errors.New("not found")
		})
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "pve-manager/7.4-3/d4a3b4a1", nil
		})

		info, err := Detect()
		if err != nil {
			t.Fatalf("Detect() err = %v, want nil", err)
		}
		if info == nil || info.Type != types.ProxmoxVE || info.Version != "7.4-3" {
			t.Fatalf("Detect() info = %#v, want type=%v version=%q", info, types.ProxmoxVE, "7.4-3")
		}
	})
}

func TestGetVersion_Branches(t *testing.T) {
	tmpDir := t.TempDir()
	setValue(t, &additionalPaths, []string{})
	setValue(t, &pveSourceFiles, []string{})
	setValue(t, &pbsSourceFiles, []string{})
	setValue(t, &pveDirCandidates, []string{})
	setValue(t, &pbsDirCandidates, []string{})
	setValue(t, &pveVersionFile, filepath.Join(tmpDir, "missing-pve-version"))
	setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-pve-legacy"))
	setValue(t, &pbsVersionFile, filepath.Join(tmpDir, "missing-pbs-version"))

	t.Run("pve success", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(binary string) (string, error) {
			if binary == "pveversion" {
				return "/fake/pveversion", nil
			}
			return "", errors.New("not found")
		})
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "pve-manager/7.4-3/d4a3b4a1", nil
		})

		version, err := GetVersion(types.ProxmoxVE)
		if err != nil || version != "7.4-3" {
			t.Fatalf("GetVersion(pve) = (%q, %v), want (%q, %v)", version, err, "7.4-3", nil)
		}
	})

	t.Run("pve error", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })
		sourceFile := filepath.Join(tmpDir, "pve.list")
		if err := os.WriteFile(sourceFile, []byte("pve-enterprise repository"), 0644); err != nil {
			t.Fatal(err)
		}
		setValue(t, &pveSourceFiles, []string{sourceFile})

		version, err := GetVersion(types.ProxmoxVE)
		if err == nil || version != "" {
			t.Fatalf("GetVersion(pve) = (%q, %v), want (\"\", error)", version, err)
		}
	})

	t.Run("pbs success", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(binary string) (string, error) {
			if binary == "proxmox-backup-manager" {
				return "/fake/proxmox-backup-manager", nil
			}
			return "", errors.New("not found")
		})
		setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
			return "version: 2.4.1", nil
		})

		version, err := GetVersion(types.ProxmoxBS)
		if err != nil || version != "2.4.1" {
			t.Fatalf("GetVersion(pbs) = (%q, %v), want (%q, %v)", version, err, "2.4.1", nil)
		}
	})

	t.Run("pbs error", func(t *testing.T) {
		setValue(t, &lookPathFunc, func(string) (string, error) { return "", errors.New("not found") })
		sourceFile := filepath.Join(tmpDir, "pbs.list")
		if err := os.WriteFile(sourceFile, []byte("proxmox-backup repository"), 0644); err != nil {
			t.Fatal(err)
		}
		setValue(t, &pbsSourceFiles, []string{sourceFile})

		version, err := GetVersion(types.ProxmoxBS)
		if err == nil || version != "" {
			t.Fatalf("GetVersion(pbs) = (%q, %v), want (\"\", error)", version, err)
		}
	})
}

func TestRunCommand_TimeoutBranch(t *testing.T) {
	setValue(t, &commandTimeout, 20*time.Millisecond)

	_, err := runCommand("/bin/sh", "-c", "sleep 1")
	if err == nil {
		t.Fatal("runCommand() should return timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %q", err.Error())
	}
}

func TestWriteDetectionDebug_Branches(t *testing.T) {
	t.Run("mkdir error", func(t *testing.T) {
		setValue(t, &mkdirAllFunc, func(string, os.FileMode) error { return errors.New("no perms") })
		setValue(t, &debugBaseDir, t.TempDir())

		if path := writeDetectionDebug(); path != "" {
			t.Fatalf("writeDetectionDebug() = %q, want empty string", path)
		}
	})

	t.Run("write file error", func(t *testing.T) {
		setValue(t, &debugBaseDir, t.TempDir())
		setValue(t, &writeFileFunc, func(string, []byte, os.FileMode) error { return errors.New("disk full") })

		if path := writeDetectionDebug(); path != "" {
			t.Fatalf("writeDetectionDebug() = %q, want empty string", path)
		}
	})

	t.Run("user error and version file contents", func(t *testing.T) {
		tmpDir := t.TempDir()
		setValue(t, &debugBaseDir, tmpDir)
		setValue(t, &userCurrentFunc, func() (*user.User, error) { return nil, errors.New("no user") })

		legacy := filepath.Join(tmpDir, "pve-legacy")
		version := filepath.Join(tmpDir, "pve-version")
		pbs := filepath.Join(tmpDir, "pbs-version")
		if err := os.WriteFile(legacy, []byte("pve-manager/7.4-3/d4a3b4a1"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(version, []byte("7.4-1"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pbs, []byte("2.4-1"), 0644); err != nil {
			t.Fatal(err)
		}
		setValue(t, &pveLegacyFile, legacy)
		setValue(t, &pveVersionFile, version)
		setValue(t, &pbsVersionFile, pbs)

		path := writeDetectionDebug()
		if path == "" {
			t.Fatal("writeDetectionDebug() returned empty path")
		}
		t.Cleanup(func() { _ = os.Remove(path) })

		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		contentStr := string(content)
		wantSubstrings := []string{
			"Current USER: unknown",
			fmt.Sprintf("%s content: %s", legacy, "pve-manager/7.4-3/d4a3b4a1"),
			fmt.Sprintf("%s content: %s", version, "7.4-1"),
			fmt.Sprintf("%s content: %s", pbs, "2.4-1"),
		}
		for _, want := range wantSubstrings {
			if !strings.Contains(contentStr, want) {
				t.Fatalf("debug file should contain %q", want)
			}
		}
	})
}
