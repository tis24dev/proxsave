package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// These tests pin the dry-run honesty of the resolve-before-decide ordering in
// clearImmutableGuards: once resolution runs first, the dry-run preview must
// reflect exactly what a real run would do (a missing leaf clears nothing; an
// allowlist-escaping target stays pending), so the "would remove" preview never
// over-promises.

// TestCleanupMountGuards_MissingLeafNotCountedWouldClear proves that a target
// whose resolved leaf no longer exists is NOT counted as "would clear" in dry-run.
// It must be reported as nothing-to-clear (neither cleared nor pending), so the
// dry-run "would remove" preview stays honest.
func TestCleanupMountGuards_MissingLeafNotCountedWouldClear(t *testing.T) {
	withTempGuardBaseDir(t)
	ran := installChattrCleanupSeams(t, []byte("/mnt/pve/gone\n"), "", nil)
	// The leaf no longer exists: EvalSymlinks reports ENOENT and the deepest
	// existing ancestor (/mnt) resolves to itself, so leafExists is false.
	resolveGuardTarget = func(p string) (string, error) {
		if filepath.Clean(p) == "/mnt/pve/gone" {
			return "", os.ErrNotExist
		}
		return p, nil
	}

	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	if err := CleanupMountGuards(context.Background(), logger, true); err != nil {
		t.Fatalf("CleanupMountGuards dry-run: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("dry-run must run no chattr, calls=%#v", *ran)
	}
	out := buf.String()
	// A missing leaf is "nothing to clear": never counted as cleared/would-clear
	// and never pending.
	if !strings.Contains(out, "immutable-cleared=0") || !strings.Contains(out, "immutable-pending=0") {
		t.Fatalf("missing-leaf target must be neither cleared nor pending; out=%q", out)
	}
	if strings.Contains(out, "would clear immutable flag") {
		t.Fatalf("missing-leaf target must not be reported as 'would clear'; out=%q", out)
	}
}

// TestCleanupMountGuards_DryRunOutOfAllowlistPending proves that a target whose
// resolved path escapes the datastore allowlist is counted as pending in dry-run
// (not as "would clear"), matching what a real run would leave behind.
func TestCleanupMountGuards_DryRunOutOfAllowlistPending(t *testing.T) {
	withTempGuardBaseDir(t)
	ran := installChattrCleanupSeams(t, []byte("/mnt/pve/evil\n"), "", nil)
	// Passes the textual allowlist but resolves outside it.
	resolveGuardTarget = func(p string) (string, error) {
		if filepath.Clean(p) == "/mnt/pve/evil" {
			return "/etc/evil", nil
		}
		return p, nil
	}

	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	if err := CleanupMountGuards(context.Background(), logger, true); err != nil {
		t.Fatalf("CleanupMountGuards dry-run: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("dry-run must run no chattr, calls=%#v", *ran)
	}
	out := buf.String()
	if !strings.Contains(out, "immutable-cleared=0") || !strings.Contains(out, "immutable-pending=1") {
		t.Fatalf("out-of-allowlist target must be pending in dry-run; out=%q", out)
	}
	// Honest preview: a pending target keeps the dir (guard-dir=kept), not removed.
	if !strings.Contains(out, "guard-dir=kept") {
		t.Fatalf("dry-run with a pending target must preview keeping the dir; out=%q", out)
	}
	if strings.Contains(out, "would remove") {
		t.Fatalf("dry-run with a pending target must not preview removing the dir; out=%q", out)
	}
}
