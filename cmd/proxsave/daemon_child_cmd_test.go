package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The child is the binary on disk, checked by safeexec.TrustedCommandContext: a
// trusted path gives `<execPath> --backup --config <path>`.
func TestBuildBackupCmdRunsTheTrustedBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "proxsave")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	d := newTestDaemon(t, nil, nil, time.Hour)
	d.execPath, d.configPath = bin, "/opt/proxsave/configs/backup.env"
	cmd, err := d.buildBackupCmd(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("buildBackupCmd: %v", err)
	}
	want := []string{bin, "--backup", "--config", "/opt/proxsave/configs/backup.env"}
	if cmd.Path != bin || !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("cmd = %q %v, want %q %v", cmd.Path, cmd.Args, bin, want)
	}
}

// A path the check refuses builds no child: relative, missing, or world-writable.
func TestBuildBackupCmdRefusesAnUntrustedBinary(t *testing.T) {
	dir := t.TempDir()
	writable := filepath.Join(dir, "proxsave-ww")
	if err := os.WriteFile(writable, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	for _, path := range []string{"proxsave", filepath.Join(dir, "absent"), writable} {
		d := newTestDaemon(t, nil, nil, time.Hour)
		d.execPath = path
		if cmd, err := d.buildBackupCmd(context.Background(), nil, ""); err == nil || cmd != nil {
			t.Errorf("%s: buildBackupCmd = %v, %v; want a refusal", path, cmd, err)
		}
	}
}

// A refused binary ends the run like a cmd.Start failure: a finish ping with exit 1.
// The binary would run and exit 0: only the refusal makes the 1.
func TestRunOnceReportsARefusedBinaryLikeAStartFailure(t *testing.T) {
	writable := filepath.Join(t.TempDir(), "proxsave")
	if err := os.WriteFile(writable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	rep := &fakeReporter{backupURL: true}
	d := newTestDaemon(t, rep, nil, time.Hour)
	d.execPath = writable
	d.runOnce(context.Background())
	s := rep.snapshot()
	if s.started != 1 || s.finished != 1 || s.hung != 0 || s.lastCode != 1 {
		t.Fatalf("started=%d finished=%d hung=%d code=%d, want 1/1/0/1", s.started, s.finished, s.hung, s.lastCode)
	}
}
