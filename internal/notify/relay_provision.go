package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
)

// ProvisionRelaySecret performs the get-chat-id handshake WITH provision intent and, on a
// 200 that issues a notify_secret, persists it (immutable identity file) and confirms it
// back to the server so the centralized healthcheck fetch can authenticate WITHOUT any
// Telegram pairing. Now that the server issues the relay secret for a chat-less known
// ServerID, this is the generic, Telegram-independent provisioning entry point used by the
// healthcheck self-heal hooks.
//
// It reuses the exact three bricks the Telegram path uses -
// checkTelegramRegistrationWithSecret(provision=true), identity.PersistNotifySecret, and
// confirmTelegramRelaySecret - so both paths share one wire contract and one log-masking
// path. The issued secret is registered with the logger's masker inside
// checkTelegramRegistrationWithSecret BEFORE any body preview is logged.
//
// Returns provisioned=true once the secret is on disk (a confirm failure is NON-FATAL: the
// hash the server stored on issuance already authenticates the fetch, and the server
// re-confirms on the next run). Every other outcome returns (false, err-or-nil):
//   - a non-200 handshake            -> (false, err)
//   - a 200 that issued no secret    -> (false, nil)   [nothing to adopt]
//   - an empty baseDir               -> (false, err)
//   - an issued secret < identity.NotifySecretMinLen runes -> (false, err)  [defensive floor]
//   - a persist failure              -> (false, err)
//
// Callers MUST treat a non-nil err as "retry later" and NEVER block on it.
func ProvisionRelaySecret(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) (bool, error) {
	if strings.TrimSpace(baseDir) == "" {
		return false, fmt.Errorf("relay provision: empty baseDir, cannot persist relay secret")
	}
	// Serialize the entire issue -> persist -> confirm across processes. Hook a (installer)
	// and hook b (an enable-now daemon) can run concurrently against the same server_id;
	// two DISTINCT minted secrets would strand the host (last-write-wins on disk vs the
	// server's confirm-locks-reissue). An exclusive advisory lock on the identity dir makes
	// exactly one minter win; the loser adopts the winner's on-disk secret below.
	unlock, err := identity.LockNotifySecret(baseDir)
	if err != nil {
		return false, fmt.Errorf("relay provision: lock: %w", err)
	}
	defer unlock()
	// Re-check UNDER the lock: if a concurrent minter already persisted a secret, adopt it
	// instead of minting a competing one. Returns (false, nil) - nothing to provision - so
	// the caller reads the winner's secret from disk on its next fetch. A real read error
	// (the file exists but could not be read) is surfaced rather than swallowed: proceeding
	// would mint a fresh secret OVER a value we failed to read. Missing/empty/malformed still
	// yields ("", nil) per the loader contract, so we fall through and provision.
	if s, err := identity.LoadNotifySecret(baseDir); err != nil {
		return false, fmt.Errorf("relay provision: re-check load: %w", err)
	} else if strings.TrimSpace(s) != "" {
		logTelegramRegistrationDebug(logger, "relay provision: secret already present under lock (adopting; nothing to provision)")
		return false, nil
	}

	status, secret := checkTelegramRegistrationWithSecret(ctx, serverAPIHost, serverID, true, logger)
	if status.Code != 200 {
		// Do NOT wrap the raw server body (status.Error) into the returned error: it is
		// untrusted text. The status code alone is enough for the caller to degrade/retry.
		return false, fmt.Errorf("relay provision: get-chat-id returned status %d", status.Code)
	}
	if secret == "" {
		// Linked/known but the server issued no (new) token - e.g. the secret is already
		// confirmed server-side. Nothing to adopt; not an error.
		logTelegramRegistrationDebug(logger, "relay provision: 200 without token (nothing to provision)")
		return false, nil
	}
	// Defensive length floor (shared with the persistence sink): an issued secret shorter
	// than identity.NotifySecretMinLen is not masked in logs, so refuse it rather than write
	// an unmaskable value to disk. (Server format is 19 chars, so a real secret never trips
	// this.)
	if len([]rune(secret)) < identity.NotifySecretMinLen {
		return false, fmt.Errorf("relay provision: issued secret too short (<%d runes); refusing to persist", identity.NotifySecretMinLen)
	}
	if err := identity.PersistNotifySecret(ctx, baseDir, secret, logger); err != nil {
		return false, fmt.Errorf("relay provision: persist failed: %w", err)
	}
	if err := confirmTelegramRelaySecret(ctx, http.DefaultClient, serverAPIHost, serverID, secret, logger); err != nil {
		// Non-fatal: the secret IS persisted and the issuance hash already authenticates the
		// fetch; the server re-confirms on the next run. confirmTelegramRelaySecret already
		// redacts the secret from its error text.
		logTelegramRegistrationDebug(logger, "relay provision: confirm failed (non-fatal, secret persisted): %v", err)
		return true, nil
	}
	logTelegramRegistrationDebug(logger, "relay provision: secret persisted and confirmed")
	return true, nil
}
