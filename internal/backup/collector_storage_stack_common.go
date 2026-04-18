package backup

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (c *Collector) collectCommonFilesystemFstab(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := c.safeCopyFile(
		ctx,
		c.systemPath("/etc/fstab"),
		filepath.Join(c.tempDir, "etc/fstab"),
		"filesystem table",
	); err != nil {
		c.logger.Warning("Failed to collect /etc/fstab: %v", err)
	}

	return nil
}

func (c *Collector) collectCommonStorageStackCrypttab(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := c.safeCopyFile(
		ctx,
		c.systemPath("/etc/crypttab"),
		filepath.Join(c.tempDir, "etc/crypttab"),
		"crypttab",
	); err != nil {
		c.logger.Warning("Failed to collect /etc/crypttab: %v", err)
	}

	return nil
}

func (c *Collector) collectCommonStorageStackISCSISnapshot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, dir := range []struct {
		src  string
		dest string
		desc string
	}{
		{src: "/etc/iscsi", dest: filepath.Join(c.tempDir, "etc/iscsi"), desc: "iSCSI configuration"},
		{src: "/var/lib/iscsi", dest: filepath.Join(c.tempDir, "var/lib/iscsi"), desc: "iSCSI runtime state"},
	} {
		if err := c.safeCopyDir(ctx, c.systemPath(dir.src), dir.dest, dir.desc); err != nil {
			c.logger.Warning("Failed to collect %s (%s): %v", dir.desc, dir.src, err)
		}
	}

	return nil
}

func (c *Collector) collectCommonStorageStackMultipathSnapshot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := c.safeCopyDir(
		ctx,
		c.systemPath("/etc/multipath"),
		filepath.Join(c.tempDir, "etc/multipath"),
		"multipath configuration",
	); err != nil {
		c.logger.Warning("Failed to collect multipath configuration (/etc/multipath): %v", err)
	}
	if err := c.safeCopyFile(
		ctx,
		c.systemPath("/etc/multipath.conf"),
		filepath.Join(c.tempDir, "etc/multipath.conf"),
		"multipath.conf",
	); err != nil {
		c.logger.Warning("Failed to collect /etc/multipath.conf: %v", err)
	}

	return nil
}

func (c *Collector) collectCommonStorageStackMDADMSnapshot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := c.safeCopyDir(
		ctx,
		c.systemPath("/etc/mdadm"),
		filepath.Join(c.tempDir, "etc/mdadm"),
		"mdadm configuration",
	); err != nil {
		c.logger.Warning("Failed to collect mdadm configuration (/etc/mdadm): %v", err)
	}

	return nil
}

func (c *Collector) collectCommonStorageStackLVMSnapshot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, dir := range []struct {
		src  string
		dest string
		desc string
	}{
		{src: "/etc/lvm/backup", dest: filepath.Join(c.tempDir, "etc/lvm/backup"), desc: "LVM metadata backups"},
		{src: "/etc/lvm/archive", dest: filepath.Join(c.tempDir, "etc/lvm/archive"), desc: "LVM metadata archives"},
	} {
		if err := c.safeCopyDir(ctx, c.systemPath(dir.src), dir.dest, dir.desc); err != nil {
			c.logger.Warning("Failed to collect %s (%s): %v", dir.desc, dir.src, err)
		}
	}

	return nil
}

func (c *Collector) collectCommonStorageStackMountUnitsSnapshot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := c.safeCopySystemdMountUnitFiles(ctx); err != nil {
		c.logger.Warning("Failed to collect systemd mount units: %v", err)
	}

	return nil
}

func (c *Collector) collectCommonStorageStackAutofsSnapshot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := c.safeCopyAutofsMapFiles(ctx); err != nil {
		c.logger.Warning("Failed to collect autofs map files: %v", err)
	}

	return nil
}

func (c *Collector) collectCommonStorageStackReferencedFiles(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	crypttabContent, _ := os.ReadFile(c.systemPath("/etc/crypttab"))
	fstabContent, _ := os.ReadFile(c.systemPath("/etc/fstab"))
	refs := uniqueSortedStrings(append(
		extractCrypttabKeyFiles(string(crypttabContent)),
		extractFstabReferencedFiles(string(fstabContent))...,
	))

	for _, ref := range refs {
		dest := filepath.Join(c.tempDir, strings.TrimPrefix(ref, "/"))
		if err := c.safeCopyFile(ctx, c.systemPath(ref), dest, "referenced file"); err != nil {
			c.logger.Warning("Failed to collect referenced file %s: %v", ref, err)
		}
	}

	return nil
}

func extractCrypttabKeyFiles(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	var out []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		keyFile := strings.TrimSpace(fields[2])
		if keyFile == "" || keyFile == "none" || keyFile == "-" {
			continue
		}
		if strings.HasPrefix(keyFile, "/") {
			out = append(out, keyFile)
		}
	}

	return uniqueSortedStrings(out)
}

func extractFstabReferencedFiles(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	keys := map[string]struct{}{
		"credentials":  {},
		"cred":         {},
		"passwd":       {},
		"passfile":     {},
		"keyfile":      {},
		"identityfile": {},
	}

	var out []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		for _, opt := range strings.Split(fields[3], ",") {
			opt = strings.TrimSpace(opt)
			if opt == "" || !strings.Contains(opt, "=") {
				continue
			}
			parts := strings.SplitN(opt, "=", 2)
			key := strings.ToLower(strings.TrimSpace(parts[0]))
			val := strings.TrimSpace(parts[1])
			if key == "" || val == "" {
				continue
			}
			if _, ok := keys[key]; !ok {
				continue
			}
			if strings.HasPrefix(val, "/") {
				out = append(out, val)
			}
		}
	}

	return uniqueSortedStrings(out)
}

func (c *Collector) safeCopySystemdMountUnitFiles(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	base := c.systemPath("/etc/systemd/system")
	info, err := os.Stat(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", base, err)
	}
	if !info.IsDir() {
		return nil
	}

	destBase := filepath.Join(c.tempDir, "etc/systemd/system")
	if c.shouldExclude(base) || c.shouldExclude(destBase) {
		c.incFilesSkipped()
		return nil
	}

	if c.dryRun {
		return nil
	}
	if err := c.ensureDir(destBase); err != nil {
		return err
	}

	return filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		if err != nil {
			return err
		}
		if info == nil || info.IsDir() {
			return nil
		}
		name := strings.ToLower(info.Name())
		if !strings.HasSuffix(name, ".mount") && !strings.HasSuffix(name, ".automount") {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(destBase, rel)
		if c.shouldExclude(path) || c.shouldExclude(dest) {
			return nil
		}
		return c.safeCopyFile(ctx, path, dest, "systemd mount unit")
	})
}

func (c *Collector) safeCopyAutofsMapFiles(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, path := range []string{
		"/etc/auto.master",
		"/etc/autofs.conf",
	} {
		src := c.systemPath(path)
		dest := filepath.Join(c.tempDir, strings.TrimPrefix(path, "/"))
		if err := c.safeCopyFile(ctx, src, dest, "autofs config"); err != nil {
			continue
		}
	}

	matches, _ := filepath.Glob(c.systemPath("/etc/auto.*"))
	for _, src := range matches {
		base := filepath.Base(src)
		if base == "auto.master" {
			continue
		}
		dest := filepath.Join(c.tempDir, "etc", base)
		_ = c.safeCopyFile(ctx, src, dest, "autofs map")
	}

	_ = c.safeCopyDir(ctx, c.systemPath("/etc/auto.master.d"), filepath.Join(c.tempDir, "etc/auto.master.d"), "autofs drop-in configs")
	return nil
}
