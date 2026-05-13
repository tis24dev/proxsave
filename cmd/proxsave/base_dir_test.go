package main

import (
	"path/filepath"
	"sync"
	"testing"
)

func forceDetectedBaseDirForTest(t *testing.T, baseDir string) {
	t.Helper()
	origInfo := execInfo
	origOnce := execInfoOnce

	execPath := filepath.Join(baseDir, "build", "proxsave")
	execInfo = ExecInfo{
		ExecPath: execPath,
		ExecDir:  filepath.Dir(execPath),
		BaseDir:  baseDir,
		HasBase:  true,
	}
	execInfoOnce = sync.Once{}
	execInfoOnce.Do(func() {})

	t.Cleanup(func() {
		execInfo = origInfo
		execInfoOnce = origOnce
	})
}
