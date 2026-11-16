package workspacegc

import (
	"time"

	"radar/internal/protocol"
	"radar/internal/state"
)

const retentionPassed = "retention_gate_passed"
const retentionHeld = "agent_work_or_review_pending"

// RetentionGate is only the first GC gate, not cleanup execution eligibility.
// Location, ownership, leases and provider safety still require the production
// planner/executor. ReadyAt is the normal retention deadline, not permission.
type RetentionGate struct {
	Decision string
	ReadyAt  time.Time
}

// InspectRetention uses exactly the production GC selection of active refs and
// normal automatic retention. Historical refs and diagnostic display truncation
// cannot change this decision. It does not run provider checks or perform I/O.
func InspectRetention(record state.TaskRecord, refs []state.SourceRefRecord, now time.Time) RetentionGate {
	return retentionGate(record, activeRefsByRecord(refs)[record.ID], now, Options{})
}

func retentionGate(record state.TaskRecord, refs []protocol.SourceRef, now time.Time, options Options) RetentionGate {
	gate := RetentionGate{Decision: "task_not_done"}
	if record.State != "done" {
		return gate
	}
	retention := options.Retention
	if retention == 0 {
		retention = DefaultRetention
	}
	doneAt, err := time.Parse(time.RFC3339, record.DoneAt)
	if err == nil {
		gate.ReadyAt = doneAt.Add(retention)
	}
	if !options.IgnoreRetention && !doneLongEnough(record.DoneAt, now, retention) {
		gate.Decision = "retention_pending"
		if err != nil {
			gate.Decision = "completion_time_missing_or_invalid"
		}
		return gate
	}
	blocked := protocol.SourceRefsActivity(refs) != protocol.ActivityIdle
	for _, ref := range refs {
		blocked = blocked || ref.AgentReview
	}
	gate.Decision = retentionPassed
	if blocked {
		gate.Decision = retentionHeld
	}
	return gate
}
