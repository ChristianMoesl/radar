package workspacegc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

// One native GC candidate, not a sweep or a diagnostic executable plan. Keep
// preparation outside lifecycle admission and retain its original policy/time,
// selected ordered bundle and mode through the final guarded execution. This
// private boundary is also used by Run; it does not supply consent or replay.
type candidateCleanupPlan struct {
	candidate      Candidate
	selected       protocol.CleanupPreview
	mode           integration.CleanupMode
	alreadyMissing bool
	now            time.Time
	options        Options
}

func prepareCandidateCleanup(ctx context.Context, service cleanup.Service, candidate Candidate, now time.Time, options Options) (candidateCleanupPlan, error) {
	mode, err := CandidateMode(candidate, now)
	if err != nil {
		return candidateCleanupPlan{}, err
	}
	preview, err := service.Preview(ctx, candidate.Task, mode)
	alreadyMissing := mode != integration.CleanupRetire && errors.Is(err, cleanup.ErrNoResources) && pathMissing(candidate.Path)
	if err != nil && !alreadyMissing {
		return candidateCleanupPlan{}, err
	}
	selected, workspaceTarget := SelectTargetsForCandidate(preview, candidate)
	if !alreadyMissing && workspaceTarget == nil && (candidate.WorkspaceID != "" || !pathMissing(candidate.Path)) {
		return candidateCleanupPlan{}, fmt.Errorf("matching workspace cleanup target was not found")
	}
	alreadyMissing = alreadyMissing || (mode != integration.CleanupRetire && len(selected.Targets) == 0 && pathMissing(candidate.Path))
	if messages := cleanup.AutomaticBlockingMessages(selected.Targets, mode == integration.CleanupExpired); len(messages) > 0 {
		return candidateCleanupPlan{}, errors.New(messages[0])
	}
	return candidateCleanupPlan{candidate: candidate, selected: selected, mode: mode, alreadyMissing: alreadyMissing, now: now, options: options}, nil
}

// No new planner invocation, target selection or mode decision at execution.
// Native Service/provider admission still runs for the exact prepared bundle.
// A returned prefix can accompany an error; it is not rollback, independently
// verified resource state or permission to replay this in-process value.
func (p candidateCleanupPlan) execute(ctx context.Context, store *state.Store, service cleanup.Service) (protocol.CleanupResult, bool, error) {
	var result protocol.CleanupResult
	runtimeChanged := false
	execute := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !candidateStillCurrent(store, p.candidate, p.now, p.options) {
			return fmt.Errorf("task completion changed or workspace candidate changed during cleanup; preview again")
		}
		if p.alreadyMissing {
			// Successful no-ops need the same final lifecycle and path evidence.
			if !pathMissing(p.candidate.Path) {
				return fmt.Errorf("workspace path is no longer missing or cannot be inspected; preview again")
			}
			return ctx.Err()
		}
		var err error
		result, err = service.Execute(ctx, p.selected, cleanup.ExecuteOptions{Mode: p.mode})
		// Preserve the original in-guard classification boundary, before a guard
		// returns and any later callback can alter provider-owned result maps.
		for _, target := range result.Targets {
			runtimeChanged = runtimeChanged || target.Operation["runtime_retired"] == "true"
		}
		return err
	}
	var err error
	if p.options.GuardExecution != nil {
		err = p.options.GuardExecution(ctx, p.candidate.Task, execute)
	} else {
		err = execute()
	}
	return result, runtimeChanged, err
}
