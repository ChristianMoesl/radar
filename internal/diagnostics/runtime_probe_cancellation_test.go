package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type finalRuntimeRegistrationFixture struct {
	*probeRegistrationFixture
	reads int
	final func()
}

func TestRuntimeProbeAbsoluteDeadlineStopsFollowingProviders(t *testing.T) {
	for _, expiredAtEntry := range []bool{false, true} {
		t.Run(map[bool]string{false: "during_first_provider", true: "at_entry"}[expiredAtEntry], func(t *testing.T) {
			store, _, path := fixture(t)
			reg := &probeRegistrationFixture{registrations: []protocol.DiagnosticWorkspace{{ID: "registered", Path: path, SandboxName: "sandbox", SessionName: "session"}}}
			deadline := time.Now().Add(20 * time.Millisecond)
			if expiredAtEntry {
				deadline = time.Now().Add(-time.Second)
			}
			ctx := delayedPreviewDeadline{Context: context.Background(), deadline: deadline}
			calls := []string{}
			sbx := runtimeProbeFixture{source: "sbx", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "present", ID: "private-lifetime", State: "running"}, run: func(context.Context) { time.Sleep(max(time.Until(deadline), 0) + time.Millisecond) }}
			tmux := runtimeProbeFixture{source: "tmux", calls: &calls}
			page, err := Probe(ctx, store, protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: "registered"}, false, integration.NewRegistry(reg, sbx, tmux))
			if expiredAtEntry {
				if err == nil || len(calls) != 0 {
					t.Fatal("expired request called providers", page, err, calls)
				}
				return
			}
			if err != nil || len(calls) != 1 || ctx.Err() != nil {
				t.Fatal("absolute expiry did not stop following providers before timer notification", page, err, calls)
			}
			for _, item := range page.Items {
				if item.Runtime.Observation.Presence != "unknown" || item.Runtime.Observation.Problem != "cancelled_or_timed_out" || item.Runtime.IdentityComparison != "not_observed" {
					t.Fatal("absolute expiry retained usable evidence", item.Runtime)
				}
			}
		})
	}
}

func (p *finalRuntimeRegistrationFixture) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	p.reads++
	if p.reads == 2 && p.final != nil {
		p.final()
	}
	return p.probeRegistrationFixture.DiagnosticWorkspaceRegistrations()
}

func TestRuntimeProbeLateCancellationWithholdsWholeSelectedPage(t *testing.T) {
	for _, kind := range []string{"last_provider", "last_provider_paged", "final_registration", "final_registration_paged", "final_registration_exact", "absolute_deadline_without_timer", "allowed"} {
		t.Run(kind, func(t *testing.T) {
			store, _, path := fixture(t)
			group := protocol.DiagnosticWorkspace{ID: "registered", Path: path, SandboxName: "sandbox", SessionName: "session"}
			reg := &finalRuntimeRegistrationFixture{probeRegistrationFixture: &probeRegistrationFixture{registrations: []protocol.DiagnosticWorkspace{group}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var probeContext context.Context = ctx
			if kind == "absolute_deadline_without_timer" {
				deadline := time.Now().Add(20 * time.Millisecond)
				probeContext = delayedPreviewDeadline{Context: ctx, deadline: deadline}
				reg.final = func() { time.Sleep(max(time.Until(deadline), 0) + time.Millisecond) }
			} else if strings.HasPrefix(kind, "final_registration") {
				reg.final = cancel
			}
			calls := []string{}
			sbx := runtimeProbeFixture{source: "sbx", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "present", ID: "private-sandbox-lifetime", State: "running"}}
			tmux := runtimeProbeFixture{source: "tmux", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "present", ID: "private-session-lifetime", State: "running"}}
			if strings.HasPrefix(kind, "last_provider") {
				tmux.run = func(context.Context) { cancel() }
			}
			request := protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: group.ID}
			if strings.HasSuffix(kind, "_paged") {
				request.Limit = 1
			}
			if strings.HasSuffix(kind, "_exact") {
				identity, _ := json.Marshal([]string{group.ID, "sbx", group.SandboxName})
				sum := sha256.Sum256(identity)
				request.ID = "runtime:" + hex.EncodeToString(sum[:])
			}
			beforeRecords, beforeRefs, revision := store.InspectionSnapshot()
			page, err := Probe(probeContext, store, request, false, integration.NewRegistry(reg, sbx, tmux))
			if err != nil || len(page.Items) == 0 || reg.reads != 2 || len(calls) != map[bool]int{true: 1, false: 2}[request.ID != ""] {
				t.Fatal("late-boundary fixture failed or retried providers", page, err, reg.reads, calls)
			}
			for _, item := range page.Items {
				view := item.Runtime
				if view.OwnershipEvidence != "unknown_not_checked" || view.LeaseEvidence != "unknown_not_checked" {
					t.Fatal("cancellation repair invented ownership/lease evidence", view)
				}
				if kind == "allowed" {
					if view.Observation.Presence != "present" || view.IdentityComparison == "not_observed" {
						t.Fatal("healthy observation was discarded by own deferred cancellation", view)
					}
				} else if view.Observation != (protocol.DiagnosticRuntimeObservation{Presence: "unknown", State: "unknown", Problem: "cancelled_or_timed_out"}) || view.IdentityComparison != "not_observed" {
					t.Fatal("earlier/late observation remained usable after cancellation/expiry", view)
				}
			}
			if kind == "absolute_deadline_without_timer" && probeContext.Err() != nil {
				t.Fatal("fixture did not isolate absolute expiry from timer notification")
			}
			afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
			before, _ := json.Marshal([]any{beforeRecords, beforeRefs})
			after, _ := json.Marshal([]any{afterRecords, afterRefs})
			if string(before) != string(after) || afterRevision != revision {
				t.Fatal("probe cancellation mutated retained state")
			}
		})
	}
}
