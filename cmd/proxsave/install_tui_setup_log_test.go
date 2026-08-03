package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// captureBootstrapLog returns a bootstrap logger whose lines are readable, mirroring
// the persisted install log: the mirror is exactly the sink startFlowSessionLog
// installs in production, so what lands in the buffer here is what lands on disk there.
func captureBootstrapLog(t *testing.T) (*logging.BootstrapLogger, *bytes.Buffer) {
	t.Helper()
	bootstrap := logging.NewBootstrapLogger()
	buf := &bytes.Buffer{}
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(buf)
	bootstrap.SetMirrorLogger(mirror)
	return bootstrap, buf
}

// TestLogHealthcheckSetupOutcomeRecordsWhyTheStepWasSkipped is the regression proper.
// The TUI install driver never called logHealthcheckSetupBootstrapOutcome, so every
// reason the step can decline to run — no alive URL yet, unreadable config, no server
// identity, self mode — was absent from a TUI install log while the CLI recorded it.
// The step then looked like it had simply never happened, and the reason was gone for
// good, since nothing re-derives it after the install.
func TestLogHealthcheckSetupOutcomeRecordsWhyTheStepWasSkipped(t *testing.T) {
	cases := []struct {
		name        string
		eligibility orchestrator.HealthcheckSetupEligibility
		want        string
	}{
		{"self mode without a URL", orchestrator.HealthcheckSetupSkipSelfMode, "no alive URL configured yet"},
		{"self mode", orchestrator.HealthcheckSetupEligibleSelf, "self mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bootstrap, buf := captureBootstrapLog(t)
			res := installer.HealthcheckSetupResult{
				HealthcheckSetupBootstrap: orchestrator.HealthcheckSetupBootstrap{Eligibility: tc.eligibility},
			}
			logHealthcheckSetupOutcome(bootstrap, res, nil)
			if !strings.Contains(buf.String(), tc.want) {
				t.Fatalf("the install log must say why the healthcheck step did not run; want %q, got %q", tc.want, buf.String())
			}
		})
	}

	// A config that cannot be read is the case where the log is the ONLY evidence: the
	// step is skipped and the error is not surfaced anywhere else on the TUI path.
	t.Run("unreadable config", func(t *testing.T) {
		bootstrap, buf := captureBootstrapLog(t)
		logHealthcheckSetupOutcome(bootstrap, installer.HealthcheckSetupResult{
			HealthcheckSetupBootstrap: orchestrator.HealthcheckSetupBootstrap{
				Eligibility: orchestrator.HealthcheckSetupSkipConfigError,
				ConfigError: "permission denied reading backup.env",
			},
		}, nil)
		if !strings.Contains(buf.String(), "permission denied reading backup.env") {
			t.Fatalf("the config error must reach the install log, got %q", buf.String())
		}
	})
}

// TestLogHealthcheckSetupOutcomeStaysSilentOnBootstrapFailure pins the asymmetry with
// the Telegram twin, which the driver carried before this refactor: on error the
// healthcheck step gets a warning and NOTHING else, while Telegram still records its
// partial verdict.
//
// The result passed here is deliberately NOT the zero value. RunHealthcheckSetup
// happens to return a zero result on every error path today, which makes the choice
// invisible in production and unpinnable with a realistic fixture — a zero result
// carries Shown=false and exits on its own. Feeding a populated result is what makes
// the rule itself testable, so a later change that starts returning partial results on
// error cannot silently flip this behaviour.
func TestLogHealthcheckSetupOutcomeStaysSilentOnBootstrapFailure(t *testing.T) {
	bootstrap, buf := captureBootstrapLog(t)
	logHealthcheckSetupOutcome(bootstrap, installer.HealthcheckSetupResult{
		HealthcheckSetupBootstrap: orchestrator.HealthcheckSetupBootstrap{
			Eligibility: orchestrator.HealthcheckSetupSkipSelfMode,
		},
		Shown: true, CheckAttempts: 2,
	}, errors.New("boom"))

	out := buf.String()
	if !strings.Contains(out, "Healthcheck setup failed (non-blocking): boom") {
		t.Fatalf("the failure must be recorded, got %q", out)
	}
	if strings.Contains(out, "not verified") {
		t.Fatalf("a failed step must not get a verdict line, got %q", out)
	}
	if strings.Contains(out, "no alive URL configured yet") {
		t.Fatalf("a failed step must not get an eligibility diagnosis either, got %q", out)
	}
}

