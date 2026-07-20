package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// Regression for netcommit-default-focuses-commit (2026-06-09 audit),
// rewritten for the Charm network-commit screen in the same commit that
// removed the tview one. Contract: a reflexive Enter (or any single
// keystroke) must NEVER commit a possibly-broken network configuration and
// disarm the automatic rollback; the default is always the safe "Let
// rollback run" choice, and the countdown expiry never commits.

func auditNetcommitHealth() networkHealthReport {
	var health networkHealthReport
	health.add("gateway ping", networkHealthCritical, "gateway unreachable")
	health.add("dns lookup", networkHealthWarn, "slow response")
	return health
}

func bindNetcommit(c *components.Confirm) *struct {
	resolved bool
	result   components.ConfirmResult
	err      error
} {
	cap := &struct {
		resolved bool
		result   components.ConfirmResult
		err      error
	}{}
	c.Bind(func(v components.ConfirmResult, err error) {
		cap.resolved = true
		cap.result = v
		cap.err = err
	})
	return cap
}

func TestNetworkCommitConfirm_BareEnterNeverCommits(t *testing.T) {
	c := newNetworkCommitConfirm(30*time.Second, auditNetcommitHealth(), nil, "/tmp/diag")
	cap := bindNetcommit(c)
	c.Update(shell.KeyMsg("enter")) //nolint:errcheck
	if !cap.resolved {
		t.Fatal("enter did not resolve")
	}
	if cap.result.Answer {
		t.Fatal("AUDIT VIOLATION: bare Enter committed the network configuration")
	}
}

func TestNetworkCommitConfirm_SingleKeystrokesNeverCommit(t *testing.T) {
	c := newNetworkCommitConfirm(30*time.Second, auditNetcommitHealth(), nil, "")
	cap := bindNetcommit(c)
	for _, key := range []string{"y", "Y", "n", "N", "space", "c"} {
		c.Update(shell.KeyMsg(key)) //nolint:errcheck
		if cap.resolved {
			t.Fatalf("AUDIT VIOLATION: single key %q resolved the netcommit prompt", key)
		}
	}
}

func TestNetworkCommitConfirm_TimeoutNeverCommits(t *testing.T) {
	// Real 1-second countdown: run the armed tick commands exactly as the
	// Bubble Tea runtime would, until expiry.
	c := newNetworkCommitConfirm(1*time.Second, auditNetcommitHealth(), nil, "")
	cap := bindNetcommit(c)
	// Even after the user navigated onto COMMIT, expiry must resolve to
	// not-committed.
	c.Update(shell.KeyMsg("left")) //nolint:errcheck
	cmd := c.Init()
	if cmd == nil {
		t.Fatal("netcommit confirm must arm its countdown")
	}
	hardDeadline := time.Now().Add(10 * time.Second)
	for !cap.resolved && cmd != nil && time.Now().Before(hardDeadline) {
		msg := cmd()
		if msg == nil {
			break
		}
		_, cmd = c.Update(msg)
	}
	if !cap.resolved {
		t.Fatal("deadline tick did not resolve")
	}
	if cap.result.Answer || !cap.result.TimedOut {
		t.Fatalf("AUDIT VIOLATION: timeout resolved %+v, want not-committed timeout", cap.result)
	}
}

func TestNetworkCommitConfirm_CommitRequiresDeliberateNavigation(t *testing.T) {
	c := newNetworkCommitConfirm(30*time.Second, auditNetcommitHealth(), nil, "")
	cap := bindNetcommit(c)
	c.Update(shell.KeyMsg("left"))  //nolint:errcheck
	c.Update(shell.KeyMsg("enter")) //nolint:errcheck
	if !cap.resolved || !cap.result.Answer {
		t.Fatalf("deliberate navigation must still allow commit, got %+v", cap.result)
	}
}

func TestNetworkCommitConfirm_ViewContract(t *testing.T) {
	repair := &nicRepairResult{
		AppliedNICMap: []nicMappingEntry{{OldName: "eth0", NewName: "enp1s0"}},
		ChangedFiles:  []string{"/etc/network/interfaces"},
	}
	c := newNetworkCommitConfirm(30*time.Second, auditNetcommitHealth(), repair, "/tmp/diag")
	view := c.View(100, 30)
	for _, want := range []string{
		"Rollback in 30s",
		"on timeout: Let rollback run",
		"COMMIT",
		"Let rollback run",
		"Network health: CRITICAL",
		"gateway unreachable",
		"Recommendation: do NOT commit",
		"NIC repair: APPLIED (1 file(s))",
		"eth0 -> enp1s0",
		"/tmp/diag",
		"rollback will be automatic",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("netcommit view missing %q", want)
		}
	}
}

func TestCharmPromptNetworkCommit_AdapterSemantics(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	// Expired window: not committed, no screen shown.
	ok, err := ui.PromptNetworkCommit(context.Background(), 0, auditNetcommitHealth(), nil, "")
	if err != nil || ok {
		t.Fatalf("expired window must be (false, nil), got ok=%v err=%v", ok, err)
	}

	type result struct {
		ok  bool
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		ok, err := ui.PromptNetworkCommit(context.Background(), 30*time.Second, auditNetcommitHealth(), nil, "/tmp/diag")
		resCh <- result{ok, err}
	}()
	d.waitScreen("Network apply")
	d.keys("enter") // bare Enter: let rollback run
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("bare enter must not commit, got %+v", res)
	}

	// Cancelled context short-circuits before any screen.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ui.PromptNetworkCommit(cancelled, 30*time.Second, auditNetcommitHealth(), nil, ""); err == nil {
		t.Fatal("cancelled context must surface an error")
	}
}
