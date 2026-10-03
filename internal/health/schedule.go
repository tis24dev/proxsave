package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// ScheduleAck is the relay's answer about the backup schedule frequency on a config poll that
// sent one: Requested is the frequency the relay last stored, Applied that the backup check and
// every periodic notify check carry its period. Timeout and Grace are the backup check's, in
// seconds, as the relay observed them; nil when it is not a simple check.
type ScheduleAck struct {
	Requested string `json:"requested"`
	Applied   bool   `json:"applied"`
	Timeout   *int64 `json:"timeout"`
	Grace     *int64 `json:"grace"`
}

// Confirms reports whether this ack is the relay applying exactly frequency. A nil ack confirms
// nothing.
func (a *ScheduleAck) Confirms(frequency string) bool {
	return a != nil && a.Applied && a.Requested == frequency
}

// FetchCentralizedConfigPoll is the daemon's config poll with the backup schedule: the
// contract-1 notify policy (FetchCentralizedConfigWithPolicy) plus ?frequency, so the relay
// gives the backup check and the periodic notify checks that period and acks it in Schedule. It
// also returns the HTTP status of the answer (0 when there was none) and logs the transport
// stages in DEBUG as plog says. An empty frequency is not sent.
func FetchCentralizedConfigPoll(ctx context.Context, client *http.Client, serverAPIHost, serverID, secret string, channels []string, notifyOn, frequency string, plog PollLog) (CentralizedConfig, int, error) {
	extra := url.Values{"hc_contract": {"1"}, "notify_on": {notifyOn}}
	if frequency != "" {
		extra.Set("frequency", frequency)
	}
	return fetchConfigPoll(ctx, client, serverAPIHost, serverID, secret, false, channels, extra, plog)
}

// ScheduleStateSchemaVersion is the version of the .schedule_state.json layout.
const ScheduleStateSchemaVersion = 1

// ScheduleCadence is a backup cadence as the schedule state records it: the frequency, the
// weekday as cron names it (mon..sun), the day of the month and the HH:MM time.
type ScheduleCadence struct {
	Frequency string `json:"frequency"`
	Weekday   string `json:"weekday"`
	MonthDay  int    `json:"monthday"`
	Time      string `json:"time"`
}

// ScheduleState is what the daemon keeps about the backup schedule in daemon_state/: the
// frequency the ProxSave HC Server last confirmed (empty when none ever was) and the cadence
// backup.env configured when the daemon started, with how healthchecks ran then. Every daemon
// writes it at start; only a centralized one records a confirmation, so a daemon that goes back
// to centralized runs daily until the relay confirms again.
type ScheduleState struct {
	SchemaVersion int              `json:"schema_version"`
	LastConfirmed string           `json:"last_confirmed,omitempty"`
	ConfirmedTS   int64            `json:"confirmed_ts,omitempty"`
	Configured    *ScheduleCadence `json:"configured,omitempty"`
	ConfiguredTS  int64            `json:"configured_ts,omitempty"`
	// ConfiguredInvalid says backup.env held an invalid value at start: Configured is then the
	// fallback the daemon runs.
	ConfiguredInvalid bool `json:"configured_invalid,omitempty"`
	// Healthchecks is how the daemon ran healthchecks at start: ScheduleHealthchecksCentralized,
	// ScheduleHealthchecksSelf or ScheduleHealthchecksOff. Empty is centralized: the first
	// layout was written by a centralized daemon only.
	Healthchecks string `json:"healthchecks,omitempty"`
}

// The values of ScheduleState.Healthchecks.
const (
	ScheduleHealthchecksCentralized = "centralized"
	ScheduleHealthchecksSelf        = "self"
	ScheduleHealthchecksOff         = "off"
)

// Negotiated reports whether the daemon that wrote st had its cadence confirmed by the relay,
// rather than applied at once.
func (st ScheduleState) Negotiated() bool {
	return st.Healthchecks == "" || st.Healthchecks == ScheduleHealthchecksCentralized
}

// ScheduleStatePath is BASE_DIR/daemon_state/.schedule_state.json.
func ScheduleStatePath(baseDir string) string {
	return filepath.Join(DaemonStateDir(baseDir), ".schedule_state.json")
}

// ReadScheduleState reads the schedule state. A missing or empty file is (zero, false, nil).
func ReadScheduleState(baseDir string) (ScheduleState, bool, error) {
	data, err := os.ReadFile(ScheduleStatePath(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return ScheduleState{}, false, nil
		}
		return ScheduleState{}, false, fmt.Errorf("read schedule state: %w", err)
	}
	if len(data) == 0 {
		return ScheduleState{}, false, nil
	}
	var st ScheduleState
	if err := json.Unmarshal(data, &st); err != nil {
		return ScheduleState{}, false, fmt.Errorf("parse schedule state: %w", err)
	}
	return st, true, nil
}

// WriteScheduleState writes the schedule state atomically, 0600, in a root-only daemon_state/.
func WriteScheduleState(baseDir string, st ScheduleState) error {
	st.SchemaVersion = ScheduleStateSchemaVersion
	return writeJSONAtomic(ScheduleStatePath(baseDir), st)
}
