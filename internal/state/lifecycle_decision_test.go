package state

import (
	"encoding/json"
	"reflect"
	"testing"

	"radar/internal/protocol"
)

// Freeze the pre-extraction transition as a regression oracle. Only the shared
// evaluator is used by production; this contract guards its stateful behavior.
func retainedLifecycleContract(records []TaskRecord, sourceRefs []SourceRefRecord, nowText string) {
	for i := range records {
		refs := activeSourceRefRecordsForRecord(records[i].ID, sourceRefs)
		workItems := lifecycleWorkItems(refs)
		if len(workItems) == 0 {
			continue
		}
		fallback := records[i].Snapshot.Attention
		allDone := true
		for _, ref := range workItems {
			if sourceSignal(ref.Snapshot, fallback) != "done" {
				allDone = false
				break
			}
		}
		completionCandidate := allDone
		for _, ref := range refs {
			if ref.Snapshot.Activity != protocol.ActivityIdle || ref.Snapshot.AgentReview {
				allDone = false
			}
		}
		if completionCandidate && !allDone {
			for _, ref := range refs {
				if ref.Snapshot.Authored {
					records[i].AgentReviewPending = true
				}
			}
		}
		for _, ref := range refs {
			if ref.Snapshot.Authored && ref.Snapshot.AgentReview {
				records[i].AgentReviewPending = false
			}
		}
		if records[i].AgentReviewPending {
			allDone = false
		}
		if allDone {
			records[i].State = "done"
			records[i].DoneAt = firstNonEmpty(completionTimestamp(workItems), records[i].DoneAt, nowText)
			records[i].Reason = firstDoneReason(workItems, fallback)
			records[i].UpdatedAt = nowText
			continue
		}
		records[i].State = "active"
		records[i].DoneAt = ""
		records[i].Reason = ""
	}
}

func TestInspectLifecycleMatchesRetainedProductionContract(t *testing.T) {
	for mask := 0; mask < 16384; mask++ {
		flag := func(n int) bool { return mask&(1<<n) != 0 }
		record := TaskRecord{ID: "record", State: "done", DoneAt: "2026-01-01T00:00:00Z", Reason: "private-reason", UpdatedAt: "original", AgentReviewPending: flag(9), Snapshot: protocol.Task{Attention: "attention"}}
		if flag(10) {
			record.Snapshot.Attention = "done"
		}
		primary := protocol.SourceRef{ID: "primary", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityNone, Signal: "done", AgentReview: flag(7), Authored: flag(8), Status: "private-status", Metadata: map[string]string{"completed_at": "2026-01-02T00:00:00Z"}}
		if flag(1) {
			primary.Role = protocol.SourceRefRoleInformational
		}
		if flag(2) {
			primary.Lifecycle = protocol.SourceRefLifecycleWorkspace
		}
		if flag(3) {
			primary.Authority = protocol.SourceRefAuthorityPrimary
		} else if flag(12) {
			primary.Authority = ""
		}
		if flag(4) {
			primary.Signal = ""
		} else if flag(5) {
			primary.Signal = "attention"
		}
		if flag(6) {
			primary.Activity = protocol.ActivityBusy
		}
		refs := []SourceRefRecord{
			{ID: "primary", TaskRecordID: record.ID, Active: flag(0), Snapshot: primary},
			{ID: "secondary", TaskRecordID: record.ID, Active: flag(11), Snapshot: protocol.SourceRef{ID: "secondary", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Signal: "done"}},
			{ID: "informational", TaskRecordID: record.ID, Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleInformational, Authored: true, AgentReview: true, Activity: protocol.ActivityWaiting}},
			{ID: "other", TaskRecordID: "other", Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, AgentReview: true, Activity: protocol.ActivityBusy}},
		}
		if flag(13) {
			refs[1].Snapshot.Signal = "attention"
		}
		before, _ := json.Marshal([]any{record, refs})
		evidence := InspectLifecycle(record, refs)
		after, _ := json.Marshal([]any{record, refs})
		if string(before) != string(after) {
			t.Fatalf("inspection mutated state, mask %d", mask)
		}
		want, got := []TaskRecord{record}, []TaskRecord{record}
		retainedLifecycleContract(want, refs, "2026-01-03T00:00:00Z")
		updateRecordLifecycles(got, refs, "2026-01-03T00:00:00Z")
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("contract changed, mask %d: want %+v got %+v", mask, want, got)
		}
		if evidence.SourceState == "unchanged" {
			if evidence.Decision != "no_work_item_inputs" || !reflect.DeepEqual(got[0], record) {
				t.Fatalf("no-input transition, mask %d: %+v", mask, evidence)
			}
		} else if got[0].State != evidence.SourceState || got[0].AgentReviewPending != evidence.NextReviewPending {
			t.Fatalf("inspection diverged, mask %d: %+v, %+v", mask, evidence, got[0])
		}
	}
}

func TestInspectLifecycleExplainsPrimaryFallbackAndDurableReview(t *testing.T) {
	record := TaskRecord{ID: "record", State: "active", AgentReviewPending: true, Snapshot: protocol.Task{Attention: "done"}}
	refs := []SourceRefRecord{
		{ID: "primary", TaskRecordID: "record", Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary}},
		{ID: "secondary", TaskRecordID: "record", Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Signal: "attention"}},
		{ID: "workspace", TaskRecordID: "record", Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace, Authored: true}},
	}
	e := InspectLifecycle(record, refs)
	if e.Decision != "durable_review_pending" || e.SourceState != "active" || e.WorkItemInputs != 1 || e.DoneWorkItemInputs != 1 || e.FallbackWorkItemInputs != 1 || e.AuthoritativeRefs != 3 || !e.RecordedReviewPending || !e.NextReviewPending {
		t.Fatalf("wrong durable hold: %+v", e)
	}
	refs[2].Snapshot.AgentReview = true
	e = InspectLifecycle(record, refs)
	if e.Decision != "activity_or_review_pending" || e.ReviewHolds != 1 || e.NextReviewPending {
		t.Fatalf("authored review handoff lost: %+v", e)
	}
	refs[2].Snapshot.AgentReview = false
	record.AgentReviewPending = false
	refs[2].Snapshot.Activity = protocol.ActivityWaiting
	e = InspectLifecycle(record, refs)
	if e.Decision != "activity_or_review_pending" || e.ActivityHolds != 1 || !e.NextReviewPending {
		t.Fatalf("completion handoff not retained: %+v", e)
	}
	refs[2].Snapshot.Activity = protocol.ActivityIdle
	e = InspectLifecycle(record, refs)
	if e.Decision != "source_completion_ready" || e.SourceState != "done" {
		t.Fatalf("primary precedence lost: %+v", e)
	}
	refs[0].Active = false
	e = InspectLifecycle(record, refs)
	if e.Decision != "work_item_not_done" || e.DoneWorkItemInputs != 0 || e.FallbackWorkItemInputs != 0 {
		t.Fatalf("secondary transition lost: %+v", e)
	}
}
