package diagnostics

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestOperationPhaseProjectionKeepsUnknownInvalidAndCompletedSeparate(t *testing.T) {
	phases := []protocol.OperationPhaseTiming{{Phase: "cleanup_execution", CompletedIntervals: 1, ElapsedMilliseconds: 0}, {Phase: "cleanup_worktree", CompletedIntervals: 2, ElapsedMilliseconds: 3}}
	for _, status := range []string{"running", "succeeded", "failed", "partial", "reported", "unknown"} {
		t.Run(status, func(t *testing.T) {
			native := protocol.ManagedOperation{ID: "op", Kind: "cleanup", Status: status, PhaseTimings: phases}
			view := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{native})[0].Operation
			if status == "reported" || status == "unknown" {
				if view.PhaseTimingEvidence != "unknown_not_captured" || len(view.PhaseTimings) > 0 {
					t.Fatal("unverified phases became native")
				}
				return
			}
			if view.PhaseTimingEvidence != "native_completed_phase_aggregates" || !reflect.DeepEqual(view.PhaseTimings, phases) {
				t.Fatal("completed phases lost", view)
			}
			view.PhaseTimings[0].Phase = "caller-edit"
			if native.PhaseTimings[0].Phase != "cleanup_execution" {
				t.Fatal("projection aliases store")
			}
		})
	}
	cases := map[string][]protocol.OperationPhaseTiming{
		"private name": {{Phase: "/private-secret-error", CompletedIntervals: 1}},
		"duplicate":    {phases[0], phases[0]}, "unordered": {phases[1], phases[0]},
		"missing count":  {{Phase: "cleanup_execution"}},
		"negative count": {{Phase: "cleanup_execution", CompletedIntervals: -1}},
		"large count":    {{Phase: "cleanup_execution", CompletedIntervals: protocol.MaxOperationPhaseIntervals + 1}},
		"negative sum":   {{Phase: "cleanup_execution", CompletedIntervals: 1, ElapsedMilliseconds: -1}},
		"large sum":      {{Phase: "cleanup_execution", CompletedIntervals: 1, ElapsedMilliseconds: protocol.MaxOperationPhaseMilliseconds + 1}},
		"excess phases":  make([]protocol.OperationPhaseTiming, 12),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			view := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{{ID: "op", Kind: "cleanup", Status: "failed", PhaseTimings: bad}})[0].Operation
			raw, _ := json.Marshal(view)
			if view.PhaseTimingEvidence != "unavailable_invalid_or_overflow" || len(view.PhaseTimings) > 0 || strings.Contains(string(raw), "private-secret") {
				t.Fatalf("invalid leaked=%s", raw)
			}
		})
	}
	for _, native := range []protocol.ManagedOperation{{ID: "op", Kind: "cleanup", Status: "running"}, {ID: "op", Kind: "task-note", Status: "succeeded", PhaseTimings: phases}, {ID: "op", Kind: "workspace", Status: "reported", PhaseTimingsUnavailable: true}} {
		view := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{native})[0].Operation
		if view.PhaseTimingEvidence != "unknown_not_captured" || len(view.PhaseTimings) > 0 {
			t.Fatal("no phase evidence became zero/known")
		}
	}
	view := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{{ID: "op", Kind: "cleanup", Status: "partial", PhaseTimings: phases, PhaseTimingsUnavailable: true}})[0].Operation
	if view.PhaseTimingEvidence != "unavailable_invalid_or_overflow" || len(view.PhaseTimings) > 0 {
		t.Fatal("invalid marker did not withhold all sums")
	}
}

func TestOperationPhasesRespectScopeAndInvalidateCompletePaginationWithoutReadMutation(t *testing.T) {
	store, _, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
	taskID := store.Records()[0].NumericID
	a := store.BeginOperation("cleanup", taskID, "")
	b := store.BeginOperation("cleanup", taskID, "")
	store.RecordOperationPhase(a, "cleanup_execution", 0)
	req := protocol.DiagnosticsRequest{Section: "operations", Workspace: path, Limit: 1}
	first, err := Read(store, req, false, integration.NewRegistry())
	if err != nil || first.Total != 2 || first.NextCursor == "" {
		t.Fatal("fixture", err)
	}
	before := store.DashboardSnapshot()
	again, err := Read(store, req, false, integration.NewRegistry())
	if err != nil || first.Snapshot != again.Snapshot || !reflect.DeepEqual(before, store.DashboardSnapshot()) {
		t.Fatal("inspection recorded/ticked phases", err)
	}
	offpage := b
	if first.Items[0].ID == b {
		offpage = a
	}
	store.RecordOperationPhase(offpage, "cleanup_runtime", time.Millisecond)
	req.Cursor = first.NextCursor
	if _, err := Read(store, req, false, integration.NewRegistry()); err == nil {
		t.Fatal("cursor survived off-page phase completion")
	}
	page, err := Read(store, protocol.DiagnosticsRequest{Section: "operations", Workspace: "/not-this-workspace"}, false, integration.NewRegistry())
	if err != nil || page.Total != 0 {
		t.Fatal("phase data crossed scope", err)
	}
	raw, _ := json.Marshal(first)
	for _, label := range []string{"completed_cleanup_phases_only_no_active_phase_progress", "phase_aggregates_overlap_and_sum_individually_truncated_milliseconds", "missing_phase_evidence_is_unknown_not_zero_or_complete_coverage"} {
		if !strings.Contains(string(raw), label) {
			t.Fatal("missing honest limitation", label)
		}
	}
}
