package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Audited regression for the historical Tier-1 defaultYes bugs (the tview
// TUI once dropped the engine-supplied default, and the netcommit screen
// once focused COMMIT): for EVERY confirm-style adapter method, a bare Enter
// must resolve to the declared default, and countdown expiry must resolve to
// No regardless of that default. Table-driven over the real charm adapter,
// through a real Session.

func TestCharmConfirmDefaultParity_Audited(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type invocation struct {
		name        string
		screen      string
		invoke      func(ctx context.Context) (bool, error)
		enterAnswer bool // what a bare Enter must resolve to
	}
	cases := []invocation{
		{
			name:   "ConfirmAction defaultNo",
			screen: "Proceed",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmAction(ctx, "Proceed", "Apply the change?", "Apply", "Skip", 0, false)
			},
			enterAnswer: false,
		},
		{
			name:   "ConfirmAction defaultYes",
			screen: "Proceed",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmAction(ctx, "Proceed", "Apply the change?", "Apply", "Skip", 0, true)
			},
			enterAnswer: true,
		},
		{
			name:   "ConfirmFstabMerge defaultNo",
			screen: "Fstab merge",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmFstabMerge(ctx, "Fstab merge", "merge?", 0, false)
			},
			enterAnswer: false,
		},
		{
			name:   "ConfirmFstabMerge defaultYes",
			screen: "Fstab merge",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmFstabMerge(ctx, "Fstab merge", "merge?", 0, true)
			},
			enterAnswer: true,
		},
		{
			name:   "ConfirmCompatibility",
			screen: "Compatibility warning",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmCompatibility(ctx, errors.New("mismatch"))
			},
			enterAnswer: false,
		},
		{
			name:   "ConfirmContinueWithoutSafetyBackup",
			screen: "Safety backup failed",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmContinueWithoutSafetyBackup(ctx, errors.New("disk full"))
			},
			enterAnswer: false,
		},
		{
			name:   "ConfirmContinueWithPBSServicesRunning",
			screen: "PBS services running",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmContinueWithPBSServicesRunning(ctx)
			},
			enterAnswer: false,
		},
		{
			name:   "ConfirmApplyVMConfigs",
			screen: "Apply VM/CT configs",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmApplyVMConfigs(ctx, "pve1", "pve1", 3)
			},
			enterAnswer: false,
		},
		{
			name:   "ConfirmApplyStorageCfg",
			screen: "Apply storage.cfg",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmApplyStorageCfg(ctx, "/tmp/storage.cfg")
			},
			enterAnswer: false,
		},
		{
			name:   "ConfirmApplyDatacenterCfg",
			screen: "Apply datacenter.cfg",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmApplyDatacenterCfg(ctx, "/tmp/datacenter.cfg")
			},
			enterAnswer: false,
		},
		{
			name:   "PromptNetworkCommit",
			screen: "Network apply",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.PromptNetworkCommit(ctx, 30*time.Second, auditNetcommitHealth(), nil, "")
			},
			enterAnswer: false, // the netcommit default is ALWAYS the safe rollback
		},
	}

	type result struct {
		ok  bool
		err error
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resCh := make(chan result, 1)
			go func() {
				ok, err := tc.invoke(context.Background())
				resCh <- result{ok, err}
			}()
			d.waitScreen(tc.screen)
			d.keys("enter")
			res := <-resCh
			if res.err != nil {
				t.Fatalf("unexpected error: %v", res.err)
			}
			if res.ok != tc.enterAnswer {
				t.Fatalf("AUDIT VIOLATION: bare Enter resolved %v, declared default is %v", res.ok, tc.enterAnswer)
			}
		})
	}
}

// TestCharmConfirmTimeoutAlwaysNo_Audited: countdown expiry resolves to No
// even when the Enter default is Yes, through the real adapter methods.
// Short real countdowns keep the test honest end-to-end.
func TestCharmConfirmTimeoutAlwaysNo_Audited(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		ok  bool
		err error
	}
	cases := []struct {
		name   string
		screen string
		invoke func(ctx context.Context) (bool, error)
	}{
		{
			name:   "ConfirmAction defaultYes times out to No",
			screen: "Proceed",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmAction(ctx, "Proceed", "Apply?", "Apply", "Skip", 1*time.Second, true)
			},
		},
		{
			name:   "ConfirmFstabMerge defaultYes times out to No",
			screen: "Fstab merge",
			invoke: func(ctx context.Context) (bool, error) {
				return ui.ConfirmFstabMerge(ctx, "Fstab merge", "merge?", 1*time.Second, true)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resCh := make(chan result, 1)
			go func() {
				ok, err := tc.invoke(context.Background())
				resCh <- result{ok, err}
			}()
			d.waitScreen(tc.screen)
			// Send NOTHING: let the countdown expire.
			select {
			case res := <-resCh:
				if res.err != nil {
					t.Fatalf("unexpected error: %v", res.err)
				}
				if res.ok {
					t.Fatal("AUDIT VIOLATION: countdown expiry resolved Yes")
				}
			case <-time.After(30 * time.Second):
				t.Fatal("countdown did not expire")
			}
		})
	}
}
