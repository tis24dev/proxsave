package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// accessControlWarningMarker is the stable prefix of the line this check emits. The
// tests below assert on it rather than on WarningCount, because a full workflow run
// emits unrelated warnings that depend on real host state.
const accessControlWarningMarker = "Access control/TFA:"

// newAccessControlTestLogger returns a logger that actually records warnings, plus
// the buffer they land in. The package's newTestLogger() is LogLevelError, and
// logging.Logger drops a warning before it ever reaches the counter at that level,
// so a check that only warns would look silently correct.
func newAccessControlTestLogger() (*logging.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelWarning, false)
	logger.SetOutput(&buf)
	return logger, &buf
}

// TestAccessControlHostIsOurs pins the rule the access control warning uses: a
// backup is this machine's own when its hostname equals, once spelling is
// normalised, one of the names this machine answers to. Those names are what
// os.Hostname() reports (the kernel short name) and what this run resolved as its
// own write-side identity ("hostname -f", the same resolver that stamped the name
// into the archive).
//
// There is deliberately no fold to the first label. Reinstating one turns the four
// security rows below red, which is the point: folding silences this warning on
// exactly the cross-host restore it exists for, and that is when an admin can be
// locked out of the web UI.
func TestAccessControlHostIsOurs(t *testing.T) {
	tests := []struct {
		name        string
		backupHost  string
		currentHost string
		runHost     string
		want        bool
	}{
		// Both sides already agree.
		{name: "both sides equal and unqualified", backupHost: "pve", currentHost: "pve", runHost: "pve", want: true},
		{name: "both sides equal and qualified", backupHost: "pve.home.arpa", currentHost: "pve.home.arpa", runHost: "pve.home.arpa", want: true},

		// The false positive this rule exists to remove: a default Proxmox box whose
		// kernel name is short and whose archives carry the FQDN.
		{name: "the kernel short name and this run's own FQDN are one machine", backupHost: "pve.home.arpa", currentHost: "pve", runHost: "pve.home.arpa", want: true},
		{name: "the same pair with the spellings swapped", backupHost: "pve", currentHost: "pve.home.arpa", runHost: "pve", want: true},
		{name: "a trailing root dot and upper case are the same name", backupHost: "PVE.Home.Arpa.", currentHost: "pve", runHost: "pve.home.arpa", want: true},

		// SECURITY. Each of these is silenced by a fold to the first label, and each is
		// a genuinely cross-host access control restore.
		{name: "a foreign FQDN sharing our short name, hostname -f resolving", backupHost: "pve.siteb.example", currentHost: "pve", runHost: "pve.home.arpa", want: false},
		{name: "a foreign FQDN sharing our short name on a stock node whose hostname -f fails", backupHost: "pve.siteb.example", currentHost: "pve", runHost: "pve", want: false},
		{name: "a look-alike domain under an unqualified current host", backupHost: "localhost.attacker.example", currentHost: "localhost", runHost: "localhost", want: false},

		// Accepted degradations: this machine cannot confirm it answers to the other
		// spelling, so it warns. An extra advisory line, never a suppressed one. The
		// fold silences both of these, which is what it was for; the trade is taken
		// deliberately, because the same fold silences the four rows above it.
		{name: "our own older FQDN after hostname -f stopped resolving", backupHost: "pve.home.arpa", currentHost: "pve", runHost: "pve", want: false},
		{name: "our own short name when neither name we hold is short", backupHost: "pve", currentHost: "pve.home.arpa", runHost: "pve.home.arpa", want: false},

		// Unchanged from the behaviour before the run identity existed.
		{name: "two qualified names sharing a first label", backupHost: "pve.siteb.example", currentHost: "pve.sitea.example", runHost: "pve.sitea.example", want: false},
		{name: "a plain domain change", backupHost: "pve.old.example", currentHost: "pve.new.example", runHost: "pve.new.example", want: false},
		{name: "a different machine entirely", backupHost: "pbs", currentHost: "pve", runHost: "pve", want: false},

		// The unresolved sentinel is refused for the RUN slot only, so two machines
		// that both failed to resolve cannot become each other's identity. A machine
		// the kernel really does call "unknown" still recognises its own work.
		{name: "the unresolved sentinel never names a host", backupHost: "unknown", currentHost: "pve", runHost: "unknown", want: false},
		{name: "a machine the kernel really calls unknown keeps its own bundles", backupHost: "unknown", currentHost: "unknown", runHost: "unknown", want: true},

		// No run identity: exactly the strict rule this check had before, which warns
		// more, never less. This is what a deleted plumb degrades to.
		{name: "no run identity, matching", backupHost: "pve", currentHost: "pve", runHost: "", want: true},
		{name: "no run identity, case only", backupHost: "PVE", currentHost: "pve", runHost: "", want: true},
		{name: "no run identity, not matching", backupHost: "pve.home.arpa", currentHost: "pve", runHost: "", want: false},

		// Defensive rows: the caller returns before these, so they only pin that the
		// helper alone cannot be talked into claiming a nameless bundle.
		{name: "an empty backup host names nothing", backupHost: "", currentHost: "pve", runHost: "pve.home.arpa", want: false},
		{name: "a blank backup host names nothing", backupHost: "   ", currentHost: "pve", runHost: "pve", want: false},
		{name: "no name on either side of this machine", backupHost: "pve", currentHost: "", runHost: "", want: false},
		{name: "the run identity alone can still recognise our own bundle", backupHost: "pve.home.arpa", currentHost: "", runHost: "pve.home.arpa", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accessControlHostIsOurs(tt.backupHost, tt.currentHost, tt.runHost); got != tt.want {
				t.Fatalf("accessControlHostIsOurs(backup=%q, current=%q, run=%q) = %v, want %v",
					tt.backupHost, tt.currentHost, tt.runHost, got, tt.want)
			}
		})
	}
}

