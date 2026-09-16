package orchestrator

import (
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
)

// TestAPartialDualArchiveIsNotADualBackup is the restore half of the partial-archive
// contract. The collection manifest is an ExportOnly diagnostic restore never opens,
// so recording the gap only there let a dual archive that lost its PBS half clear the
// compatibility check against a dual host as though nothing were missing: PBS
// categories offered, nothing behind them.
func TestAPartialDualArchiveIsNotADualBackup(t *testing.T) {
	manifest := &backup.Manifest{
		ProxmoxType:       "dual",
		ProxmoxTargets:    []string{"pve", "pbs"},
		IncompleteTargets: []string{"pbs"},
	}

	if got := DetectBackupType(manifest); got != SystemTypePVE {
		t.Fatalf("DetectBackupType() = %s, want %s: the archive carries no PBS payload", got, SystemTypePVE)
	}

	err := ValidateCompatibility(SystemTypeDual, DetectBackupType(manifest))
	if err == nil {
		t.Fatal("ValidateCompatibility passed a half-empty archive as a whole one")
	}
	if !strings.Contains(err.Error(), "partial compatibility") {
		t.Fatalf("ValidateCompatibility error = %v, want it to report partial compatibility", err)
	}
}

// TestAWholeDualArchiveIsStillDual: the subtraction must not fire on a complete run,
// or every dual backup would restore as half of itself.
func TestAWholeDualArchiveIsStillDual(t *testing.T) {
	manifest := &backup.Manifest{ProxmoxType: "dual", ProxmoxTargets: []string{"pve", "pbs"}}

	if got := DetectBackupType(manifest); got != SystemTypeDual {
		t.Fatalf("DetectBackupType() = %s, want %s", got, SystemTypeDual)
	}
	if err := ValidateCompatibility(SystemTypeDual, SystemTypeDual); err != nil {
		t.Fatalf("ValidateCompatibility = %v, want a whole dual archive to pass", err)
	}
}

// TestAnArchiveThatLostEveryRoleIsUnknown: with both halves gone there is no role
// payload at all, and claiming either product would send restore looking for files
// the archive does not hold.
func TestAnArchiveThatLostEveryRoleIsUnknown(t *testing.T) {
	manifest := &backup.Manifest{
		ProxmoxType:       "dual",
		ProxmoxTargets:    []string{"pve", "pbs"},
		IncompleteTargets: []string{"pve", "pbs"},
	}

	if got := DetectBackupType(manifest); got != SystemTypeUnknown {
		t.Fatalf("DetectBackupType() = %s, want %s", got, SystemTypeUnknown)
	}
}

// TestIncompleteTargetMatchingIgnoresCaseAndSpace: the two lists are written by
// different code paths, ProxmoxType.Targets() and the collector naming the failed
// recipe, so the match cannot depend on them agreeing on spelling.
func TestIncompleteTargetMatchingIgnoresCaseAndSpace(t *testing.T) {
	manifest := &backup.Manifest{
		ProxmoxType:       "dual",
		ProxmoxTargets:    []string{"pve", "pbs"},
		IncompleteTargets: []string{" PBS "},
	}

	if got := DetectBackupType(manifest); got != SystemTypePVE {
		t.Fatalf("DetectBackupType() = %s, want %s", got, SystemTypePVE)
	}
}
