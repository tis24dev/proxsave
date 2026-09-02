package safefs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A cancellation while WAITING FOR A SLOT must name the operation and the path the
// same way a cancellation inside the worker does (5e7fa0c). Deleting the
// context.Canceled arm of the limiter-acquire path left internal/safefs green;
// this drives that exact arm: the limiter is full, the context is cancelled.
func TestCancelWhileWaitingForASlotNamesTheOperation(t *testing.T) {
	limiter := &operationLimiter{slots: make(chan struct{}, 1)}
	limiter.slots <- struct{}{} // no free slot: acquire must wait

	// The cancel must land while acquire is BLOCKED on the full limiter: a context
	// cancelled beforehand is answered by runBounded's entry check and never
	// reaches the acquire arm this test exists to pin.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	_, err := runBounded[int](ctx, limiter, time.Minute, &TimeoutError{Op: "stat", Path: "/mnt/nas"}, func() (int, error) {
		t.Fatal("the worker must never run: the slot was never free")
		return 0, nil
	})
	var ce *CancelError
	if !errors.As(err, &ce) {
		t.Fatalf("got %v (%T): a cancel while waiting for a slot lost the operation and the path", err, err)
	}
	if ce.Op != "stat" || ce.Path != "/mnt/nas" {
		t.Fatalf("CancelError names %q %q, want stat /mnt/nas", ce.Op, ce.Path)
	}
}
