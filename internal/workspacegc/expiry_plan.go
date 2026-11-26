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

// ExpiryPlan retains one registered native expiry candidate and its complete
// ordered bundle, including an already-missing path's guarded no-op. It is not
// a public diagnostic plan, consent, resource attestation or replay authority.
// A typed caller must bind fresh registration/scope, explicit destructive consent
// and bounded one-use retention separately; no expiry apply adapter is implied.
type ExpiryPlan struct{ prepared candidateCleanupPlan }

// PrepareExpiry accepts only the exact current registered candidate at its
// native seven-day destructive expiry stage. It never sweeps other candidates,
// substitutes confirmed-manual cleanup or bypasses retention with IgnoreRetention.
// Expected must be captured by the trusted caller, never supplied by a client.
// Preview reads may cover the owning task; only this workspace bundle is kept.
// Existing structural blockers still apply; expiring data warnings retain the
// native expiry policy. No lifecycle/execution admission is acquired here.
func PrepareExpiry(ctx context.Context, store *state.Store, service cleanup.Service, expected Candidate, now time.Time, options Options) (ExpiryPlan, error) {
	prepared, err := prepareRegisteredCandidateCleanup(ctx, store, service, expected, now, options, integration.CleanupExpired)
	return ExpiryPlan{prepared: prepared}, err
}

// Preview returns an owned copy of private native values, not a public wire
// contract or payload that may be accepted back from a client.
func (p ExpiryPlan) Preview() protocol.CleanupPreview { return p.prepared.preview() }

// Execute uses the original policy/time/lifecycle guard and final native
// candidate/provider checks without preview, selection or mode reevaluation.
// A successful prefix or missing-path no-op is not independently verified
// runtime/registration absence, rollback or permission to retry this value.
func (p ExpiryPlan) Execute(ctx context.Context, store *state.Store, service cleanup.Service) (protocol.CleanupResult, bool, error) {
	if store == nil || p.prepared.mode != integration.CleanupExpired || p.prepared.candidate.WorkspaceID == "" || (len(p.prepared.selected.Targets) == 0 && !p.prepared.alreadyMissing) {
		return protocol.CleanupResult{}, false, fmt.Errorf("native workspace-expiry plan unavailable")
	}
	return p.prepared.execute(ctx, store, service)
}