// TestWarnAccessControlHostnameMismatchUsesTheRunHostname pins that the METHOD
// actually consults the run identity. The table above cannot see this: passing ""
// at the call site leaves every one of its rows green.
//
// This reads the real os.Hostname() with no seam, because the method has none. The
// RFC 2606 .invalid names keep it machine-independent: they can never equal a real
// hostname, so the run identity is the ONLY way the bundle can be recognised as
// ours, which is what makes subtest 1 load-bearing. Two consequences worth knowing:
// swapping in a plausible name like "pve" would make this machine-dependent and
// silently non-load-bearing, and on a host where os.Hostname() errors or is blank
// the method returns early, so subtest 2 fails loudly rather than passing vacuously.
func TestWarnAccessControlHostnameMismatchUsesTheRunHostname(t *testing.T) {
	const (
		ownName     = "proxsave-selftest.invalid"
		foreignName = "proxsave-other.invalid"
	)

	newRun := func(logger *logging.Logger, backupHost, runHost string) *restoreUIWorkflowRun {
		return &restoreUIWorkflowRun{
			logger:       logger,
			runHostname:  runHost,
			plan:         &RestorePlan{NormalCategories: []Category{{ID: "pve_access_control"}}},
			decisionInfo: &RestoreDecisionInfo{BackupHostname: backupHost},
		}
	}

	t.Run("a bundle written under this run's own name is not warned about", func(t *testing.T) {
		logger, buf := newAccessControlTestLogger()
		newRun(logger, ownName, ownName).warnAccessControlHostnameMismatch()
		if strings.Contains(buf.String(), accessControlWarningMarker) {
			t.Fatalf("warned about this machine's own bundle; the run identity was not consulted\n%s", buf.String())
		}
	})

	t.Run("another machine is still warned about", func(t *testing.T) {
		logger, buf := newAccessControlTestLogger()
		newRun(logger, foreignName, ownName).warnAccessControlHostnameMismatch()
		if !strings.Contains(buf.String(), accessControlWarningMarker) {
			t.Fatalf("a cross-host access control restore must warn: WebAuthn credentials are bound to the UI origin\n%s", buf.String())
		}
	})
}

