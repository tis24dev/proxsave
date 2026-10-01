package main

import (
	"context"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/theme"
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
// the relay confirms it applied exactly that, nothing otherwise. It reports whether sent is no
// longer the policy wanted now: a newer one was asked while this poll was in flight, and this
// poll may have reached the relay after it.
func (d *daemon) recordNotifyPolicyAck(sent notifyPolicy, ack *health.NotifyPolicyAck) (stale bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.notifyPolls++
	d.notifyApplied = nil
	if ack.Confirms(sent.notifyOn, sent.channels) {
		applied := sent
		d.notifyApplied = &applied
	}
	want := notifyPolicy{notifyOn: negotiatedNotifyOn(d.cfg), channels: enabledNotifyChannels(d.cfg)}
	if d.notifyWant != nil {
		want = *d.notifyWant
	}
	return !sent.equal(want)
}

// notifyPolicyResends bounds how many times one config poll sends the wanted policy again after
// finding it had sent a stale one.
const notifyPolicyResends = 2

// refreshNotifyPolicy runs before each scheduled run. It re-reads NOTIFY_ON and the enabled
// channels from backup.env and, when they differ from the policy the relay last confirmed, or
// none is confirmed, negotiates them again so the run finds them applied. Only those two are
// taken from the re-read: every other setting stays the one read at daemon start. A slow or
// unreachable relay costs the run at most notifyPolicyRefreshWait; the run then notifies every
// outcome, as it does whenever the policy is unconfirmed.
//
// Journal (todo point 42): a policy already confirmed is one DEBUG line and no request; one that
// is sent is the "Applying notify level..." block, applied or pending. With no relay secret on
// disk the request is still attempted, as before, but with no block: that case has none.
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
	d.mu.Unlock()
	logger := logging.GetDefaultLogger()
	if applied {
		logging.DebugStep(logger, "notify policy", "before run notify_on=%s channels=%s already confirmed, not sent",
			want.notifyOn, want.channelList())
		return
	}

	block := true
	if secret, _ := identity.LoadNotifySecret(d.cfg.BaseDir); strings.TrimSpace(secret) == "" {
		block = false
		logging.DebugStep(logger, "notify policy", "before run notify_on=%s channels=%s, no relay secret on disk",
			want.notifyOn, want.channelList())
	}
	if block {
		logging.Info("Applying notify level...")
		d.logNotifyPolicyRead(fresh.NotifyOn, want, d.cfg.ConfigPath)
	}

	// The poll runs on its own goroutine because it can reach the relay-secret flock, which
	// honours no deadline. result is written before done is closed and read only after it is.
	var result configPoll
	done := make(chan struct{})
	go func() {
		defer close(done)
		op := ""
		if block {
			op = "notify policy"
		}
		p := d.pollCentralized(ctx, op, "")
		d.afterFailedPoll(p)
		if r := d.reporterFromPoll(p, false); r != nil {
			d.setReporter(r)
		}
		result = p
	}()
	wait := notifyPolicyRefreshWait
	if d.notifyRefreshWaitOverride > 0 {
		wait = d.notifyRefreshWaitOverride
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	answered := false
	select {
	case <-done:
		answered = true
	case <-timer.C:
	case <-ctx.Done():
	}
	if !block {
		return
	}
	if !answered {
		logging.DebugStep(logger, "notify policy", "no answer within %s", wait)
		d.logNotifyLevelOutcome(want, reachNone)
		return
	}
	d.logNotifyPolicyAnswer(result)
	d.logNotifyLevelOutcome(want, result.answer())
}

// logNotifyPolicyRead is the DEBUG evidence of what the notify level block sends: NOTIFY_ON as
// backup.env holds it (quoted, with the value requested instead, when it is not one the relay
// knows) and the enabled channels.
func (d *daemon) logNotifyPolicyRead(raw string, want notifyPolicy, source string) {
	logger := logging.GetDefaultLogger()
	if strings.TrimSpace(raw) != want.notifyOn {
		logging.DebugStep(logger, "notify policy", "read notify_on=%q requested=%s channels=%s source=%s",
			raw, want.notifyOn, want.channelList(), source)
		return
	}
	logging.DebugStep(logger, "notify policy", "read notify_on=%s channels=%s source=%s",
		want.notifyOn, want.channelList(), source)
}

// logNotifyPolicyAnswer is the DEBUG evidence of what the relay answered about the notify policy
// on poll p: its ack, a missing one, or why there was no usable answer (the transport stages are
// already logged by the poll itself).
func (d *daemon) logNotifyPolicyAnswer(p configPoll) {
	logger := logging.GetDefaultLogger()
	switch {
	case !p.sent:
		logging.DebugStep(logger, "notify policy", "not sent error=%v", p.err)
	case p.err != nil:
		logging.DebugStep(logger, "notify policy", "failed %s", pollFailure(p))
	case p.cfg.NotifyPolicy == nil:
		logging.DebugStep(logger, "notify policy", "ack missing")
	default:
		ack := p.cfg.NotifyPolicy
		channels := "none"
		if len(ack.Channels) > 0 {
			channels = strings.Join(ack.Channels, ",")
		}
		logging.DebugStep(logger, "notify policy", "ack notify_on=%s channels=%s mode=%s applied=%t",
			ack.Requested, channels, ack.Mode, ack.Applied)
	}
}

// logNotifyLevelOutcome ends the notify level block: the level, then applied when the relay has
// confirmed want, else what is in effect meanwhile (every outcome notified), why, and pending.
func (d *daemon) logNotifyLevelOutcome(want notifyPolicy, answer pollReach) {
	d.mu.Lock()
	confirmed := d.notifyApplied != nil && d.notifyApplied.equal(want)
	d.mu.Unlock()
	logging.Info("  Notify level: %s", want.notifyOn)
	if confirmed {
		logging.Info("%s Notify level: applied", theme.SymbolSuccess)
		return
	}
	logging.Info("  In effect: %s", config.NotifyOnAlways)
	logging.Info("%s", answer.why())
	logging.Info("%s Notify level: pending, no action needed", theme.SymbolWarning)
}
