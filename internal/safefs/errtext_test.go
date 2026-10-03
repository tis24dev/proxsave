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
