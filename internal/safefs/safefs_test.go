package safefs

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestStat_ReturnsTimeoutError(t *testing.T) {
	prev := osStat
	defer func() { osStat = prev }()

	osStat = func(string) (os.FileInfo, error) {
		select {}
	}

	start := time.Now()
	_, err := Stat(context.Background(), "/does/not/matter", 25*time.Millisecond)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Stat err = %v; want timeout", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("Stat took too long: %s", time.Since(start))
	}
}

func TestReadDir_ReturnsTimeoutError(t *testing.T) {
	prev := osReadDir
	defer func() { osReadDir = prev }()

	osReadDir = func(string) ([]os.DirEntry, error) {
		select {}
	}

	start := time.Now()
	_, err := ReadDir(context.Background(), "/does/not/matter", 25*time.Millisecond)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("ReadDir err = %v; want timeout", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("ReadDir took too long: %s", time.Since(start))
	}
}

func TestStatfs_ReturnsTimeoutError(t *testing.T) {
	prev := syscallStatfs
	defer func() { syscallStatfs = prev }()

	syscallStatfs = func(string, *syscall.Statfs_t) error {
		select {}
	}

	start := time.Now()
	_, err := Statfs(context.Background(), "/does/not/matter", 25*time.Millisecond)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Statfs err = %v; want timeout", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("Statfs took too long: %s", time.Since(start))
	}
}

func TestStat_PropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Stat(ctx, "/does/not/matter", 50*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stat err = %v; want context.Canceled", err)
	}
}
