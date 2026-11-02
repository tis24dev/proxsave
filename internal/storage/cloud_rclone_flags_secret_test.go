package storage

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A backend credential in RCLONE_FLAGS was written in plain text to the run log: the
// rclone argv is logged at DEBUG by the filesystem check, the copy, the verification and
// the delete, and rclone -vv echoes its argv into the stderr that errors carry. The
// values of RCLONE_FLAGS are now masked in every line, in both the --flag=value and the
// --flag value form.
func TestCloudStorageMasksRcloneFlagValuesInTheLog(t *testing.T) {
	const keyValue = "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	const passValue = "Sep4r4teT0kenPassw0rdXYZ"
	cfg := &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "s3remote:bucket",
		CloudRemotePath:         "proxsave",
		RcloneTimeoutConnection: 30,
		RcloneFlags:             strings.Fields("--checkers=4 --s3-secret-access-key=" + keyValue + " --webdav-pass " + passValue),
	}

	var out bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&out)

	cs, err := NewCloudStorage(cfg, logger, "pve1")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	cs.lookPath = func(string) (string, error) { return "/usr/bin/rclone", nil }
	argvCarriesValues := false
	cs.runCommand = func(_ context.Context, _ string, args ...string) (rcloneOutput, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, keyValue) && strings.Contains(joined, passValue) {
			argvCarriesValues = true
		}
		switch args[0] {
		case "lsl":
			return rcloneOutput{stdout: []byte("1234 2026-10-26 10:00:00.000000000 a.tar.zst\n")}, nil
		case "hashsum":
			return rcloneOutput{stdout: []byte(strings.Repeat("a", 64) + "  a.tar.zst\n")}, nil
		}
		return rcloneOutput{}, nil
	}

	ctx := context.Background()
	if _, err := cs.DetectFilesystem(ctx); err != nil {
		t.Fatalf("DetectFilesystem: %v", err)
	}
	if err := cs.rcloneCopy(ctx, "/var/lib/proxsave/backup/a.tar.zst", cs.remotePathFor("a.tar.zst")); err != nil {
		t.Fatalf("rcloneCopy: %v", err)
	}
	if _, err := cs.verifyPrimary(ctx, cs.remotePathFor("a.tar.zst"), 1234, "a.tar.zst"); err != nil {
		t.Fatalf("verifyPrimary: %v", err)
	}
	// What rclone -vv writes to stderr, carried by an error into a WARNING.
	echo := errors.New(`DEBUG : rclone: Version "v1.60.1-DEV" starting with parameters ["rclone" "copyto" "--checkers=4" "--s3-secret-access-key=` + keyValue + `" "--webdav-pass" "` + passValue + `"]`)
	logger.Warning("Cloud Storage: upload failed: %v", echo)

	if !argvCarriesValues {
		t.Fatalf("precondition: the RCLONE_FLAGS values never reached the rclone argv")
	}
	log := out.String()
	for _, secret := range []string{keyValue, passValue} {
		if strings.Contains(log, secret) {
			t.Fatalf("RCLONE_FLAGS value %q written in plain text; log:\n%s", secret, log)
		}
	}
	for _, want := range []string{
		"--s3-secret-access-key=" + logging.MaskSecret(keyValue),
		"--webdav-pass " + logging.MaskSecret(passValue),
		"--checkers=4",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("log lacks %q; log:\n%s", want, log)
		}
	}
}
