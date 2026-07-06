package orchestrator

import "github.com/tis24dev/proxsave/internal/notify"

// Shared affordance copy so the CLI and TUI render identical retry/cap guidance.
const TelegramSetupRetryHint = "You can press Check again, or Skip verification and complete pairing later."
const TelegramSetupMaxAttemptsHint = "Maximum verification attempts reached. Skip and complete pairing later by running proxsave."

// TelegramSetupSeverity classifies a check outcome so both fronts can render a
// DISTINCT label/color per state (e.g. "server unreachable" vs "not paired yet"
// are different situations, not one generic retry).
type TelegramSetupSeverity int

const (
	TelegramSeverityNeutral     TelegramSetupSeverity = iota // pre-check / not applicable
	TelegramSeveritySuccess                                  // linked (green)
	TelegramSeverityPartial                                  // linked, a local step still pending (yellow)
	TelegramSeverityAction                                   // waiting for a user step: start bot / send ID (yellow)
	TelegramSeverityUnreachable                              // could not reach / unexpected server response (yellow, retryable)
	TelegramSeverityFatal                                    // cannot proceed - another check won't help (red)
)

// TelegramSetupState is the UI-agnostic verdict for one registration check. It is
// the ONLY place that maps (status, provision) -> message/policy, so the CLI and
// TUI render identical copy and honor identical retry/skip policy.
type TelegramSetupState struct {
	Code     string                // stable identifier (the new error-code catalog)
	Label    string                // short state name for display (e.g. "Not paired yet")
	Severity TelegramSetupSeverity // display category (color/symbol)
	Message  string                // exact user-facing copy; identical in CLI and TUI
	Verified bool                  // pairing active -> Continue/return allowed
	Partial  bool                  // verified but a local persist/confirm step is pending (distinct copy)
	Fatal    bool                  // another check cannot help -> do NOT offer Check again
}

// ClassifyTelegramSetupResult is the single source of truth for install-time
// pairing copy and policy. Status.Code is authoritative; Provision is consulted
// only on a 200. Both the CLI and TUI MUST render st.Message and honor st.Fatal /
// st.Partial / st.Verified so neither UI can diverge in copy or retry policy.
func ClassifyTelegramSetupResult(res notify.TelegramRegistrationResult) TelegramSetupState {
	switch res.Status.Code {
	case 200:
		switch res.Provision {
		case notify.TelegramProvisionPersistFailed:
			return TelegramSetupState{Code: "linked_token_unsaved", Label: "Linked (finishing setup)", Severity: TelegramSeverityPartial, Verified: true, Partial: true,
				Message: "Linked, but the relay token could not be saved locally. It will be reissued on the next backup."}
		case notify.TelegramProvisionConfirmFailed:
			return TelegramSetupState{Code: "linked_confirm_pending", Label: "Linked (finishing setup)", Severity: TelegramSeverityPartial, Verified: true, Partial: true,
				Message: "Linked, but the relay-token confirmation did not complete. It will finish automatically on the next backup."}
		default: // Confirmed, NoToken, Clean, or the NotApplicable zero value on a bare 200 stub
			return TelegramSetupState{Code: "linked_confirmed", Label: "Linked", Severity: TelegramSeveritySuccess, Verified: true, Message: "Linked successfully."}
		}
	case 403:
		return TelegramSetupState{Code: "bot_not_started", Label: "Bot not started", Severity: TelegramSeverityAction,
			Message: "Start the bot and send the Server ID, then press Check again."}
	case 409:
		return TelegramSetupState{Code: "not_associated", Label: "Not paired yet", Severity: TelegramSeverityAction,
			Message: "Registration not associated yet. Send the Server ID to the bot, then press Check again."}
	case 422:
		return TelegramSetupState{Code: "invalid_server_id", Label: "Invalid Server ID", Severity: TelegramSeverityFatal, Fatal: true,
			Message: "Invalid Server ID. Re-run the installer or regenerate the identity file."}
	case 426:
		return TelegramSetupState{Code: "upgrade_required", Label: "Upgrade required", Severity: TelegramSeverityFatal, Fatal: true,
			Message: "Upgrade ProxSave to v0.28.0 or later to complete pairing."}
	case notify.StatusCodeMissingServerID:
		return TelegramSetupState{Code: "missing_identity", Label: "No server identity", Severity: TelegramSeverityFatal, Fatal: true,
			Message: "Server identity not found. Re-run the installer or regenerate the identity file."}
	case 0:
		return TelegramSetupState{Code: "connection_error", Label: "Server unreachable", Severity: TelegramSeverityUnreachable,
			Message: "Could not reach the pairing server. Check connectivity and press Check again."}
	default:
		// Untrusted upstream text: strip terminal/control sequences AND truncate in
		// this shared path so both the CLI and the TUI render the same safe copy
		// (the TUI only tview.Escapes, so control bytes must be removed here).
		return TelegramSetupState{Code: "unexpected_response", Label: "Unexpected response", Severity: TelegramSeverityUnreachable,
			Message: SanitizeTelegramSetupStatusMessage(res.Status.Message)}
	}
}
