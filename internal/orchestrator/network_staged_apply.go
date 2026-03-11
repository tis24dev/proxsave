package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func applyNetworkFilesFromStage(logger *logging.Logger, stageRoot string) (applied []string, err error) {
	stageRoot = strings.TrimSpace(stageRoot)
	done := logging.DebugStart(logger, "network staged apply", "stage=%s", stageRoot)
	defer func() { done(err) }()

	if stageRoot == "" {
		return nil, nil
	}

	type stageItem struct {
		Rel  string
		Dest string
		Kind string
	}

	items := []stageItem{
		{Rel: "etc/network", Dest: "/etc/network", Kind: "dir"},
		{Rel: "etc/hosts", Dest: "/etc/hosts", Kind: "file"},
		{Rel: "etc/hostname", Dest: "/etc/hostname", Kind: "file"},
		{Rel: "etc/cloud/cloud.cfg.d/99-disable-network-config.cfg", Dest: "/etc/cloud/cloud.cfg.d/99-disable-network-config.cfg", Kind: "file"},
		{Rel: "etc/dnsmasq.d/lxc-vmbr1.conf", Dest: "/etc/dnsmasq.d/lxc-vmbr1.conf", Kind: "file"},
		// NOTE: /etc/resolv.conf intentionally not copied from backup; it is repaired/validated separately.
	}

	for _, item := range items {
		src := filepath.Join(stageRoot, filepath.FromSlash(item.Rel))
		switch item.Kind {
		case "dir":
			paths, err := copyDirOverlay(src, item.Dest)
			if err != nil {
				return applied, err
			}
			applied = append(applied, paths...)
		case "file":
			ok, err := copyFileOverlay(src, item.Dest)
			if err != nil {
				return applied, err
			}
			if ok {
				applied = append(applied, item.Dest)
			}
		default:
			return applied, fmt.Errorf("unknown staged item kind %q", item.Kind)
		}
	}

	return applied, nil
}

func copyDirOverlay(srcDir, destDir string) ([]string, error) {
	return copyDirOverlayWithinRoot(srcDir, destDir, destDir)
}

func copyDirOverlayWithinRoot(srcDir, destDir, destRoot string) ([]string, error) {
	info, err := restoreFS.Lstat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", srcDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("staged directory must not be a symlink: %s", srcDir)
	}
	if !info.IsDir() {
		return nil, nil
	}

	if err := ensureDirExistsWithInheritedMeta(destDir); err != nil {
		return nil, fmt.Errorf("ensure %s: %w", destDir, err)
	}

	var applied []string
	entries, err := restoreFS.ReadDir(srcDir)
	if err != nil {
		return nil, fmt.Errorf("readdir %s: %w", srcDir, err)
	}

	for _, entry := range entries {
		if entry == nil {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		src := filepath.Join(srcDir, name)
		dest := filepath.Join(destDir, name)

		info, err := restoreFS.Lstat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return applied, fmt.Errorf("stat %s: %w", src, err)
		}

		if info.Mode()&os.ModeSymlink != 0 {
			ok, err := copySymlinkOverlayWithinRoot(src, dest, destRoot)
			if err != nil {
				return applied, err
			}
			if ok {
				applied = append(applied, dest)
			}
			continue
		}

		if info.IsDir() {
			paths, err := copyDirOverlayWithinRoot(src, dest, destRoot)
			if err != nil {
				return applied, err
			}
			applied = append(applied, paths...)
			continue
		}

		ok, err := copyFileOverlayWithinRoot(src, dest, destRoot)
		if err != nil {
			return applied, err
		}
		if ok {
			applied = append(applied, dest)
		}
	}

	return applied, nil
}

func copyFileOverlay(src, dest string) (bool, error) {
	return copyFileOverlayWithinRoot(src, dest, filepath.Dir(dest))
}

func copyFileOverlayWithinRoot(src, dest, destRoot string) (bool, error) {
	info, err := restoreFS.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", src, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return copySymlinkOverlayWithinRoot(src, dest, destRoot)
	}
	if info.IsDir() {
		return false, nil
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("unsupported staged file type %s (mode=%s)", src, info.Mode())
	}

	data, err := restoreFS.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", src, err)
	}

	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode().Perm()
	}
	if err := writeFileAtomic(dest, data, mode); err != nil {
		return false, fmt.Errorf("write %s: %w", dest, err)
	}
	return true, nil
}

func copySymlinkOverlay(src, dest string) (bool, error) {
	return copySymlinkOverlayWithinRoot(src, dest, filepath.Dir(dest))
}

func copySymlinkOverlayWithinRoot(src, dest, destRoot string) (bool, error) {
	info, err := restoreFS.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", src, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, fmt.Errorf("source is not a symlink: %s", src)
	}

	target, err := restoreFS.Readlink(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("readlink %s: %w", src, err)
	}

	validatedTarget, err := validateOverlaySymlinkTargetWithinRoot(destRoot, dest, target)
	if err != nil {
		return false, fmt.Errorf("unsafe symlink target %s -> %s: %w", dest, target, err)
	}

	if err := ensureDirExistsWithInheritedMeta(filepath.Dir(dest)); err != nil {
		return false, fmt.Errorf("ensure %s: %w", filepath.Dir(dest), err)
	}

	if existing, err := restoreFS.Lstat(dest); err == nil {
		if existing.IsDir() {
			return false, fmt.Errorf("destination exists as directory: %s", dest)
		}
		if err := restoreFS.Remove(dest); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("remove %s: %w", dest, err)
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat %s: %w", dest, err)
	}

	if err := restoreFS.Symlink(validatedTarget, dest); err != nil {
		return false, fmt.Errorf("symlink %s -> %s: %w", dest, validatedTarget, err)
	}
	return true, nil
}

func validateOverlaySymlinkTargetWithinRoot(destRoot, dest, target string) (string, error) {
	destRoot = filepath.Clean(strings.TrimSpace(destRoot))
	dest = filepath.Clean(strings.TrimSpace(dest))

	resolved, err := resolvePathRelativeToBaseWithinRootFS(restoreFS, destRoot, filepath.Dir(dest), target)
	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(target) {
		return target, nil
	}

	resolvedParent, err := resolvePathRelativeToBaseWithinRootFS(restoreFS, destRoot, filepath.Dir(dest), ".")
	if err != nil {
		return "", err
	}

	rewrittenTarget, err := filepath.Rel(resolvedParent, resolved)
	if err != nil {
		return "", fmt.Errorf("rewrite symlink target %s -> %s: %w", dest, resolved, err)
	}
	return filepath.Clean(rewrittenTarget), nil
}
