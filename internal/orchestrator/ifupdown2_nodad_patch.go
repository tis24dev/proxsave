package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

var ifupdown2NodadPatchOnce sync.Once

// maybePatchIfupdown2NodadBug attempts to apply a small compatibility patch for a known ifupdown2
// dry-run bug on some Proxmox builds (e.g. 3.3.0-1+pmx11), where addr_add_dry_run() does not accept
// the "nodad" keyword argument and crashes preflight runs.
//
// The patch is only attempted once per process.
func maybePatchIfupdown2NodadBug(ctx context.Context, logger *logging.Logger) {
	ifupdown2NodadPatchOnce.Do(func() {
		_ = patchIfupdown2NodadBugOnce(ctx, logger)
	})
}

func patchIfupdown2NodadBugOnce(ctx context.Context, logger *logging.Logger) error {
	if logger == nil {
		return nil
	}
	if !isRealRestoreFS(restoreFS) {
		return nil
	}

	// Only patch a known Proxmox package build unless explicitly needed later.
	if !commandAvailable("dpkg-query") {
		logger.Debug("ifupdown2 nodad patch: skipped (dpkg-query not available)")
		return nil
	}

	versionOut, err := restoreCmd.Run(ctx, "dpkg-query", "-W", "-f=${Version}", "ifupdown2")
	if err != nil {
		logger.Debug("ifupdown2 nodad patch: skipped (dpkg-query failed: %v)", err)
		return nil
	}
	version := strings.TrimSpace(string(versionOut))
	if version != "3.3.0-1+pmx11" {
		logger.Debug("ifupdown2 nodad patch: skipped (ifupdown2 version=%q not targeted)", version)
		return nil
	}

	const nlcachePath = "/usr/share/ifupdown2/lib/nlcache.py"

	contentBytes, err := restoreFS.ReadFile(nlcachePath)
	if err != nil {
		logger.Warning("ifupdown2 nodad patch: failed to read %s: %v", nlcachePath, err)
		return err
	}
	backupPath, applied, err := patchIfupdown2NlcacheNodadSignature(restoreFS, nlcachePath, contentBytes, nowRestore())
	if err != nil {
		logger.Warning("ifupdown2 nodad patch: failed: %v", err)
		return err
	}
	if !applied {
		logger.Debug("ifupdown2 nodad patch: already applied or not needed (%s)", nlcachePath)
		return nil
	}
	logger.Warning("Applied ifupdown2 compatibility patch for dry-run nodad bug (version=%s). Backup: %s", version, backupPath)
	return nil
}

func patchIfupdown2NlcacheNodadSignature(fs FS, nlcachePath string, original []byte, now time.Time) (backupPath string, applied bool, err error) {
	if fs == nil {
		return "", false, fmt.Errorf("nil filesystem")
	}
	path := strings.TrimSpace(nlcachePath)
	if path == "" {
		return "", false, fmt.Errorf("empty nlcache path")
	}

	oldSig := "def addr_add_dry_run(self, ifname, addr, broadcast=None, peer=None, scope=None, preferred_lifetime=None, metric=None):"
	newSig := "def addr_add_dry_run(self, ifname, addr, broadcast=None, peer=None, scope=None, preferred_lifetime=None, metric=None, nodad=False):"

	content := string(original)
	switch {
	case strings.Contains(content, newSig):
		return "", false, nil
	case !strings.Contains(content, oldSig):
		return "", false, fmt.Errorf("signature not found in %s", path)
	}

	fi, statErr := fs.Stat(path)
	mode := os.FileMode(0o644)
	if statErr == nil {
		mode = fi.Mode()
	}

	ts := now.Format("2006-01-02_150405")
	backupPath = path + ".bak." + ts
	if err := fs.WriteFile(backupPath, original, mode); err != nil {
		return "", false, fmt.Errorf("write backup %s: %w", backupPath, err)
	}

	patched := strings.Replace(content, oldSig, newSig, 1)
	if err := fs.WriteFile(path, []byte(patched), mode); err != nil {
		return backupPath, false, fmt.Errorf("write patched file %s: %w", path, err)
	}
	return backupPath, true, nil
}
