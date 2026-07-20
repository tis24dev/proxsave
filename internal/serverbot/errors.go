package serverbot

import "github.com/tis24dev/proxsave/internal/logging"

// TransportError wraps a failed round-trip (encode/build/dial/read). Its Error() is
// ALREADY redacted -- the *url.Error URL is stripped (server_id/secret in the query
// never reaches a log) and the per-request secret is masked -- so it is safe to log
// or bubble up as-is. Unwrap() exposes the original error for errors.Is (e.g.
// context.Canceled / context.DeadlineExceeded); the original's raw string is NOT
// redacted, so log the TransportError itself, never errors.Unwrap(it) directly.
type TransportError struct {
	Op       string // the failing stage: "encode" | "build" | "request" | "read"
	redacted string // pre-redacted message (URL stripped, secret masked)
	err      error  // original, for errors.Is / Unwrap only
}

func (e *TransportError) Error() string { return e.Op + ": " + e.redacted }
func (e *TransportError) Unwrap() error { return e.err }

// newTransportError redacts at construction (it has the per-request secret here):
// strip the *url.Error URL, then mask the secret. An empty secret yields only the
// URL strip, which is correct for the pre-auth get-chat-id call.
func newTransportError(op string, err error, secret string) *TransportError {
	return &TransportError{
		Op:       op,
		redacted: logging.RedactSecrets(logging.RedactURLError(err).Error(), secret),
		err:      err,
	}
}

// AuthRejected reports the shared 401/403 concept. Do does NOT turn these into an
// error: the status stays on Response and drives each caller's own reprovision /
// ErrHCAuth / poll-definitive logic.
func AuthRejected(status int) bool { return status == 401 || status == 403 }
