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
	info, err := restoreFS.Stat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", srcDir, err)
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

		if entry.IsDir() {
			paths, err := copyDirOverlay(src, dest)
			if err != nil {
				return applied, err
			}
			applied = append(applied, paths...)
			continue
		}

		ok, err := copyFileOverlay(src, dest)
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
	info, err := restoreFS.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", src, err)
	}
	if info.IsDir() {
		return false, nil
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
