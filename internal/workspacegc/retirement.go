package workspacegc

import (
	"context"
	"fmt"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

// RetirementPlan retains one native candidate and its complete ordered bundle.
// Private fields prevent replacing its task, mode, policy, time or operations.
// This is not a consent token or a replay-safe diagnostic plan: a typed caller
// must supply fresh registration/scope admission and retain it in its existing
// bounded one-use plan machinery. No registration/runtime presence is attested.
// Execute still runs the original lifecycle callback and native provider guards.
type RetirementPlan struct{ prepared candidateCleanupPlan }

// PrepareRetirement resolves exactly one cached registered GC candidate, never
// a sweep, standalone cleanup, confirmed-manual cleanup or destructive expiry.
// The exact identity must come from the caller's own authorized registration
// capture, not a client-supplied Candidate or native provider payload. Native
// preview may read providers for the owning task; only the selected ordered
// workspace bundle is retained. No execution/admission callback is run here.
func PrepareRetirement(ctx context.Context, store *state.Store, service cleanup.Service, expected Candidate, now time.Time, options Options) (RetirementPlan, error) {
	prepared, err := prepareRegisteredCandidateCleanup(ctx, store, service, expected, now, options, integration.CleanupRetire)
	return RetirementPlan{prepared: prepared}, err
}

// Preview returns an owned copy, not access to the retained executable bundle.
// It contains private native values for trusted in-process disclosure only; it
// must never be serialized as the public diagnostic contract or accepted back
// from a client. Mutating this copy cannot change a later Execute.
func (p RetirementPlan) Preview() protocol.CleanupPreview { return p.prepared.preview() }

// Execute delegates the exact retained bundle to GC's existing final boundary.
// It never previews/reselects targets or recomputes the retained decision time.
// Partial returned prefixes/dispatch evidence and runtime-change flags are not
// independent outcomes, rollback or consent to replay this native value.
func (p RetirementPlan) Execute(ctx context.Context, store *state.Store, service cleanup.Service) (protocol.CleanupResult, bool, error) {
	if store == nil || p.prepared.mode != integration.CleanupRetire || p.prepared.candidate.WorkspaceID == "" || len(p.prepared.selected.Targets) == 0 {
		return protocol.CleanupResult{}, false, fmt.Errorf("native runtime-retirement plan unavailable")
	}
	return p.prepared.execute(ctx, store, service)
}
