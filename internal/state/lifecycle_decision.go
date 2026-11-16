package state

import "radar/internal/protocol"

// LifecycleEvidence describes production's next source-derived decision from
// retained inputs. It is not the recorded state or historical manual intent.
type LifecycleEvidence struct {
	Decision               string
	SourceState            string
	WorkItemInputs         int
	DoneWorkItemInputs     int
	FallbackWorkItemInputs int
	AuthoritativeRefs      int
	ActivityHolds          int
	ReviewHolds            int
	RecordedReviewPending  bool
	NextReviewPending      bool
}

type lifecycleEvaluation struct {
	evidence  LifecycleEvidence
	workItems []SourceRefRecord
}

// InspectLifecycle is observation-only: the very same evaluation is consumed
// by updateRecordLifecycles, but inspection never applies its proposed state.
func InspectLifecycle(record TaskRecord, refs []SourceRefRecord) LifecycleEvidence {
	return evaluateLifecycle(record, refs).evidence
}

func evaluateLifecycle(record TaskRecord, sourceRefs []SourceRefRecord) lifecycleEvaluation {
	refs := activeSourceRefRecordsForRecord(record.ID, sourceRefs)
	workItems := lifecycleWorkItems(refs)
	e := LifecycleEvidence{
		Decision: "no_work_item_inputs", SourceState: "unchanged",
		WorkItemInputs: len(workItems), AuthoritativeRefs: len(refs),
		RecordedReviewPending: record.AgentReviewPending, NextReviewPending: record.AgentReviewPending,
	}
	if len(workItems) == 0 {
		return lifecycleEvaluation{evidence: e, workItems: workItems}
	}
	for _, ref := range workItems {
		if ref.Snapshot.Signal == "" {
			e.FallbackWorkItemInputs++
		}
		if sourceSignal(ref.Snapshot, record.Snapshot.Attention) == "done" {
			e.DoneWorkItemInputs++
		}
	}
	hasAuthored, authoredReview := false, false
	for _, ref := range refs {
		if ref.Snapshot.Activity != protocol.ActivityIdle {
			e.ActivityHolds++
		}
		if ref.Snapshot.AgentReview {
			e.ReviewHolds++
		}
		hasAuthored = hasAuthored || ref.Snapshot.Authored
		authoredReview = authoredReview || (ref.Snapshot.Authored && ref.Snapshot.AgentReview)
	}
	completionCandidate := e.DoneWorkItemInputs == len(workItems)
	if completionCandidate && (e.ActivityHolds > 0 || e.ReviewHolds > 0) && hasAuthored {
		e.NextReviewPending = true
	}
	if authoredReview {
		e.NextReviewPending = false
	}
	e.SourceState = "active"
	switch {
	case !completionCandidate:
		e.Decision = "work_item_not_done"
	case e.ActivityHolds > 0 || e.ReviewHolds > 0:
		e.Decision = "activity_or_review_pending"
	case e.NextReviewPending:
		e.Decision = "durable_review_pending"
	default:
		e.Decision, e.SourceState = "source_completion_ready", "done"
	}
	return lifecycleEvaluation{evidence: e, workItems: workItems}
}
