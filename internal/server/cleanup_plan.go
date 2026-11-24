package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"radar/internal/protocol"
)

const maxCleanupPlans = 16
const maxCleanupPlanBytes = 1024 * 1024

// Volatile exact-preview binding on the existing CLI/TUI path. No persisted
// history or consent is recorded. Native providers must still revalidate live
// ownership, activity, aliases, paths and leases at their execution boundaries.
type nativeCleanupPlan struct {
	id         string
	payload    []byte
	revision   int64
	expires    time.Time // monotonic deadline; never accepted from a client
	diagnostic []byte    // Optional immutable typed adapter context; shares this one-use plan.
}

func (s *Server) issueCleanupPlan(ctx context.Context, preview protocol.CleanupPreview, revision int64) (protocol.CleanupPreview, error) {
	return s.issueBoundCleanupPlan(ctx, preview, revision, nil)
}

func (s *Server) issueBoundCleanupPlan(ctx context.Context, preview protocol.CleanupPreview, revision int64, diagnostic *diagnosticCleanupContext) (protocol.CleanupPreview, error) {
	if err := ctx.Err(); err != nil {
		return protocol.CleanupPreview{}, err
	}
	if preview.TaskID <= 0 || len(preview.Targets) == 0 || len(preview.Targets) > 512 {
		return protocol.CleanupPreview{}, errors.New("cleanup preview exceeds target bounds")
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return protocol.CleanupPreview{}, errors.New("cleanup preview identity unavailable")
	}
	preview.PlanID = hex.EncodeToString(token[:])
	raw, err := json.Marshal(preview)
	if err != nil || len(raw) > maxCleanupPlanBytes {
		return protocol.CleanupPreview{}, errors.New("cleanup preview exceeds native payload bounds")
	}
	s.cleanupPlanMu.Lock()
	defer s.cleanupPlanMu.Unlock()
	// Provider reads may be slow. Never issue a freshness identity for an input
	// snapshot that changed while the planner was checking its resources.
	// The publication reader can itself advance the store revision; compare only
	// after it returns, before retaining or superseding any native/typed plan.
	if (s.resourceSnapshot != nil && s.resourceSnapshot().ResourceRefreshPending) || revision != s.store.Revision() {
		return protocol.CleanupPreview{}, errors.New("resource snapshot changed; refresh and preview cleanup again")
	}
	// Admission can wait, and the publication reader can outlive the caller.
	// Recheck after both before issuing or superseding an executable plan.
	if err := ctx.Err(); err != nil {
		return protocol.CleanupPreview{}, err
	}
	now := time.Now()
	for taskID, plan := range s.cleanupPlans {
		if !now.Before(plan.expires) || plan.revision != revision {
			delete(s.cleanupPlans, taskID)
		}
	}
	if _, exists := s.cleanupPlans[preview.TaskID]; !exists && len(s.cleanupPlans) >= maxCleanupPlans {
		return protocol.CleanupPreview{}, errors.New("cleanup preview capacity reached; preview again later")
	}
	if s.cleanupPlans == nil {
		s.cleanupPlans = make(map[int]nativeCleanupPlan)
	}
	var binding []byte
	if diagnostic != nil {
		// Copy all public/private adapter context into the same bounded plan.
		// No caller-owned slices/maps and no second executable plan cache.
		copy := *diagnostic
		copy.View.PlanID, copy.View.ExpiresAt = preview.PlanID, now.Add(time.Minute).UTC().Format(time.RFC3339Nano)
		binding, err = json.Marshal(copy)
		if err != nil || len(raw)+len(binding) > maxCleanupPlanBytes {
			return protocol.CleanupPreview{}, errors.New("cleanup adapter context exceeds native payload bounds")
		}
	}
	// Only the latest successfully issued preview for this task can be applied.
	s.cleanupPlans[preview.TaskID] = nativeCleanupPlan{id: preview.PlanID, payload: raw, revision: revision, expires: now.Add(time.Minute), diagnostic: binding}
	return preview, nil
}

func (s *Server) discardCleanupPlan(preview *protocol.CleanupPreview) {
	s.cleanupPlanMu.Lock()
	defer s.cleanupPlanMu.Unlock()
	if plan, ok := s.cleanupPlans[preview.TaskID]; ok && plan.id == preview.PlanID {
		delete(s.cleanupPlans, preview.TaskID)
	}
}

func (s *Server) takeCleanupPlan(preview *protocol.CleanupPreview) (protocol.CleanupPreview, error) {
	changed := errors.New("cleanup preview missing, expired, consumed or changed; preview again and obtain fresh confirmation")
	if !hexPlanID(preview.PlanID) || len(preview.Targets) == 0 || len(preview.Targets) > 512 {
		return protocol.CleanupPreview{}, changed
	}
	raw, err := json.Marshal(preview)
	if err != nil || len(raw) > maxCleanupPlanBytes {
		return protocol.CleanupPreview{}, changed
	}
	s.cleanupPlanMu.Lock()
	defer s.cleanupPlanMu.Unlock()
	plan, exists := s.cleanupPlans[preview.TaskID]
	if !exists || plan.id != preview.PlanID {
		return protocol.CleanupPreview{}, changed
	}
	// Consume before every remaining check/dispatch. A changed request, lost
	// reply, cancellation, partial failure or successful reread cannot replay it.
	delete(s.cleanupPlans, preview.TaskID)
	if len(plan.diagnostic) != 0 || !time.Now().Before(plan.expires) || plan.revision != s.store.Revision() || !bytes.Equal(raw, plan.payload) {
		return protocol.CleanupPreview{}, changed
	}
	var bound protocol.CleanupPreview
	if err := json.Unmarshal(plan.payload, &bound); err != nil {
		return protocol.CleanupPreview{}, changed
	}
	return bound, nil
}
