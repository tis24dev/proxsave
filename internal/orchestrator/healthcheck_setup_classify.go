package orchestrator

import (
	"errors"

	"github.com/tis24dev/proxsave/internal/health"
)

// Shared hints so the CLI and TUI render identical retry copy.
const (
	HealthcheckSetupRetryHint       = "You can run the check again."
	HealthcheckSetupMaxAttemptsHint = "Maximum checks reached. You can finish the install and verify monitoring later."
)

// HealthcheckSetupState is the single mapping of a check result to user-facing
// copy + policy flags; both front-ends render it identically. All copy is our own
// (no untrusted server text), so no sanitization is needed. LoginURL is the portal
// magic-link to display, when present.
type HealthcheckSetupState struct {
	Message  string
	LoginURL string
	Verified bool // provisioning ready AND monitor reachable -> Continue/return allowed
	Fatal    bool // another check cannot help -> do NOT offer Check again
}

// ClassifyHealthcheckSetupResult maps a HealthcheckCheckResult to display state.
func ClassifyHealthcheckSetupResult(res HealthcheckCheckResult) HealthcheckSetupState {
	st := HealthcheckSetupState{LoginURL: res.LoginURL}
	switch {
	case res.Err == nil:
		st.Verified = true
		st.Message = "Monitoring connection verified: this host's backups will be reported to healthchecks."
	case errors.Is(res.Err, health.ErrHCAuth):
		st.Fatal = true
		st.Message = "The monitoring server rejected this host's credentials. Complete Telegram pairing, then reinstall to retry."
	case errors.Is(res.Err, health.ErrHCUnknown):
		st.Fatal = true
		st.Message = "This host is not registered on the server yet. Complete Telegram pairing first."
	case errors.Is(res.Err, health.ErrHCDisabled):
		st.Fatal = true
		st.Message = "Centralized monitoring is currently disabled on the server; nothing to configure here."
	case errors.Is(res.Err, health.ErrHCNotReady):
		st.Message = "Monitoring is still being provisioned on the server. Try the check again in a moment."
	default:
		st.Message = "Could not reach the monitoring server. Check this host's connectivity and try again."
	}
	return st
}
