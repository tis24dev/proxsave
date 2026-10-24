package main

import (
	"path/filepath"
	"testing"
)

func forceDetectedBaseDirForTest(t *testing.T, baseDir string) {
	t.Helper()
	orig := getExecInfo

	execPath := filepath.Join(baseDir, "build", "proxsave")
	info := ExecInfo{
		ExecPath: execPath,
		ExecDir:  filepath.Dir(execPath),
		BaseDir:  baseDir,
		HasBase:  true,
	}
	getExecInfo = func() ExecInfo { return info }

	t.Cleanup(func() { getExecInfo = orig })
}