// TestNewRestoreUIWorkflowRunRecordsTheRunHostname is the constructor pin. The chain
// test below also covers it, but this turns "the constructor dropped the field" into
// a one-line message instead of a substring assertion deep inside a workflow run.
func TestNewRestoreUIWorkflowRunRecordsTheRunHostname(t *testing.T) {
	w := newRestoreUIWorkflowRun(context.Background(), &config.Config{}, newTestLogger(), "vtest", nil, "pve.home.arpa")
	if w.runHostname != "pve.home.arpa" {
		t.Fatalf("runHostname = %q, want pve.home.arpa", w.runHostname)
	}
}

// TestRunRestoreWorkflowWithUICarriesTheRunHostnameToTheAccessControlCheck drives the
// real engine, so it proves the run identity survives the whole hop from
// runRestoreWorkflowWithUI to the check and is genuinely READ at the far end, not
// merely stored. Passing "" at runRestoreWorkflowWithUI's own call to
// newRestoreUIWorkflowRun compiles and leaves the helper table entirely green; this
// is what catches it, and unlike the structural guard it survives a refactor of the
// chain.
//
// The run stops at the first confirmation (confirmRestore: false) with
// ErrRestoreAborted, which is after the check has already fired. The same .invalid
// naming caveat as the method pin applies.
func TestRunRestoreWorkflowWithUICarriesTheRunHostnameToTheAccessControlCheck(t *testing.T) {
	const (
		ownName     = "proxsave-selftest.invalid"
		foreignName = "proxsave-other.invalid"
	)

	cases := []struct {
		name        string
		backupHost  string
		wantWarning bool
	}{
		{name: "a bundle written under this run's own name is not warned about", backupHost: ownName, wantWarning: false},
		{name: "another machine is still warned about", backupHost: foreignName, wantWarning: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origRestoreSystem := restoreSystem
			origPrepare := prepareRestoreBundleFunc
			origAnalyze := analyzeRestoreArchiveFunc
			t.Cleanup(func() {
				restoreSystem = origRestoreSystem
				prepareRestoreBundleFunc = origPrepare
				analyzeRestoreArchiveFunc = origAnalyze
			})

			restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}
			prepareRestoreBundleFunc = stubPreparedRestoreBundle("/bundle.tar", &backup.Manifest{
				CreatedAt:     time.Unix(1700000000, 0),
				ProxmoxType:   "pve",
				ScriptVersion: "vtest",
			})
			analyzeRestoreArchiveFunc = func(archivePath string, logger *logging.Logger) ([]Category, *RestoreDecisionInfo, error) {
				return []Category{{ID: "pve_access_control", Name: "Access control", IsAvailable: true}},
					&RestoreDecisionInfo{BackupType: SystemTypePVE, BackupHostname: tc.backupHost}, nil
			}

			logger, buf := newAccessControlTestLogger()
			ui := &fakeRestoreWorkflowUI{mode: RestoreModeFull, confirmRestore: false}

			err := runRestoreWorkflowWithUI(context.Background(), &config.Config{BaseDir: "/base"}, logger, "vtest", ui, ownName)
			if !errors.Is(err, ErrRestoreAborted) {
				t.Fatalf("err = %v, want ErrRestoreAborted (the run must reach the confirmation, so the check has fired)", err)
			}

			got := strings.Contains(buf.String(), accessControlWarningMarker)
			if got != tc.wantWarning {
				t.Fatalf("access control warning present = %v, want %v; the run hostname did not reach the check\n%s",
					got, tc.wantWarning, buf.String())
			}
		})
	}
}
