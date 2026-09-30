package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/serverbot"
)

// NotifyPolicyAck is the relay's answer about the notify policy it applied to this host's
// notify checks: warning/failure need event-driven checks (pinged only when a channel really
// sent), always keeps them periodic. Applied is true only when the relay saw every requested
// channel's check in that mode.
type NotifyPolicyAck struct {
	ContractVersion int      `json:"contract_version"`
	Requested       string   `json:"requested"`
	Applied         bool     `json:"applied"`
	Mode            string   `json:"mode"`
	Revision        int      `json:"revision"`
	Channels        []string `json:"channels"`
}

// DeliveryCheck is the relay's view of one of the two monitoring checks.
type DeliveryCheck struct {
	Armed                bool `json:"armed"`
	ConfiguredDownRoutes int  `json:"configured_down_routes"`
	VerifiedDownRoutes   int  `json:"verified_down_routes"`
}

// DeliveryStatus is GET /api/healthcheck/delivery-status: whether the Healthchecks
// integrations of this host's project would deliver a DOWN of the alive and the backup check.
type DeliveryStatus struct {
	SchemaVersion   int              `json:"schema_version"`
	ProjectCode     string           `json:"project_code"`
	State           string           `json:"state"`
	ReasonCodes     []string         `json:"reason_codes"`
	AlertWorker     string           `json:"alert_worker"`
	NotifyPolicy    *NotifyPolicyAck `json:"notify_policy"`
	AgeSeconds      int              `json:"age_seconds"`
	ValidForSeconds int              `json:"valid_for_seconds"`
	Checks          struct {
		Alive  DeliveryCheck `json:"alive"`
		Backup DeliveryCheck `json:"backup"`
	} `json:"checks"`
}

// DeliveryStates are the states the relay may answer; anything else is not a usable answer.
var DeliveryStates = map[string]bool{
	"ready": true, "degraded": true, "not_configured": true, "unverified": true, "unknown": true,
}

// ErrDeliveryUnavailable is any answer that is not a valid schema_version 1 status: the
// relay feature off, the reader down, an old relay without the route, a bad body.
var ErrDeliveryUnavailable = errors.New("healthcheck delivery status unavailable")

const deliveryTimeout = 5 * time.Second

// FetchDeliveryStatus asks the relay, with the same per-server auth as the config poll.
// It never provisions and never retries: a caller that gets an error applies always.
// With a logger, the transport stages are logged in DEBUG under op (serverbot.Request.LogOperation),
// and an answer other than 200 adds an excerpt of its body with the secret masked.
func FetchDeliveryStatus(ctx context.Context, client *http.Client, serverAPIHost, serverID, secret string, logger *logging.Logger, op string) (DeliveryStatus, error) {
	resp, err := serverbot.New(serverAPIHost, client, logger).Do(ctx, serverbot.Request{
		Method:       http.MethodGet,
		Path:         "/api/healthcheck/delivery-status",
		Query:        url.Values{"server_id": {serverID}},
		Secret:       secret,
		Timeout:      deliveryTimeout,
		MaxBytes:     16384,
		LogOperation: op,
	})
	if err != nil {
		return DeliveryStatus{}, fmt.Errorf("%w: %v", ErrDeliveryUnavailable, err)
	}
	if resp.Status != http.StatusOK {
		logging.DebugStep(logger, op, "response body=%q", logging.RedactSecrets(resp.Snippet(200), secret))
		return DeliveryStatus{}, fmt.Errorf("%w: http %d", ErrDeliveryUnavailable, resp.Status)
	}
	var st DeliveryStatus
	if err := resp.JSON(&st); err != nil {
		return DeliveryStatus{}, fmt.Errorf("%w: bad JSON", ErrDeliveryUnavailable)
	}
	if st.SchemaVersion != 1 || !DeliveryStates[st.State] {
		return DeliveryStatus{}, fmt.Errorf("%w: schema %d state %q", ErrDeliveryUnavailable, st.SchemaVersion, st.State)
	}
	// The relay states how old its evaluation is and how long it vouches for it: one it no longer vouches for is
	// not an answer.
	if st.AgeSeconds < 0 || st.ValidForSeconds <= 0 || st.AgeSeconds >= st.ValidForSeconds {
		return DeliveryStatus{}, fmt.Errorf("%w: expired (age %ds, valid for %ds)", ErrDeliveryUnavailable, st.AgeSeconds, st.ValidForSeconds)
	}
	return st, nil
}

// Remaining is how much longer the evaluation may be used, from when it was received: what is left of the relay's
// valid_for_seconds after age_seconds, never more than limit.
func (s DeliveryStatus) Remaining(limit time.Duration) time.Duration {
	left := time.Duration(s.ValidForSeconds-s.AgeSeconds) * time.Second
	switch {
	case left <= 0:
		return 0
	case left > limit:
		return limit
	default:
		return left
	}
}

// PolicyConfirmed reports whether the relay applied exactly this request: same threshold,
// same channel set, applied on the notify checks.
func (s DeliveryStatus) PolicyConfirmed(requested string, channels []string) bool {
	return s.NotifyPolicy.Confirms(requested, channels)
}

// Confirms reports whether this ack is the relay applying exactly this request: same threshold,
// same channel set, applied on the notify checks. A nil ack confirms nothing.
func (a *NotifyPolicyAck) Confirms(requested string, channels []string) bool {
	if a == nil || !a.Applied || a.ContractVersion != 1 || a.Requested != requested {
		return false
	}
	return sameSet(a.Channels, channels)
}

func sameSet(a, b []string) bool {
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}

// FetchCentralizedConfigWithPolicy is the daemon's config poll for a contract-1 client: the
// authoritative channel set plus the notify threshold, so the relay turns the notify checks
// event-driven for warning/failure and periodic for always. notifyOn must already be one of
// always, warning, failure: the caller maps an invalid value to always.
func FetchCentralizedConfigWithPolicy(ctx context.Context, client *http.Client, serverAPIHost, serverID, secret string, channels []string, notifyOn string) (CentralizedConfig, error) {
	return fetchConfigQuery(ctx, client, serverAPIHost, serverID, secret, false, channels,
		url.Values{"hc_contract": {"1"}, "notify_on": {notifyOn}})
}
