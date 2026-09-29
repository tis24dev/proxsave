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
func FetchDeliveryStatus(ctx context.Context, client *http.Client, serverAPIHost, serverID, secret string) (DeliveryStatus, error) {
	resp, err := serverbot.New(serverAPIHost, client, nil).Do(ctx, serverbot.Request{
		Method:   http.MethodGet,
		Path:     "/api/healthcheck/delivery-status",
		Query:    url.Values{"server_id": {serverID}},
		Secret:   secret,
		Timeout:  deliveryTimeout,
		MaxBytes: 16384,
	})
	if err != nil {
		return DeliveryStatus{}, fmt.Errorf("%w: %v", ErrDeliveryUnavailable, err)
	}
	if resp.Status != http.StatusOK {
		return DeliveryStatus{}, fmt.Errorf("%w: http %d", ErrDeliveryUnavailable, resp.Status)
	}
	var st DeliveryStatus
	if err := resp.JSON(&st); err != nil {
		return DeliveryStatus{}, fmt.Errorf("%w: bad JSON", ErrDeliveryUnavailable)
	}
	if st.SchemaVersion != 1 || !DeliveryStates[st.State] {
		return DeliveryStatus{}, fmt.Errorf("%w: schema %d state %q", ErrDeliveryUnavailable, st.SchemaVersion, st.State)
	}
	return st, nil
}

// PolicyConfirmed reports whether the relay applied exactly this request: same threshold,
// same channel set, applied on the notify checks.
func (s DeliveryStatus) PolicyConfirmed(requested string, channels []string) bool {
	a := s.NotifyPolicy
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
