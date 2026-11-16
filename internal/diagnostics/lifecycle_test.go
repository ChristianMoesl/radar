package diagnostics

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
	"radar/internal/state"
)

func TestLifecycleEvidenceIsProductionBackedNotDisplayLimitedOrApplied(t *testing.T) {
	record := state.TaskRecord{ID: "record", NumericID: 1, State: "done", DoneAt: "2026-01-01T00:00:00Z", Reason: "private-reason", Snapshot: protocol.Task{Attention: "done"}}
	refs := []state.SourceRefRecord{}
	for i := 0; i < 25; i++ {
		id := "ref-" + strconv.Itoa(i)
		refs = append(refs, state.SourceRefRecord{ID: id, TaskRecordID: record.ID, Active: true, Snapshot: protocol.SourceRef{ID: id, Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Status: "private-status", Metadata: map[string]string{"token": "private-token"}}})
	}
	refs[24].Snapshot.Activity = protocol.ActivityBusy
	refs[24].Snapshot.Authored = true
	before, _ := json.Marshal([]any{record, refs})
	view := taskView(record, refs, state.LinkingEvidence{}, time.Now())
	after, _ := json.Marshal([]any{record, refs})
	if string(before) != string(after) || view.State != "done" || view.DoneAt != record.DoneAt || view.Review {
		t.Fatal("source decision applied or recorded evidence changed")
	}
	e := view.Lifecycle
	want := state.InspectLifecycle(record, refs)
	if !view.Truncated || len(view.Sources) != protocol.DiagnosticChildLimit || e.Decision != want.Decision || e.SourceState != want.SourceState || e.ActivityHolds != want.ActivityHolds || e.WorkItemInputs != want.WorkItemInputs || e.NextReviewPending != want.NextReviewPending {
		t.Fatalf("evidence differs from production or is truncated: %+v", e)
	}
	if e.Evidence != "production_lifecycle_evaluation_on_cached_inputs" || e.Decision != "activity_or_review_pending" || e.SourceState != "active" || e.WorkItemInputs != 25 || e.DoneWorkItemInputs != 25 || e.FallbackWorkItemInputs != 25 || e.ActivityHolds != 1 || !e.NextReviewPending || e.RecordedReviewPending {
		t.Fatalf("wrong source-decision evidence: %+v", e)
	}
	encoded, _ := json.Marshal(view)
	for _, private := range []string{"private-", "decision_evidence", "manual_override"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private content or invented provenance: %s", encoded)
		}
	}
}

func TestLifecycleWithoutWorkItemsDoesNotInventManualProvenance(t *testing.T) {
	record := state.TaskRecord{ID: "record", State: "done", AgentReviewPending: true}
	view := taskView(record, nil, state.LinkingEvidence{}, time.Now())
	if view.State != "done" || !view.Review || view.Lifecycle.SourceState != "unchanged" || view.Lifecycle.Decision != "no_work_item_inputs" || !view.Lifecycle.NextReviewPending || !view.Lifecycle.RecordedReviewPending {
		t.Fatalf("invented transition or provenance: %+v", view)
	}
}

func TestLifecycleSourceDecisionChangesInvalidatePagination(t *testing.T) {
	record := state.TaskRecord{ID: "a", State: "done"}
	ref := state.SourceRefRecord{ID: "ref", TaskRecordID: "a", Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Signal: "done"}}
	view := taskView(record, []state.SourceRefRecord{ref}, state.LinkingEvidence{}, time.Now())
	items := []protocol.DiagnosticItem{{ID: "a", Task: &view}, {ID: "b"}}
	req := protocol.DiagnosticsRequest{Section: "tasks", Limit: 1}
	page, err := paginate(req, items, 1, false, "cached_daemon_state")
	if err != nil || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	ref.Snapshot.Signal = "attention"
	view = taskView(record, []state.SourceRefRecord{ref}, state.LinkingEvidence{}, time.Now())
	req.Cursor = page.NextCursor
	if _, err := paginate(req, items, 1, false, "cached_daemon_state"); err == nil {
		t.Fatal("changed source decision accepted stale cursor")
	}
}
