package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The rollback scripts run under `set -eu` and write their log into RestoreRunDir,
// under /var/lib/proxsave. On a host where /var is its own filesystem and is full or
// read-only while / is still writable, the log cannot be written but the files the
// rollback restores can. A log that cannot be written must not stop the rollback.
//
// These tests run the REAL generated scripts with a fake tar first on PATH: it records
// that it was reached and exits 1, so nothing is extracted onto the live system and the
// prune phase, gated on a successful extract, never runs. The service commands the
// scripts call afterwards are faked too.

type rollbackScriptCase struct {
	name  string
	build func(markerPath, backupPath, logPath string) string
}

func rollbackScriptCases() []rollbackScriptCase {
	return []rollbackScriptCase{
		{"network", func(m, b, l string) string { return buildRollbackScript(m, b, l, false) }},
		{"firewall", buildFirewallRollbackScript},
		{"ha", buildHARollbackScript},
		{"access-control", buildAccessControlRollbackScript},
	}
}

// runRollbackScriptReachesExtract runs the script built by tc with logPath as its log
// and reports whether the extraction was attempted.
func runRollbackScriptReachesExtract(t *testing.T, tc rollbackScriptCase, dir, logPath string) bool {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	reached := filepath.Join(dir, "tar-reached")
	fakes := map[string]string{
		"tar":          "#!/bin/sh\n: > " + shellQuote(reached) + "\nexit 1\n",
		"systemctl":    "#!/bin/sh\nexit 0\n",
		"pve-firewall": "#!/bin/sh\nexit 0\n",
	}
	for name, body := range fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	markerPath := filepath.Join(dir, "marker")
	if err := os.WriteFile(markerPath, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "rollback.sh")
	script := tc.build(markerPath, filepath.Join(dir, "backup.tar.gz"), logPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_ = cmd.Run() // the fake tar fails on purpose; only whether it was reached matters

	_, err := os.Stat(reached)
	return err == nil
}

func TestRollbackScriptsRunWhenTheirLogCannotBeWritten(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	for _, tc := range rollbackScriptCases() {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// A log path under a regular file: opening it fails with ENOTDIR even for
			// root, which stands in for a full or read-only /var.
			notADir := filepath.Join(dir, "not-a-dir")
			if err := os.WriteFile(notADir, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if !runRollbackScriptReachesExtract(t, tc, dir, filepath.Join(notADir, "rollback.log")) {
				t.Fatalf("%s rollback stopped before its extraction because the log could not be written", tc.name)
			}
		})
	}
}

// Control: with a writable log the same scripts reach the extraction and write the log.
func TestRollbackScriptsRunWithAWritableLog(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	for _, tc := range rollbackScriptCases() {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "rollback.log")
			if !runRollbackScriptReachesExtract(t, tc, dir, logPath) {
				t.Fatalf("control broken: %s rollback did not reach its extraction with a writable log", tc.name)
			}
			info, err := os.Stat(logPath)
			if err != nil {
				t.Fatalf("%s rollback wrote no log: %v", tc.name, err)
			}
			if info.Size() == 0 {
				t.Fatalf("%s rollback log is empty", tc.name)
			}
		})
	}
}
