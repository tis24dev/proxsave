package orchestrator

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

const (
	resolvConfPath       = "/etc/resolv.conf"
	maxResolvConfSize    = 64 * 1024
	resolvConfRepairWait = 2 * time.Second
)

func maybeRepairResolvConfAfterRestore(ctx context.Context, logger *logging.Logger, archivePath string, dryRun bool) (err error) {
	done := logging.DebugStart(logger, "resolv.conf repair", "dryRun=%v archive=%s", dryRun, filepath.Base(strings.TrimSpace(archivePath)))
	defer func() { done(err) }()

	if dryRun {
		logger.Info("Dry run enabled: skipping /etc/resolv.conf repair")
		return nil
	}

	needsRepair := false
	reason := ""

	linkTarget, linkErr := restoreFS.Readlink(resolvConfPath)
	if linkErr == nil {
		logging.DebugStep(logger, "resolv.conf repair", "Detected symlink: %s -> %s", resolvConfPath, linkTarget)
		if isProxsaveCommandsSymlink(linkTarget) {
			needsRepair = true
			reason = "symlink points to proxsave commands output"
		}
		if _, err := restoreFS.Stat(resolvConfPath); err != nil {
			needsRepair = true
			if reason == "" {
				reason = fmt.Sprintf("broken symlink: %v", err)
			}
		}
	} else {
		if _, err := restoreFS.Stat(resolvConfPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				needsRepair = true
				reason = "missing"
			} else {
				logger.Warning("DNS resolver preflight: stat %s failed: %v", resolvConfPath, err)
			}
		}
	}

	if !needsRepair {
		logging.DebugStep(logger, "resolv.conf repair", "No action required")
		return nil
	}

	if reason == "" {
		reason = "unknown"
	}
	logger.Warning("DNS resolver preflight: %s needs repair (%s)", resolvConfPath, reason)

	if err := removeResolvConfIfPresent(); err != nil {
		return err
	}

	if repaired, err := repairResolvConfWithSystemdResolved(logger); err != nil {
		return err
	} else if repaired {
		return nil
	}

	if strings.TrimSpace(archivePath) != "" {
		candidates := []string{
			"var/lib/proxsave-info/commands/system/resolv_conf.txt",
			"commands/resolv_conf.txt",
		}
		for _, candidate := range candidates {
			data, err := readTarEntry(ctx, archivePath, candidate, maxResolvConfSize)
			if err == nil && hasNameserverEntries(string(data)) {
				logging.DebugStep(logger, "resolv.conf repair", "Using DNS resolver content from archive %s", candidate)
				if err := restoreFS.WriteFile(resolvConfPath, normalizeResolvConf(data), 0o644); err != nil {
					return fmt.Errorf("write %s: %w", resolvConfPath, err)
				}
				logger.Info("DNS resolver repaired: restored %s from archive diagnostics", resolvConfPath)
				return nil
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				logger.Debug("DNS resolver repair: could not read %s from archive: %v", candidate, err)
			}
		}
	}

	dns1, dns2 := fallbackDNSFromGateway(ctx, logger)
	contents := fmt.Sprintf("nameserver %s\nnameserver %s\noptions timeout:2 attempts:2\n", dns1, dns2)
	if err := restoreFS.WriteFile(resolvConfPath, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", resolvConfPath, err)
	}
	logger.Warning("DNS resolver repaired: wrote static %s (nameserver=%s,%s)", resolvConfPath, dns1, dns2)
	return nil
}

func isProxsaveCommandsSymlink(target string) bool {
	target = filepath.ToSlash(strings.TrimSpace(target))
	return strings.Contains(target, "var/lib/proxsave-info/commands/system/resolv_conf.txt") ||
		strings.Contains(target, "commands/resolv_conf.txt")
}

func removeResolvConfIfPresent() error {
	if err := restoreFS.Remove(resolvConfPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove %s: %w", resolvConfPath, err)
	}
	return nil
}

func repairResolvConfWithSystemdResolved(logger *logging.Logger) (bool, error) {
	type candidate struct {
		target string
		desc   string
	}
	candidates := []candidate{
		{target: "/run/systemd/resolve/resolv.conf", desc: "systemd-resolved resolv.conf"},
		{target: "/run/systemd/resolve/stub-resolv.conf", desc: "systemd-resolved stub-resolv.conf"},
	}

	for _, c := range candidates {
		if _, err := restoreFS.Stat(c.target); err != nil {
			continue
		}

		logging.DebugStep(logger, "resolv.conf repair", "Linking %s -> %s (%s)", resolvConfPath, c.target, c.desc)
		if err := restoreFS.Symlink(c.target, resolvConfPath); err != nil {
			return false, fmt.Errorf("symlink %s -> %s: %w", resolvConfPath, c.target, err)
		}
		logger.Info("DNS resolver repaired: %s linked to %s", resolvConfPath, c.target)
		return true, nil
	}

	return false, nil
}

func readTarEntry(ctx context.Context, archivePath, name string, maxBytes int64) ([]byte, error) {
	file, err := restoreFS.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	reader, err := createDecompressionReader(ctx, file, archivePath)
	if err != nil {
		return nil, fmt.Errorf("create decompression reader: %w", err)
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}

	wantA := strings.TrimPrefix(strings.TrimSpace(name), "./")
	wantB := "./" + wantA
	tarReader := tar.NewReader(reader)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		header, err := tarReader.Next()
		if err == io.EOF {
			return nil, os.ErrNotExist
		}
		if err != nil {
			return nil, err
		}

		if header.Name != wantA && header.Name != wantB {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("archive entry %s is not a regular file", header.Name)
		}

		limit := maxBytes
		if header.Size > 0 && header.Size < limit {
			limit = header.Size
		}
		lr := io.LimitReader(tarReader, limit+1)
		data, err := io.ReadAll(lr)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("archive entry %s too large (%d bytes)", header.Name, header.Size)
		}
		return data, nil
	}
}

func hasNameserverEntries(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "nameserver") {
			return true
		}
	}
	return false
}

func normalizeResolvConf(data []byte) []byte {
	out := strings.ReplaceAll(string(data), "\r\n", "\n")
	out = strings.TrimRight(out, "\n") + "\n"
	return []byte(out)
}

func fallbackDNSFromGateway(ctx context.Context, logger *logging.Logger) (string, string) {
	dns2 := "1.1.1.1"
	ctxTimeout, cancel := context.WithTimeout(ctx, resolvConfRepairWait)
	defer cancel()

	out, err := restoreCmd.Run(ctxTimeout, "ip", "route", "show", "default")
	if err != nil {
		logging.DebugStep(logger, "resolv.conf repair", "ip route show default failed: %v", err)
		return dns2, dns2
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return dns2, dns2
	}
	first := strings.SplitN(line, "\n", 2)[0]
	fields := strings.Fields(first)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "via" {
			gw := strings.TrimSpace(fields[i+1])
			if gw != "" {
				return gw, dns2
			}
		}
	}
	return dns2, dns2
}
