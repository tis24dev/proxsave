package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

var (
	healthcheckSetupBuildBootstrap = orchestrator.BuildHealthcheckSetupBootstrap
	healthcheckSetupCheck          = orchestrator.CheckHealthcheckConnection
	healthcheckSetupSelfCheck      = orchestrator.CheckHealthcheckSelfConnection
	healthcheckSetupPromptYesNo    = promptYesNo
)

func logHealthcheckSetupBootstrapOutcome(bootstrap *logging.BootstrapLogger, state orchestrator.HealthcheckSetupBootstrap) {
	switch state.Eligibility {
	case orchestrator.HealthcheckSetupSkipConfigError:
		if strings.TrimSpace(state.ConfigError) != "" {
			logBootstrapWarning(bootstrap, "Healthcheck setup: unable to load config (skipping): %s", state.ConfigError)
		}
	case orchestrator.HealthcheckSetupEligibleSelf:
		logBootstrapInfo(bootstrap, "Healthcheck setup: self mode (reachability check of your own alive URL)")
	case orchestrator.HealthcheckSetupSkipSelfMode:
		logBootstrapInfo(bootstrap, "Healthcheck setup: self mode but no alive URL configured yet (skipping)")
	case orchestrator.HealthcheckSetupSkipIdentityUnavailable:
		logBootstrapWarning(bootstrap, "Healthcheck setup: identity/relay secret unavailable; skipping (pair Telegram to enable centralized monitoring)")
	}
	// SkipDisabled (cron mode) is a silent skip, mirroring Telegram when disabled.
}

// runHealthcheckSetupCLI shows the install-time healthchecks guide/check screen
// (mirrors runTelegramSetupCLI): a short guide, the portal magic-link, and a
// non-blocking connection check with retry/skip. Only renders when the daemon
// engine with centralized monitoring was chosen and the identity/secret exist.
func runHealthcheckSetupCLI(ctx context.Context, reader *bufio.Reader, baseDir, configPath string, bootstrap *logging.BootstrapLogger) error {
	state, err := healthcheckSetupBuildBootstrap(configPath, baseDir)
	if err != nil {
		logBootstrapWarning(bootstrap, "Healthcheck setup bootstrap failed (non-blocking): %v", err)
		return nil
	}

	logHealthcheckSetupBootstrapOutcome(bootstrap, state)
	if state.Eligibility != orchestrator.HealthcheckSetupEligibleCentralized &&
		state.Eligibility != orchestrator.HealthcheckSetupEligibleSelf {
		return nil
	}
	selfMode := state.Eligibility == orchestrator.HealthcheckSetupEligibleSelf

	fmt.Println("\n--- Backup monitoring (healthchecks) ---")
	if selfMode {
		fmt.Println("Self mode: the daemon reports to YOUR own healthchecks server using the ping")
		fmt.Println("URLs you entered. The check below verifies the alive URL is reachable from here.")
	} else {
		fmt.Println("The daemon reports each backup outcome + a liveness heartbeat to healthchecks,")
		fmt.Println("so a silent failure (crash, hang, host down) is caught by an external monitor.")
		fmt.Println("A personal monitoring portal has been provisioned for this host.")
	}
	fmt.Println()

	check, err := healthcheckSetupPromptYesNo(ctx, reader, "Check the monitoring connection now? [Y/n]: ", true)
	if err != nil {
		return skipOptionalInstallStepOnAbort(bootstrap, "Healthcheck setup", err)
	}
	if !check {
		fmt.Println("Skipped. The daemon connects to the monitor on its first run.")
		logBootstrapInfo(bootstrap, "Healthcheck setup: check skipped by user")
		return nil
	}

	attempts := 0
	for {
		attempts++
		var st orchestrator.HealthcheckSetupState
		if selfMode {
			// Self mode: pure reachability of the user's own alive URL; no server
			// fetch, no magic-link (LoginURL stays empty so the box is skipped).
			res := healthcheckSetupSelfCheck(ctx, state.HealthcheckAliveURL)
			st = orchestrator.ClassifyHealthcheckSelfResult(res)
		} else {
			res := healthcheckSetupCheck(ctx, state.ServerAPIHost, state.ServerID, baseDir, state.HealthcheckHeartbeatInterval)
			st = orchestrator.ClassifyHealthcheckSetupResult(res)
		}

		// Show the portal magic-link whenever the server minted one - even if the
		// reachability ping then failed, the user can still open the portal.
		if link := strings.TrimSpace(st.LoginURL); link != "" {
			fmt.Println()
			fmt.Println("Your monitoring portal (single-use link, valid ~1h):")
			fmt.Printf("  %s\n", link)
			fmt.Println("Open it to set a password and configure alert channels (email, etc.).")
			fmt.Println()
		}

		printHealthcheckSetupStatus(st)

		if st.Verified {
			logBootstrapInfo(bootstrap, "Healthcheck setup: connection verified (attempts=%d, state=%s)", attempts, st.Keyword)
			return nil
		}

		if st.Fatal { // re-checking cannot help: do NOT offer another check
			logBootstrapInfo(bootstrap, "Healthcheck setup: not verified (attempts=%d, fatal)", attempts)
			return nil
		}
		if attempts >= orchestrator.HealthcheckSetupMaxVerificationAttempts {
			fmt.Println(orchestrator.HealthcheckSetupMaxAttemptsHint)
			logBootstrapInfo(bootstrap, "Healthcheck setup: not verified (attempts=%d, max)", attempts)
			return nil
		}

		fmt.Println(orchestrator.HealthcheckSetupRetryHint)
		retry, err := healthcheckSetupPromptYesNo(ctx, reader, "Check again? [y/N]: ", false)
		if err != nil {
			return skipOptionalInstallStepOnAbort(bootstrap, "Healthcheck setup", err)
		}
		if !retry {
			fmt.Println("Not verified. The daemon will keep trying on its runs.")
			logBootstrapInfo(bootstrap, "Healthcheck setup: not verified (attempts=%d, declined)", attempts)
			return nil
		}
	}
}

// printHealthcheckSetupStatus renders the real state as a two-line block: a symbol +
// state keyword, then the plain-language explanation indented on the next line. The
// keyword is the SAME real state the run reports (WORKING / NOT RUNNING / ...).
func printHealthcheckSetupStatus(st orchestrator.HealthcheckSetupState) {
	symbol := "⚠"
	switch st.Level {
	case orchestrator.HealthcheckSetupLevelOk:
		symbol = "✓"
	case orchestrator.HealthcheckSetupLevelError:
		symbol = "✗"
	}
	fmt.Printf("Status: %s %s\n", symbol, st.Keyword)
	if st.Message != "" {
		fmt.Printf("  %s\n", st.Message)
	}
}
