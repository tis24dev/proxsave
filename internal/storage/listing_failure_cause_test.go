package storage

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// The unreadable-archives block prints "  - <name>: <cause>". listingFailureCause
// exists to strip the path out of a PathError so the cause does not repeat the name
// the item line already carries. Returning err.Error() instead left every package
// green; this is the missing test.
func TestListingFailureCauseStripsThePathFromAPathError(t *testing.T) {
	err := &fs.PathError{Op: "lstat", Path: "/mnt/nas/pve-backup-20260101.tar.zst", Err: errors.New("too many levels of symbolic links")}
	got := listingFailureCause(err)
	if got != "too many levels of symbolic links" {
		t.Fatalf("cause %q: the item line already names the file, the cause must not repeat it", got)
	}
	if strings.Contains(got, "/mnt/nas") || strings.Contains(got, "lstat") {
		t.Fatalf("cause %q still carries the path or the op", got)
	}
	// A non-PathError passes through whole: there is nothing to strip.
	if got := listingFailureCause(errors.New("i/o timeout")); got != "i/o timeout" {
		t.Fatalf("plain error mangled: %q", got)
	}
}
