package orchestrator

import (
	"context"
	"sync"
)

// scanController coordinates a single in-flight scan operation (e.g., listing backups)
// so it can be cancelled from UI events (Cancel/Back) without leaking goroutines.
//
// It guarantees:
// - Start() cancels any previous scan.
// - Finish() only clears the active cancel func if it still owns it (no cross-cancel).
type scanController struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	active uint64
}

// Start creates a cancellable child context of parent, registers it as the active scan,
// and cancels any previous active scan. The returned finish must be called (deferred)
// by the scan goroutine.
func (s *scanController) Start(parent context.Context) (scanCtx context.Context, finish func()) {
	if parent == nil {
		parent = context.Background()
	}
	scanCtx, cancel := context.WithCancel(parent)

	var prev context.CancelFunc
	s.mu.Lock()
	s.active++
	id := s.active
	prev = s.cancel
	s.cancel = cancel
	s.mu.Unlock()

	if prev != nil {
		prev()
	}

	return scanCtx, func() {
		cancel()
		s.mu.Lock()
		if s.active == id {
			s.cancel = nil
		}
		s.mu.Unlock()
	}
}

// Cancel cancels the currently active scan (if any).
func (s *scanController) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
