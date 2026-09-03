package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const daemonLockFileName = ".daemon.lock"

var errDaemonLockHeld = errors.New("daemon ownership lock is already held")

func acquireDaemonLock(baseDir string) (release func(), err error) {
	dir := filepath.Join(baseDir, "identity")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create daemon lock directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, daemonLockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", errDaemonLockHeld, path)
		}
		return nil, fmt.Errorf("acquire daemon lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
