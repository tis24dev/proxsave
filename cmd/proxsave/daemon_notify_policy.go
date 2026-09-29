package main

import (
	"context"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
)

// notifyPolicyRefreshWait bounds how long a scheduled run waits for the policy negotiation. It
// matches the config poll's own timeout. The wait is enforced here and not by the context alone,
// because buildReporter can reach the relay-secret flock, which honours no deadline.
const notifyPolicyRefreshWait = 5 * time.Second

// notifyPolicy is what the centralized config poll negotiates (contract 1): the NOTIFY_ON
// threshold and the enabled notification channels, sorted.
type notifyPolicy struct {
	notifyOn string
	channels []string
}

func (p notifyPolicy) equal(o notifyPolicy) bool {
	return p.notifyOn == o.notifyOn && strings.Join(p.channels, ",") == strings.Join(o.channels, ",")
}

// channelList renders the channels as the poll sends them: "none" for the empty set.
func (p notifyPolicy) channelList() string {
	if len(p.channels) == 0 {
		return "none"
	}
	return strings.Join(p.channels, ",")
}

// wantedNotifyPolicy is the policy the next config poll sends.
func (d *daemon) wantedNotifyPolicy() notifyPolicy {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.notifyWant != nil {
		return *d.notifyWant
	}
	return notifyPolicy{notifyOn: negotiatedNotifyOn(d.cfg), channels: enabledNotifyChannels(d.cfg)}
}

// recordNotifyPolicyAck keeps what the relay answered to a poll that sent sent: the policy when
// the relay confirms it applied exactly that, nothing otherwise.
func (d *daemon) recordNotifyPolicyAck(sent notifyPolicy, ack *health.NotifyPolicyAck) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.notifyPolls++
	d.notifyApplied = nil
	if ack.Confirms(sent.notifyOn, sent.channels) {
		applied := sent
		d.notifyApplied = &applied
	}
}

// refreshNotifyPolicy runs before each scheduled run. It re-reads NOTIFY_ON and the enabled
// channels from backup.env and, when they differ from the policy the relay last confirmed, or
// none is confirmed, negotiates them again so the run finds them applied. Only those two are
// taken from the re-read: every other setting stays the one read at daemon start. A slow or
// unreachable relay costs the run at most notifyPolicyRefreshWait; the run then notifies every
// outcome, as it does whenever the policy is unconfirmed.
func (d *daemon) refreshNotifyPolicy(ctx context.Context) {
	if d.cfg == nil || !d.cfg.HealthcheckEnabled || d.cfg.HealthcheckMode != config.HealthcheckModeCentralized {
		return
	}
	fresh, err := config.LoadConfigWithBaseDir(d.cfg.ConfigPath, d.cfg.BaseDir)
	if err != nil {
		return
	}
	want := notifyPolicy{notifyOn: negotiatedNotifyOn(fresh), channels: enabledNotifyChannels(fresh)}
	d.mu.Lock()
	d.notifyWant = &want
	applied := d.notifyApplied != nil && d.notifyApplied.equal(want)
	pollsBefore := d.notifyPolls
	d.mu.Unlock()
	if applied {
		return
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if r := d.buildReporter(ctx); r != nil {
			d.setReporter(r)
		}
		d.mu.Lock()
		reached := d.notifyPolls != pollsBefore
		applied := d.notifyApplied != nil && d.notifyApplied.equal(want)
		d.mu.Unlock()
		if reached {
			logging.Debug("daemon: notification policy sent notify_on=%s channels=%s applied=%t",
				want.notifyOn, want.channelList(), applied)
		}
	}()
	wait := notifyPolicyRefreshWait
	if d.notifyRefreshWaitOverride > 0 {
		wait = d.notifyRefreshWaitOverride
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
}
