package safefs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSystemErrorTextDropsThePathAndKeepsTheWording(t *testing.T) {
	pathErr := &fs.PathError{Op: "open", Path: "/mnt/backup/x.tar", Err: syscall.EACCES}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"bare path error", pathErr, "permission denied"},
		{"wrapped path error", fmt.Errorf("copy failed: %w", pathErr), "copy failed: permission denied"},
		{"timeout", &TimeoutError{Op: "stat", Path: "/mnt/backup", Timeout: 30 * time.Second}, "timed out after 30s"},
		{"wrapped timeout", fmt.Errorf("source file could not be read: %w", &TimeoutError{Op: "stat", Path: "/mnt/x", Timeout: time.Second}), "source file could not be read: timed out after 1s"},
		{"timeout over a minute, whole seconds", &TimeoutError{Op: "stat", Path: "/mnt/backup", Timeout: 120 * time.Second}, "timed out after 120s"},
		{"timeout with a fraction, rounded up", &TimeoutError{Op: "stat", Path: "/mnt/backup", Timeout: 1500 * time.Millisecond}, "timed out after 2s"},
		{"link error", &os.LinkError{Op: "rename", Old: "/a", New: "/b", Err: syscall.EXDEV}, "invalid cross-device link"},
		{"no path", errors.New("rclone command not found in PATH"), "rclone command not found in PATH"},
		// A wrapper that names the file carries the path too: that part goes.
		{"wrapper with a path", fmt.Errorf("failed to create temporary file in %s: %w", "/root/l1test/secondary/backup",
			&fs.PathError{Op: "createtemp", Path: "/root/l1test/secondary/backup/.tmp-x-*", Err: syscall.EROFS}), "read-only file system"},
		{"wrapper with two paths", fmt.Errorf("stream copy %s -> %s: %w", "/a/x", "/b/x", &fs.PathError{Op: "read", Path: "/a/x", Err: syscall.EIO}), "input/output error"},
		{"path in the middle", fmt.Errorf("path does not exist: %s: %w", "/mnt/x", &fs.PathError{Op: "stat", Path: "/mnt/x", Err: syscall.ENOENT}), "path does not exist: no such file or directory"},
		{"wrapper with a path around a timeout", fmt.Errorf("failed to open source file %s: %w", "/mnt/x", &TimeoutError{Op: "open", Path: "/mnt/x", Timeout: 120 * time.Second}), "timed out after 120s"},
		{"every part holds a path", errors.New("failed to detect filesystem type for /mnt/x: filesystem type not found in /proc/mounts"), "filesystem type not found in /proc/mounts"},
		{"a slash inside a word is no path", errors.New("read: input/output error"), "read: input/output error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SystemErrorText(tc.err); got != tc.want {
				t.Fatalf("SystemErrorText = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every timeout fact of the run prints whole seconds, a fraction rounded up.
func TestWholeSeconds(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{-time.Second, "0s"},
		{20 * time.Millisecond, "1s"},
		{time.Second, "1s"},
		{1001 * time.Millisecond, "2s"},
		{120 * time.Second, "120s"},
		{300 * time.Second, "300s"},
		{time.Hour, "3600s"},
	} {
		if got := WholeSeconds(tc.d); got != tc.want {
			t.Fatalf("WholeSeconds(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
