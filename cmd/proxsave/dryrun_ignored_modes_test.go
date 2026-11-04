package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Six modes returned before the runtime, where --dry-run is honoured, and wrote for
// real: --newkey replaced the recipient, --new-install deleted backup/, configs/ and
// log/, --upgrade-config and --upgrade-config-json rewrote backup.env. Five are now
// refused; --upgrade-config --dry-run runs the preview that already exists.
func TestDryRunIsRefusedWithTheModesThatIgnoredIt(t *testing.T) {
	for _, tc := range []struct {
		args cli.Args
		want string
	}{
		{cli.Args{Install: true, DryRun: true}, "--dry-run is not supported with --install or --new-install: the installer always writes the installation."},
		{cli.Args{NewInstall: true, DryRun: true}, "--dry-run is not supported with --install or --new-install: the installer always writes the installation."},
		{cli.Args{ForceNewKey: true, DryRun: true}, "--dry-run is not supported with --newkey: it always replaces the encryption recipient."},
		{cli.Args{Decrypt: true, DryRun: true}, "--dry-run is not supported with --decrypt: it always writes the decrypted archive."},
		{cli.Args{UpgradeConfigJSON: true, DryRun: true}, "--dry-run is not supported with --upgrade-config-json: use --upgrade-config-dry-run to preview the changes."},
	} {
		messages := validateModeCompatibility(&tc.args)
		if len(messages) != 1 || messages[0] != tc.want {
			t.Errorf("%+v: messages %q, want [%q]", tc.args, messages, tc.want)
		}
	}
	for _, args := range []cli.Args{
		{Install: true}, {NewInstall: true}, {ForceNewKey: true}, {Decrypt: true}, {UpgradeConfigJSON: true},
		{UpgradeConfig: true, DryRun: true},
	} {
		for _, m := range validateModeCompatibility(&args) {
			if strings.Contains(m, "--dry-run is not supported") {
				t.Errorf("a dry-run guard fired on %+v: %s", args, m)
			}
		}
	}
}

func TestUpgradeConfigWithDryRunRunsThePreview(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "backup.env")
	before := []byte("BACKUP_PATH=/var/lib/proxsave/backup\n")
	if err := os.WriteFile(configPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetConsoleQuiet(true)
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(&out)
	bootstrap.SetMirrorLogger(mirror)

	args := &cli.Args{UpgradeConfig: true, DryRun: true, ConfigPath: configPath}
	code, handled := runUpgradeConfigDryMode(context.Background(), args, bootstrap, "")
	if !handled || code != types.ExitSuccess.Int() {
		t.Fatalf("handled=%v code=%d, want the preview to run; log:\n%s", handled, code, out.String())
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("backup.env changed under --dry-run:\n%s", after)
	}
	if !strings.Contains(out.String(), "Dry run only: no files were modified.") {
		t.Fatalf("no preview line; log:\n%s", out.String())
	}
}
