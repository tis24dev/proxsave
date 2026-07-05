package orchestrator

import (
	"errors"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/serverbot"
)

// Shared hints so the CLI and TUI render identical retry copy.
const (
	HealthcheckSetupRetryHint       = "You can run the check again."
	HealthcheckSetupMaxAttemptsHint = "Maximum checks reached. You can finish the install and verify monitoring later."
)

// HealthcheckSetupState is the single mapping of a check result to user-facing
// copy + policy flags; both front-ends render it identically. The status Message
// is always our OWN copy; LoginURL (server-minted) is passed through
// serverbot.SanitizeLoginURL so a hostile/MITM'd server cannot inject terminal escape
// sequences into the install console.
type HealthcheckSetupState struct {
	Message  string
	LoginURL string
	Verified bool // provisioning ready AND monitor reachable -> Continue/return allowed
	Fatal    bool // another check cannot help -> do NOT offer Check again
}

// ClassifyHealthcheckSetupResult maps a HealthcheckCheckResult to display state.
func ClassifyHealthcheckSetupResult(res HealthcheckCheckResult) HealthcheckSetupState {
	st := HealthcheckSetupState{LoginURL: serverbot.SanitizeLoginURL(res.LoginURL)}
	switch {
	case res.Err == nil && res.Reachable:
		st.Verified = true
		st.Message = "Monitoring connection verified: this host's backups will be reported to healthchecks."
	case res.Err == nil:
		// Provisioning is ready but reachability was not confirmed (defensive; not
		// reachable in practice since the server rejects empty ping-urls). Retry.
		st.Message = "Monitoring is provisioned, but reachability could not be confirmed. Try the check again."
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
