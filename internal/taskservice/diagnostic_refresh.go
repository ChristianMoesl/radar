package taskservice

import (
	"context"
	"errors"
	"sync"
	"time"

	"radar/internal/collector"
)

// RefreshDiagnostics reuses native collection, authoring reconciliation and
// publication, but NOT the daemon refresher (which also runs destructive GC).
// Busy collection admission fails immediately, rather than queuing an action
// that could start after its caller's deadline. It never coalesces with unrelated
// work and then claims that work as its own result.
func (s *Service) RefreshDiagnostics(ctx context.Context) (collector.Result, error) {
	if err := ctx.Err(); err != nil {
		return collector.Result{}, err
	}
	if !s.fullMu.TryLock() {
		return collector.Result{}, errors.New("source collection busy")
	}
	defer s.fullMu.Unlock()
	if !s.localMu.TryLock() {
		return collector.Result{}, errors.New("local collection busy")
	}
	local := s.refreshCollected(ctx, true)
	s.localMu.Unlock()
	if err := ctx.Err(); err != nil {
		return local, err
	}
	remote := s.refreshCollected(ctx, false)
	if remote.Attempts == nil {
		remote.Attempts = local.Attempts
	} else {
		for name, attempt := range local.Attempts {
			remote.Attempts[name] = attempt
		}
	}
	return remote, ctx.Err()
}

// Bound waiting for shared publication/lifecycle admission with the caller's
// context while preserving normal serialization. Successful acquisition does not
// certify continued liveness: callers must recheck cancellation before work that
// requires a live context.
func lockCollectionContext(ctx context.Context, mu *sync.Mutex) bool {
	for ctx.Err() == nil {
		if mu.TryLock() {
			return true
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return false
}
