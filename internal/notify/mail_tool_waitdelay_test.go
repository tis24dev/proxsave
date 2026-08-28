package notify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

// TestBothMailToolBranchesCarryTheMailBudget pins two things an edit is likely to get
// wrong in opposite directions.
//
// First, BOTH branches are stamped. Only the absolute one goes through
// TrustedCommandContext, so a fix written against "the TrustedCommandContext line"
// would leave the other exempt. That branch is unreachable today, because every
// resolver in this file goes through lookupAbsolutePath and refuses a non-absolute
// answer, so this half is guarding a future resolver rather than a live path, and
// saying so is more useful than implying coverage the code does not need.
//
// Second, and this is the half that matters, they are stamped with mailToolWaitDelay
// and NOT with the constructor's probe budget. Three seconds is right for "hostname
// -f" and wrong for a delivery: a mail binary can hand off to a consumer still
// draining after the binary exits, and cutting that reports a FALSE failure on a
// message the MTA already accepted. On the proxmox-mail-forward path that failure
// enters sendPMFFallbackChain and the operator receives the same report twice.
func TestBothMailToolBranchesCarryTheMailBudget(t *testing.T) {
	if mailToolWaitDelay <= safeexec.CommandWaitDelay {
		t.Fatalf("mailToolWaitDelay=%s is not longer than the probe budget %s; the whole point of a separate value is that a delivery is not a probe", mailToolWaitDelay, safeexec.CommandWaitDelay)
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "sendmail")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // deliberately executable
		t.Fatalf("write fake sendmail: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, tc := range []struct {
		name       string
		pathOrName string
		branch     string
	}{
		{"absolute path", bin, "TrustedCommandContext, which would otherwise leave the 3 second probe budget in place"},
		{"bare name", "sendmail", "CommandContext, unreachable today but bounded so a future resolver is not silently exempt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := commandForMailTool(context.Background(), tc.pathOrName)
			if err != nil {
				t.Fatalf("commandForMailTool(%q): %v", tc.pathOrName, err)
			}
			if cmd.WaitDelay != mailToolWaitDelay {
				t.Fatalf("the %s branch built a command with WaitDelay=%s, want mailToolWaitDelay=%s. That branch is %s", tc.name, cmd.WaitDelay, mailToolWaitDelay, tc.branch)
			}
		})
	}
}
