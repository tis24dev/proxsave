package identity

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These tests pin readFirstLine as it behaves today, for the four fixed host files
// identity reads, for the per-interface addr_assign_type, and for the temp-file
// cases the other tests use. They are the safety net for reading the fixed files by
// their constant path and the per-interface attribute through os.Root, so they
// assert values and states, never how the file is opened.

// plainFirstLine is what readFirstLine returns when the file is read with a plain
// os.ReadFile of path: the reference for the live host files.
func plainFirstLine(path string, limit int) (string, systemFileState) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", systemFileAbsent
	case errors.Is(err, fs.ErrPermission):
		return "", systemFileDenied
	case err != nil:
		return "", systemFileUnreadable
	}
	line := strings.TrimSpace(string(data))
	if limit > 0 && len(line) > limit {
		line = line[:limit]
	}
	if line == "" {
		return "", systemFileAbsent
	}
	return line, systemFilePresent
}

// The four fixed host files, with the limits identity reads them with, on whatever
// host runs the tests: present, absent or denied, the value and the state must be
// what a plain read gives.
func TestReadFirstLineFixedHostFilesCharacterization(t *testing.T) {
	for _, tc := range []struct {
		path  string
		limit int
	}{
		{machineIDPath, maxMachineIDBytes},
		{dbusMachineIDPath, maxMachineIDBytes},
		{productUUIDPath, maxMachineIDBytes},
		{procVersionPath, maxProcVersionBytes},
	} {
		gotValue, gotState := readFirstLine(tc.path, tc.limit)
		wantValue, wantState := plainFirstLine(tc.path, tc.limit)
		if gotValue != wantValue || gotState != wantState {
			t.Errorf("readFirstLine(%s, %d) = (%q, %s), want (%q, %s)", tc.path, tc.limit, gotValue, gotState, wantValue, wantState)
		}
		t.Logf("%s: state=%s len=%d", tc.path, gotState, len(gotValue))
	}
}

// Every network interface the host has: readAddrAssignType, through the real seam,
// returns what a plain read of /sys/class/net/<iface>/addr_assign_type gives.
func TestReadAddrAssignTypeLiveInterfacesCharacterization(t *testing.T) {
	if readFirstLineFunc == nil {
		t.Fatal("readFirstLineFunc is nil")
	}
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		t.Skipf("no /sys/class/net: %v", err)
	}
	if len(entries) == 0 {
		t.Skip("no interfaces")
	}
	for _, e := range entries {
		iface := e.Name()
		value, _ := plainFirstLine(filepath.Join("/sys/class/net", iface, "addr_assign_type"), 16)
		want := -1
		if v, err := strconv.Atoi(value); err == nil {
			want = v
		}
		if got := readAddrAssignType(iface, nil); got != want {
			t.Errorf("readAddrAssignType(%s) = %d, want %d", iface, got, want)
		}
	}
}

func TestReadFirstLineTempFilesCharacterization(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}
	content := write("content", "  FIRST line \nsecond\n")
	empty := write("empty", "")
	blank := write("blank", " \n\t\n")
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sibling := filepath.Join(dir, "sibling-link")
	if err := os.Symlink("content", sibling); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	for _, tc := range []struct {
		name      string
		path      string
		limit     int
		wantValue string
		wantState systemFileState
	}{
		{"trimmed and truncated", content, 5, "FIRST", systemFilePresent},
		{"whole content when the limit is 0", content, 0, "FIRST line \nsecond", systemFilePresent},
		{"limit larger than the content", content, 100, "FIRST line \nsecond", systemFilePresent},
		{"empty file is absent", empty, 10, "", systemFileAbsent},
		{"whitespace-only file is absent", blank, 10, "", systemFileAbsent},
		{"missing file is absent", filepath.Join(dir, "missing"), 10, "", systemFileAbsent},
		{"missing parent is absent", filepath.Join(dir, "nodir", "file"), 10, "", systemFileAbsent},
		{"a directory is unreadable", sub, 10, "", systemFileUnreadable},
		{"relative symlink to a sibling is followed", sibling, 5, "FIRST", systemFilePresent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotValue, gotState := readFirstLine(tc.path, tc.limit)
			if gotValue != tc.wantValue || gotState != tc.wantState {
				t.Fatalf("readFirstLine(%s, %d) = (%q, %s), want (%q, %s)", tc.path, tc.limit, gotValue, gotState, tc.wantValue, tc.wantState)
			}
		})
	}
}

// A file the process may not read, and a file in a directory it may not search, are
// both a permission denial.
func TestReadFirstLinePermissionDeniedCharacterization(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.WriteFile(locked, []byte("secret\n"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, state := readFirstLine(locked, 10); got != "" || state != systemFileDenied {
		t.Errorf("unreadable file: readFirstLine = (%q, %s), want (\"\", denied)", got, state)
	}

	closed := filepath.Join(dir, "closed")
	if err := os.Mkdir(closed, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	inside := filepath.Join(closed, "value")
	if err := os.WriteFile(inside, []byte("value\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(closed, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	if got, state := readFirstLine(inside, 10); got != "" || state != systemFileDenied {
		t.Errorf("file in a non-searchable dir: readFirstLine = (%q, %s), want (\"\", denied)", got, state)
	}
}

// TODAY readFirstLine follows a symlink wherever it points: an absolute one, and a
// relative one that leaves its directory.
func TestReadFirstLineEscapingSymlinksCharacterization(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	inner := filepath.Join(dir, "inner")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	absLink := filepath.Join(inner, "abs-link")
	if err := os.Symlink(target, absLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	upLink := filepath.Join(inner, "up-link")
	if err := os.Symlink(filepath.Join("..", "target"), upLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	for _, link := range []string{absLink, upLink} {
		if got, state := readFirstLine(link, 0); got != "outside" || state != systemFilePresent {
			t.Errorf("readFirstLine(%s) = (%q, %s), want (\"outside\", present)", link, got, state)
		}
	}
}
