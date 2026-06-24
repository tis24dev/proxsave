// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

// mountGuardChattrTargetsName is the index file (under mountGuardBaseDir) that
// records mountpoint directories ProxSave marked immutable via the `chattr +i`
// fallback guard during restore. CleanupMountGuards reads it back to clear
// exactly those directories (and only those) with `chattr -i`.
//
// Unlike the read-only bind-mount guard — which a later real mount simply shadows
// and a reboot discards — the immutable flag persists on the directory inode
// across reboots and is never cleared automatically, so it must be recorded for
// later cleanup.
const mountGuardChattrTargetsName = "chattr-targets"

// maxChattrIndexBytes bounds how large the index may grow before we refuse to
// parse it. A corrupt/oversized index is treated as empty (fail-safe: skip the
// auto-clear rather than risk unbounded memory); the operator can still run
// `chattr -i` manually, exactly as before this index existed.
const maxChattrIndexBytes = 1 << 20 // 1 MiB

// mountGuardChattrTargetsPath returns the absolute path of the immutable-guard index.
func mountGuardChattrTargetsPath() string {
	return filepath.Join(mountGuardBaseDir, mountGuardChattrTargetsName)
}

// isUnsafeIndexRune reports control characters that have no place in a
// one-path-per-line index (C0 controls incl. newline/CR, DEL, and the C1 range).
func isUnsafeIndexRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// isRecordableImmutableTarget cleans and validates a candidate target for the
// index. It rejects empty/"."/root, anything that is not a confirmable datastore
// mount root (/mnt, /media, /run/media), and — critically — any value containing
// a newline or other control character, so a single malformed path can never
// inject extra index lines or smuggle a non-datastore path past the cleanup
// allowlist.
func isRecordableImmutableTarget(target string) (string, bool) {
	clean := filepath.Clean(strings.TrimSpace(target))
	if !isValidGuardTarget(clean) {
		return "", false
	}
	if strings.IndexFunc(clean, isUnsafeIndexRune) >= 0 {
		return "", false
	}
	if !isConfirmableDatastoreMountRoot(clean) {
		return "", false
	}
	return clean, true
}

// parseImmutableGuardTargets parses index bytes into cleaned, validated,
// de-duplicated targets in first-seen order. Tolerant of blank lines, trailing
// newlines, CRLF, and duplicates; lines that fail validation are dropped. An
// oversized blob is treated as empty (see maxChattrIndexBytes).
func parseImmutableGuardTargets(data []byte) []string {
	if len(data) == 0 || len(data) > maxChattrIndexBytes {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		clean := filepath.Clean(strings.TrimSpace(line))
		if !isValidGuardTarget(clean) {
			continue
		}
		if _, dup := seen[clean]; dup {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

// recordImmutableGuardTarget appends target to the immutable-guard index after a
// successful `chattr +i`, so CleanupMountGuards can later clear exactly the
// directories ProxSave itself made immutable. Best-effort: any failure is logged
// and swallowed so a recording problem never aborts the restore. The write is
// atomic and de-duplicated.
func recordImmutableGuardTarget(logger *logging.Logger, target string) {
	clean, ok := isRecordableImmutableTarget(target)
	if !ok {
		if logger != nil {
			logger.Warning("Guard chattr index: refusing to record unsafe immutable target %q", target)
		}
		return
	}

	if err := os.MkdirAll(mountGuardBaseDir, 0o755); err != nil {
		if logger != nil {
			logger.Warning("Guard chattr index: unable to create %s: %v (cleanup will need a manual chattr -i %s)", mountGuardBaseDir, err, clean)
		}
		return
	}

	indexPath := mountGuardChattrTargetsPath()
	existing := readImmutableGuardIndex(indexPath)
	for _, t := range existing {
		if t == clean {
			return // already recorded; keep the index stable
		}
	}

	payload := strings.Join(append(existing, clean), "\n") + "\n"
	if err := writeImmutableGuardIndex(indexPath, []byte(payload), 0o600); err != nil {
		if logger != nil {
			logger.Warning("Guard chattr index: unable to record %s in %s: %v (cleanup will need a manual chattr -i)", clean, indexPath, err)
		}
	}
}

// readImmutableGuardIndex reads and parses the index at path. A missing or
// unreadable index yields no targets (the index is best-effort metadata).
func readImmutableGuardIndex(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseImmutableGuardTargets(data)
}

// writeImmutableGuardIndex writes the index atomically (temp file + rename) on the
// REAL host filesystem. The index is host state under /var/lib/proxsave — it is not
// part of the staged restore tree — so it deliberately uses os.* directly rather
// than the restoreFS abstraction (which may be a fake/overlay during restore).
func writeImmutableGuardIndex(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".chattr-targets-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
