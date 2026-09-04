package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tis24dev/proxsave/internal/filelock"
)

const daemonLockFileName = ".daemon.lock"

var errDaemonLockHeld = errors.New("daemon ownership lock is already held")

func acquireDaemonLock(baseDir string) (release func(), err error) {
	dir := filepath.Join(baseDir, "identity")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create daemon lock directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, daemonLockFileName)
	releaseLock, err := filelock.TryAcquire(path)
	if errors.Is(err, filelock.ErrHeld) {
		return nil, fmt.Errorf("%w: %s", errDaemonLockHeld, path)
	}
	if err != nil {
		return nil, fmt.Errorf("acquire daemon lock %s: %w", path, err)
	}
	return func() {
		_ = releaseLock()
	}, nil
}
