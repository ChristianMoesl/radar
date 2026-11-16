package workspacegc

import (
	"encoding/json"
	"testing"
	"time"

	"radar/internal/protocol"
	"radar/internal/state"
)

func TestRetentionInspectionUsesProductionRefsAndIgnoresDisplayLimits(t *testing.T) {
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	record := state.TaskRecord{ID: "record", State: "done", DoneAt: now.Add(-2 * DefaultRetention).Format(time.RFC3339)}
	refs := []state.SourceRefRecord{
		{ID: "active", TaskRecordID: record.ID, Active: true, Snapshot: protocol.SourceRef{ID: "active", Role: protocol.SourceRefRoleAuthoritative}},
		{ID: "historical", TaskRecordID: record.ID, Snapshot: protocol.SourceRef{ID: "historical", Role: protocol.SourceRefRoleAuthoritative, Activity: protocol.ActivityBusy}},
		{ID: "other", TaskRecordID: "other", Active: true, Snapshot: protocol.SourceRef{ID: "other", Role: protocol.SourceRefRoleAuthoritative, Activity: protocol.ActivityBusy}},
		{ID: "empty-snapshot", TaskRecordID: record.ID, Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, Activity: protocol.ActivityBusy}},
	}
	before, _ := json.Marshal(refs)
	gate := InspectRetention(record, refs, now)
	if gate.Decision != retentionPassed || !gate.ReadyAt.Equal(now.Add(-DefaultRetention)) {
		t.Fatalf("wrong production selection: %+v", gate)
	}
	after, _ := json.Marshal(refs)
	if string(before) != string(after) {
		t.Fatal("inspection mutated its inputs")
	}
	refs[0].Snapshot.Activity = protocol.ActivityBusy
	if gate := InspectRetention(record, refs, now); gate.Decision != retentionHeld {
		t.Fatalf("active work ignored: %+v", gate)
	}
	refs[0].Snapshot.Role = protocol.SourceRefRoleInformational
	if gate := InspectRetention(record, refs, now); gate.Decision != retentionPassed {
		t.Fatalf("informational activity changed production authority policy: %+v", gate)
	}
	refs[0].Snapshot.Role = protocol.SourceRefRoleAuthoritative
	refs[0].Snapshot.Activity = protocol.ActivityIdle
	refs[0].Snapshot.AgentReview = true
	if gate := InspectRetention(record, refs, now); gate.Decision != retentionHeld {
		t.Fatalf("pending review ignored: %+v", gate)
	}
}

func TestRetentionGatePreservesProductionOptionsAndPrecedence(t *testing.T) {
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, state, doneAt, want string
		options                   Options
		busy                      bool
	}{
		{name: "not done", state: "active", want: "task_not_done", busy: true},
		{name: "missing", state: "done", want: "completion_time_missing_or_invalid"},
		{name: "invalid", state: "done", doneAt: "not-a-time", want: "completion_time_missing_or_invalid"},
		{name: "future", state: "done", doneAt: now.Add(time.Hour).Format(time.RFC3339), want: "retention_pending"},
		{name: "boundary", state: "done", doneAt: now.Add(-DefaultRetention).Format(time.RFC3339), want: retentionPassed},
		{name: "before boundary", state: "done", doneAt: now.Add(-DefaultRetention + time.Second).Format(time.RFC3339), want: "retention_pending", busy: true},
		{name: "hold", state: "done", doneAt: now.Add(-DefaultRetention).Format(time.RFC3339), want: retentionHeld, busy: true},
		{name: "custom retention", state: "done", doneAt: now.Add(-DefaultRetention).Format(time.RFC3339), options: Options{Retention: 48 * time.Hour}, want: "retention_pending"},
		{name: "explicit bypass", state: "done", doneAt: now.Format(time.RFC3339), options: Options{IgnoreRetention: true}, want: retentionPassed},
		{name: "existing bypass with missing time", state: "done", options: Options{IgnoreRetention: true}, want: retentionPassed},
		{name: "bypass never bypasses hold", state: "done", options: Options{IgnoreRetention: true}, busy: true, want: retentionHeld},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := protocol.SourceRef{ID: "resource", Role: protocol.SourceRefRoleAuthoritative}
			if test.busy {
				ref.Activity = protocol.ActivityBusy
			}
			gate := retentionGate(state.TaskRecord{State: test.state, DoneAt: test.doneAt}, []protocol.SourceRef{ref}, now, test.options)
			if gate.Decision != test.want {
				t.Fatalf("gate=%+v want=%s", gate, test.want)
			}
		})
	}
}
