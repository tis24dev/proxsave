package storage

import (
	"errors"
	"testing"
)

// 2179b28 made a StorageError nested in another stop repeating its location and
// path - but disabling the guard in causeText left internal/storage green. These
// pin both sides of the boundary.
func TestNestedStorageErrorCollapsesWithinOneBackend(t *testing.T) {
	inner := &StorageError{Location: LocationSecondary, Operation: "list", Path: "/mnt/nas", Recoverable: true, Err: errors.New("i/o timeout")}
	outer := &StorageError{Location: LocationSecondary, Operation: "apply_retention", Path: "/mnt/nas", Recoverable: true, Err: inner}
	want := "secondary storage apply_retention operation failed for /mnt/nas (recoverable): list: i/o timeout"
	if got := outer.Error(); got != want {
		t.Fatalf("nested same-backend error:\n got %q\nwant %q", got, want)
	}
}

func TestNestedStorageErrorKeepsADifferentBackendWhole(t *testing.T) {
	inner := &StorageError{Location: LocationCloud, Operation: "list", Path: "remote:", Recoverable: true, Err: errors.New("rclone timeout")}
	outer := &StorageError{Location: LocationSecondary, Operation: "apply_retention", Path: "/mnt/nas", Recoverable: true, Err: inner}
	want := "secondary storage apply_retention operation failed for /mnt/nas (recoverable): cloud storage list operation failed for remote: (recoverable): rclone timeout"
	if got := outer.Error(); got != want {
		t.Fatalf("different backend must keep its full sentence:\n got %q\nwant %q", got, want)
	}
}
