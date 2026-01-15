package orchestrator

import (
	"context"
	"testing"
	"time"
)

func TestScanController_CancelCancelsActiveScan(t *testing.T) {
	var sc scanController
	scanCtx, finish := sc.Start(context.Background())
	defer finish()

	select {
	case <-scanCtx.Done():
		t.Fatalf("scan context unexpectedly cancelled early: %v", scanCtx.Err())
	default:
	}

	sc.Cancel()

	select {
	case <-scanCtx.Done():
		// ok
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("expected scan context to be cancelled")
	}
}

func TestScanController_StartCancelsPreviousAndFinishDoesNotCrossCancel(t *testing.T) {
	var sc scanController
	parent := context.Background()

	ctx1, finish1 := sc.Start(parent)
	ctx2, finish2 := sc.Start(parent)

	select {
	case <-ctx1.Done():
		// ok (should have been cancelled by second Start)
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("expected first scan to be cancelled by subsequent Start")
	}

	select {
	case <-ctx2.Done():
		t.Fatalf("second scan unexpectedly cancelled early: %v", ctx2.Err())
	default:
	}

	// Finishing the first scan must not cancel/clear the second scan.
	finish1()
	select {
	case <-ctx2.Done():
		t.Fatalf("second scan cancelled by finish of first scan: %v", ctx2.Err())
	default:
	}

	sc.Cancel()
	select {
	case <-ctx2.Done():
		// ok
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("expected second scan to be cancelled")
	}

	finish2()
}