// TestLogTelegramSetupOutcomeScrubsTheRelayMessage: the relay's own words go into the
// persisted install log, and that log is read back with cat/less later. Terminal
// escapes surviving into it let a hostile relay response drive the reader's terminal.
// The CLI has scrubbed here from the start; the TUI logged the raw bytes.
func TestLogTelegramSetupOutcomeScrubsTheRelayMessage(t *testing.T) {
	bootstrap, buf := captureBootstrapLog(t)
	logTelegramSetupOutcome(bootstrap, installer.TelegramSetupResult{
		Shown:             true,
		CheckAttempts:     3,
		LastStatusCode:    409,
		LastStatusMessage: "\x1b[2Jnot linked\x07",
	}, nil)

	out := buf.String()
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("the install log must not carry the relay's control bytes: %q", out)
	}
	if !strings.Contains(out, "Telegram setup: not verified (attempts=3 last=409 not linked)") {
		t.Fatalf("the scrubbed relay message must still reach the log: %q", out)
	}
}

// TestLogTelegramSetupOutcomeStandsInForASilentRelay: without the stand-in the line
// ends on a bare status code, which reads as a truncated log rather than a relay that
// said nothing. Same stand-in the CLI writes, so the two install logs match.
func TestLogTelegramSetupOutcomeStandsInForASilentRelay(t *testing.T) {
	bootstrap, buf := captureBootstrapLog(t)
	logTelegramSetupOutcome(bootstrap, installer.TelegramSetupResult{
		Shown: true, CheckAttempts: 1, LastStatusCode: 500, LastStatusMessage: "",
	}, nil)
	want := "Telegram setup: not verified (attempts=1 last=500 " + orchestrator.TelegramSetupStatusUnknownMessage + ")"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("want %q in the log, got %q", want, buf.String())
	}
}

// TestLogTelegramSetupOutcomeKeepsThePartialVerdictOnFailure pins the asymmetry with
// the healthcheck twin: RunTelegramSetup returns what it collected before failing, so
// the verdict is real and worth recording — only the eligibility diagnosis, which a
// failed run may never have produced, is dropped.
func TestLogTelegramSetupOutcomeKeepsThePartialVerdictOnFailure(t *testing.T) {
	bootstrap, buf := captureBootstrapLog(t)
	logTelegramSetupOutcome(bootstrap, installer.TelegramSetupResult{
		TelegramSetupBootstrap: orchestrator.TelegramSetupBootstrap{
			Eligibility: orchestrator.TelegramSetupSkipPersonalMode,
		},
		Shown: true, SkippedVerification: true,
	}, errors.New("session died"))

	out := buf.String()
	if !strings.Contains(out, "Telegram setup failed (non-blocking): session died") {
		t.Fatalf("the failure must be recorded, got %q", out)
	}
	if !strings.Contains(out, "Telegram setup: verification skipped by user") {
		t.Fatalf("the partial verdict must survive the failure, got %q", out)
	}
	if strings.Contains(out, "personal mode selected") {
		t.Fatalf("the eligibility diagnosis must be dropped on failure, got %q", out)
	}
}

// TestSetupOutcomeLoggersTolerateANilBootstrap: the driver passes the bootstrap logger
// straight through and it is nil on paths that keep no install log. Both used to be
// guarded by an `if bootstrap != nil` at every call site; the guard now lives inside.
func TestSetupOutcomeLoggersTolerateANilBootstrap(t *testing.T) {
	logTelegramSetupOutcome(nil, installer.TelegramSetupResult{Shown: true}, errors.New("boom"))
	logHealthcheckSetupOutcome(nil, installer.HealthcheckSetupResult{Shown: true}, errors.New("boom"))
}

// TestInstallTUIDriverLogsBothSetupOutcomes pins the invariant that was actually
// broken, which none of the tests above can reach: the driver called the Telegram
// logger and simply did not call the healthcheck one. Both were open-coded blocks
// then, so the omission was invisible; they are one call each now, but a call is
// still something a future edit can drop, and dropping it is silent — the install
// succeeds, only the log is poorer, and no run fails.
//
// This is a STRUCTURAL test and it knows it: it asserts the driver contains the two
// calls, not what they emit (that is the rest of this file). Driving runInstallTUI
// itself would mean a session, a written config and a relay stub for one bit of
// information. If the driver is ever restructured so these calls move or are made
// through a seam, replace this test rather than deleting it.
func TestInstallTUIDriverLogsBothSetupOutcomes(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "install_tui.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing the install driver: %v", err)
	}

	var driver *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "runInstallTUI" {
			driver = fn
			return false
		}
		return true
	})
	if driver == nil {
		t.Fatal("runInstallTUI not found in install_tui.go")
	}

	called := map[string]bool{}
	ast.Inspect(driver, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok {
			called[ident.Name] = true
		}
		return true
	})

	for _, name := range []string{"logTelegramSetupOutcome", "logHealthcheckSetupOutcome"} {
		if !called[name] {
			t.Errorf("runInstallTUI does not call %s, so that step's diagnosis never reaches the install log", name)
		}
	}
}
