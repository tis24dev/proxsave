package main

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/installer"
)

// templateCloudRemoteExample is the value the shipped template seeds CLOUD_REMOTE with.
// It is read out of the template rather than written here so the test keeps pointing at
// the real example if it is ever reworded, instead of silently passing against a literal
// nobody ships any more.
func templateCloudRemoteExample(t *testing.T) string {
	t.Helper()
	example := strings.TrimSpace(installer.DeriveInstallWizardPrefill(config.DefaultEnvTemplate()).CloudRemote)
	if example == "" {
		t.Skip("the shipped template no longer seeds CLOUD_REMOTE; this trap cannot fire")
	}
	return example
}

// TestFreshInstallDoesNotOfferTheTemplateCloudRemote pins the fix for a defect that made
// pressing Enter enough to finish an install pointed at a remote that does not exist.
//
// Off the Edit path prepareBaseTemplate hands the wizard the SHIPPED template, so every
// value the prefill reads back is a template line rather than an operator's choice. Most
// of those are blank or a usable default, but CLOUD_REMOTE ships as an EXAMPLE remote
// NAME. Offering it as the prompt default meant an operator who accepted the defaults got
// CLOUD_ENABLED=true against a remote rclone has never heard of — and because a cloud
// upload failure is deliberately non-critical, that surfaces as a warning on every run
// from then on rather than as a failure at install time.
//
// The prompt must therefore REQUIRE an answer here. schedulerEngineDefault,
// healthcheckModeDefault and cronTimeDefault already apply the same rule to their keys.
func TestFreshInstallDoesNotOfferTheTemplateCloudRemote(t *testing.T) {
	example := templateCloudRemoteExample(t)

	// Enable cloud, then answer the two rclone prompts. Everything after is defaulted.
	script := strings.Join([]string{
		"n",                    // secondary
		"y",                    // cloud
		"myremote:pbs-backups", // remote (now REQUIRED: no default is offered)
		"myremote:/logs",       // log remote
		"n",                    // firewall
		"n",                    // telegram
		"n",                    // email
		"n",                    // encryption
		"",                     // scheduler engine: default
		"off",                  // healthchecks
		"",                     // run at: default
	}, "\n") + "\n"

	var data *installer.InstallWizardData
	var err error
	transcript := captureStdout(t, func() {
		reader := bufio.NewReader(strings.NewReader(script))
		data, err = collectInstallWizardDataCLI(context.Background(), reader, config.DefaultEnvTemplate(), false, nil)
	})
	if err != nil {
		t.Fatalf("collectInstallWizardDataCLI error: %v", err)
	}
	if strings.Contains(transcript, example) {
		t.Fatalf("a fresh install offered the template's example remote %q as a default:\n%s", example, transcript)
	}
	if strings.Contains(transcript, "["+example+"]") {
		t.Fatalf("the example remote is still rendered as a prompt default:\n%s", transcript)
	}
	// Guard against a vacuous pass: the cloud branch must really have run, and the
	// typed answer — not a default — must be what reached the payload.
	if !data.EnableCloudStorage {
		t.Fatal("the cloud branch did not run, so nothing above measured the prompt")
	}
	if data.RcloneBackupRemote != "myremote:pbs-backups" {
		t.Fatalf("the typed remote must reach the payload, got %q", data.RcloneBackupRemote)
	}
	// The log path default is deliberately NOT dropped: it is a real path inside
	// whatever remote is chosen, which the template's own comment lists as an accepted
	// form. Losing it here would be an unrelated regression.
	if !strings.Contains(transcript, "Rclone remote for logs") {
		t.Fatalf("the log prompt is missing entirely:\n%s", transcript)
	}
}

// TestEditStillOffersTheStoredCloudRemote is the other half of the contract: dropping the
// default must be scoped to the path where the "stored" value is really a template line.
// On an Edit the value belongs to the operator, and a no-op edit has to keep it — that is
// the whole point of prefilling.
func TestEditStillOffersTheStoredCloudRemote(t *testing.T) {
	stored := "operator-remote:archive"
	base := "CLOUD_ENABLED=true\nCLOUD_REMOTE=" + stored + "\nCLOUD_LOG_PATH=/proxsave/log\n"

	script := strings.Join([]string{
		"n", // secondary
		"",  // cloud: keep enabled (prefilled)
		"",  // remote: accept the STORED value
		"",  // log remote: accept the stored value
		"n", // firewall
		"n", // telegram
		"n", // email
		"n", // encryption
		"",  // scheduler engine
		"off",
		"", // run at
	}, "\n") + "\n"

	var data *installer.InstallWizardData
	var err error
	transcript := captureStdout(t, func() {
		reader := bufio.NewReader(strings.NewReader(script))
		data, err = collectInstallWizardDataCLI(context.Background(), reader, base, true, nil)
	})
	if err != nil {
		t.Fatalf("collectInstallWizardDataCLI error: %v", err)
	}
	if !strings.Contains(transcript, "["+stored+"]") {
		t.Fatalf("an edit must still offer the stored remote as the default:\n%s", transcript)
	}
	if data.RcloneBackupRemote != stored {
		t.Fatalf("a no-op edit must keep the stored remote, got %q", data.RcloneBackupRemote)
	}
}
