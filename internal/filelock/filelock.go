// Package filelock provides process-scoped advisory file locks.
package filelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrHeld reports that a non-blocking lock acquisition found another owner.
var ErrHeld = errors.New("file lock is already held")

// Acquire obtains an exclusive lock, waiting until any current owner releases it.
func Acquire(path string) (release func() error, err error) {
	return acquire(path, syscall.LOCK_EX)
}

// TryAcquire obtains an exclusive lock without waiting for another owner.
func TryAcquire(path string) (release func() error, err error) {
	return acquire(path, syscall.LOCK_EX|syscall.LOCK_NB)
}

func acquire(path string, flags int) (func() error, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("file lock path is empty")
	}

	path = filepath.Clean(path)
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open file lock directory for %s: %w", path, err)
	}

	file, err := root.OpenFile(filepath.Base(path), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open file lock %s: %w", path, err),
			closeError("close file lock directory", root),
		)
	}

	if err := syscall.Flock(int(file.Fd()), flags); err != nil {
		lockErr := fmt.Errorf("acquire file lock %s: %w", path, err)
		if flags&syscall.LOCK_NB != 0 &&
			(errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)) {
			lockErr = errors.Join(ErrHeld, lockErr)
		}
		return nil, errors.Join(
			lockErr,
			closeError("close file lock", file),
			closeError("close file lock directory", root),
		)
	}

	return func() error {
		return errors.Join(
			flockUnlockError(path, file),
			closeError("close file lock", file),
			closeError("close file lock directory", root),
		)
	}, nil
}

func flockUnlockError(path string, file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("release file lock %s: %w", path, err)
	}
	return nil
}

type closer interface {
	Close() error
}

func closeError(action string, resource closer) error {
	if err := resource.Close(); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}
