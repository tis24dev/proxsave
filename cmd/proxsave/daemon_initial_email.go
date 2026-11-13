package main

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// The initial email of the monitoring portal (issue 347). HEALTHCHECK_ACCOUNT_INITIAL_EMAIL is sent
// to the relay on the start poll and on the poll before each scheduled run, until the relay gives
// a final answer for that address (applied or rejected); a different address is sent again. The
// relay mails a confirmation link; the click moves this server's project into the account of the
// address. The address is never logged.

// wantedInitialEmail is the address the next poll sends: "" when none is set, healthchecks is not
// centralized, or the relay already answered for good about this address.
func (d *daemon) wantedInitialEmail() string {
	if d.cfg == nil || !d.cfg.HealthcheckEnabled || d.cfg.HealthcheckMode != config.HealthcheckModeCentralized {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	want := d.cfg.HealthcheckAccountInitialEmail
	if d.initialEmailWant != nil {
		want = *d.initialEmailWant
	}
	want = strings.TrimSpace(want)
	if want == "" || want == d.initialEmailDone {
		return ""
	}
	return want
}

// recordInitialEmailAck keeps a final answer for sent, so that address is not sent again. It is
// called only once that answer is logged: an answer the operator never saw (a before-run poll
// that came back only after the run's backup had finished) leaves the address due, to be sent
// again next run.
func (d *daemon) recordInitialEmailAck(sent string, ack *health.AccountInitialEmailAck) {
	if sent == "" || !ack.Final() {
		return
	}
	d.mu.Lock()
	d.initialEmailDone = sent
	d.mu.Unlock()
}

// setInitialEmailWant records the address as backup.env holds it now (refreshNotifyPolicy).
func (d *daemon) setInitialEmailWant(value string) {
	d.mu.Lock()
	d.initialEmailWant = &value
	d.mu.Unlock()
}

// logInitialEmailOutcome is the "Applying monitoring portal email..." block of a poll that sent
// the address: DEBUG evidence, then the outcome. Nothing visible when the poll failed or the relay
// did not answer about it (an older relay): the next poll sends it again.
func (d *daemon) logInitialEmailOutcome(p configPoll) {
	if p.initialEmail == "" {
		return
	}
	logger := logging.GetDefaultLogger()
	logging.DebugStep(logger, "initial email", "read HEALTHCHECK_ACCOUNT_INITIAL_EMAIL set=true source=%s", d.cfg.ConfigPath)
	if p.err != nil {
		logging.DebugStep(logger, "initial email", "no answer %s", pollFailure(p))
		return
	}
	ack := p.cfg.AccountInitialEmail
	if ack == nil {
		logging.DebugStep(logger, "initial email", "ack missing")
		return
	}
	logging.DebugStep(logger, "initial email", "ack state=%s reason=%s", ack.State, ack.Reason)
	logging.Info("Applying monitoring portal email...")
	switch ack.State {
	case "applied":
		logging.Info("%s Monitoring portal email: applied", theme.SymbolSuccess)
	case "rejected":
		if why := initialEmailRejection(ack.Reason); why != "" {
			logging.Info("  %s", why)
		}
		logging.Warning("%s Monitoring portal email: not applied", theme.SymbolWarning)
	default:
		if ack.Reason == "limit" {
			logging.Info("  Daily mail limit reached, retried at the next run")
		} else {
			logging.Info("  Waiting for the confirmation link sent by mail")
		}
		logging.Info("%s Monitoring portal email: not applied", theme.SymbolWarning)
	}
	d.recordInitialEmailAck(p.initialEmail, ack)
}

// logLateInitialEmailOutcome is the initial email block of a before-run poll that did not answer
// within the bounded wait (refreshNotifyPolicy), looked at once, right after the run's "daemon:
// backup finished" line and without waiting. An answer arrived by then is logged there, once, and
// a final one is recorded. One still missing is dropped: nothing visible, and the address, still
// due, goes out again on the poll before the next run, so a dropped poll is never logged later
// and never logged twice. The block is skipped when another poll (a reporter refresh during the
// backup) already brought a final answer for the address and logged it.
func (d *daemon) logLateInitialEmailOutcome(late *beforeRunPoll) {
	if late == nil {
		return
	}
	logger := logging.GetDefaultLogger()
	select {
	case <-late.done:
	default:
		logging.DebugStep(logger, "initial email", "before-run poll still unanswered after the backup, sent again before the next run")
		return
	}
	sent := late.result.initialEmail
	if sent == "" {
		logging.DebugStep(logger, "initial email", "before-run poll answered after the wait, without the address")
		return
	}
	if d.wantedInitialEmail() != sent {
		logging.DebugStep(logger, "initial email", "before-run poll answered after the wait, a final answer was already logged")
		return
	}
	logging.DebugStep(logger, "initial email", "before-run poll answered after the wait")
	d.logInitialEmailOutcome(late.result)
}

// initialEmailRejection is the operator's reason for a rejected address; "" for a reason this
// client does not know (the DEBUG line above has it).
func initialEmailRejection(reason string) string {
	switch reason {
	case "invalid":
		return "Not a usable email address"
	case "reserved":
		return "An address of the ProxSave HC Server"
	case "unconfirmed":
		return "3 confirmation mails expired; set another address"
	}
	return ""
}
