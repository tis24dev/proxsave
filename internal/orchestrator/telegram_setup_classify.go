package orchestrator

import "github.com/tis24dev/proxsave/internal/notify"

// Shared affordance copy so the CLI and TUI render identical retry/cap guidance.
const TelegramSetupRetryHint = "You can press Check again, or Skip verification and complete pairing later."
const TelegramSetupMaxAttemptsHint = "Maximum verification attempts reached. Skip and complete pairing later by running proxsave."

// TelegramSetupState is the UI-agnostic verdict for one registration check. It is
// the ONLY place that maps (status, provision) -> message/policy, so the CLI and
// TUI render identical copy and honor identical retry/skip policy.
type TelegramSetupState struct {
	Code     string // stable identifier (the new error-code catalog)
	Message  string // exact user-facing copy; identical in CLI and TUI
	Verified bool   // pairing active -> Continue/return allowed
	Partial  bool   // verified but a local persist/confirm step is pending (distinct copy)
	Fatal    bool   // another check cannot help -> do NOT offer Check again
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
			return TelegramSetupState{Code: "linked_token_unsaved", Verified: true, Partial: true,
				Message: "Linked, but the relay token could not be saved locally. It will be reissued on the next backup."}
		case notify.TelegramProvisionConfirmFailed:
			return TelegramSetupState{Code: "linked_confirm_pending", Verified: true, Partial: true,
				Message: "Linked, but the relay-token confirmation did not complete. It will finish automatically on the next backup."}
		default: // Confirmed, NoToken, or NotApplicable (stub zero-value)
			return TelegramSetupState{Code: "linked_confirmed", Verified: true, Message: "Linked successfully."}
		}
	case 403:
		return TelegramSetupState{Code: "bot_not_started",
			Message: "Start the bot and send the Server ID, then press Check again."}
	case 409:
		return TelegramSetupState{Code: "not_associated",
			Message: "Registration not associated yet. Send the Server ID to the bot, then press Check again."}
	case 422:
		return TelegramSetupState{Code: "invalid_server_id", Fatal: true,
			Message: "Invalid Server ID. Re-run the installer or regenerate the identity file."}
	case 426:
		return TelegramSetupState{Code: "upgrade_required", Fatal: true,
			Message: "Upgrade ProxSave to v0.28.0 or later to complete pairing."}
	case notify.StatusCodeMissingServerID:
		return TelegramSetupState{Code: "missing_identity", Fatal: true,
			Message: "Server identity not found. Re-run the installer or regenerate the identity file."}
	case 0:
		return TelegramSetupState{Code: "connection_error",
			Message: "Could not reach the pairing server. Check connectivity and press Check again."}
	default:
		return TelegramSetupState{Code: "unexpected_response",
			Message: TruncateTelegramSetupStatusMessage(res.Status.Message)}
	}
}
