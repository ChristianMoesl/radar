package state

import (
	"encoding/json"
	"reflect"
	"testing"

	"radar/internal/protocol"
)

func repairFixture(t *testing.T) ([]byte, LinkRepairPlan, protocol.SourceRef) {
	t.Helper()
	a, b := authoredLinkRef("one", "shared"), authoredLinkRef("two", "shared")
	remote := testJiraIssueRef("jira:issue:ABC-7", "Shared")
	refs := []protocol.SourceRef{a, b, remote}
	old := persistedState{Version: stateVersion, NextTaskID: 7, Records: []TaskRecord{{
		ID: "task:7", NumericID: 7, CanonicalKey: "mark:ABC-7", FirstSeen: "original", UpdatedAt: "original",
		Ack: TaskAckState{Cursor: "acknowledged"}, AgentReviewPending: true, SourceRefIDs: sourceRefIDs(refs),
		Snapshot: protocol.Task{SourceRefs: refs, AcknowledgementCursor: "cached-ack"},
	}}}
	for _, ref := range refs {
		old.SourceRefs = append(old.SourceRefs, SourceRefRecord{ID: ref.ID, TaskRecordID: "task:7", Active: true, FirstSeen: "first", LastSeen: "last", Snapshot: ref})
	}
	data, _ := json.Marshal(old)
	plan, err := PlanLinkRepair(data)
	if err != nil {
		t.Fatal(err)
	}
	r := &plan.Resolutions[0]
	r.KeepAuthoredID = a.ID
	r.AcknowledgedIDs = []string{a.ID}
	r.PendingReviewIDs = []string{a.ID, b.ID}
	r.Assignments[remote.ID] = a.ID
	return data, plan, remote
}

func TestExplicitTaskLinkRepairPreservesObservationsAndAttributedHistory(t *testing.T) {
	data, plan, remote := repairFixture(t)
	repaired, err := RepairTaskLinks(data, plan, map[string]string{remote.Binding().LinkingKey(): plan.Resolutions[0].KeepAuthoredID})
	if err != nil {
		t.Fatal(err)
	}
	var before, after persistedState
	_ = json.Unmarshal(data, &before)
	_ = json.Unmarshal(repaired, &after)
	if len(after.Records) != 2 || after.NextTaskID != 8 || validateAuthoredLinks(after) != nil {
		t.Fatalf("invalid repair: %+v", after)
	}
	if after.Records[0].ID != "task:7" || after.Records[0].Ack.Cursor != "acknowledged" || after.Records[1].Ack.Cursor != "" {
		t.Fatal("task/acknowledgement attribution was not preserved")
	}
	for _, record := range after.Records {
		if record.Snapshot.AcknowledgementCursor != "" {
			t.Fatal("cached acknowledgement bypassed explicit attribution")
		}
		if !record.AgentReviewPending || record.FirstSeen != "original" || record.UpdatedAt != "original" {
			t.Fatal("pending review or history was lost")
		}
	}
	for i, ref := range after.SourceRefs {
		ref.TaskRecordID = before.SourceRefs[i].TaskRecordID
		if !reflect.DeepEqual(ref, before.SourceRefs[i]) {
			t.Fatal("repair changed source evidence rather than only record ownership")
		}
	}
	// Fresh refresh cannot immediately recreate the invalid group.
	after = relinkState(after)
	if len(after.Records) != 2 || validateAuthoredLinks(after) != nil {
		t.Fatal("repair was undone by relinking")
	}
}

func TestTaskLinkRepairRejectsGuessesStalePlansAndBindingTransfers(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*LinkRepairPlan, protocol.SourceRef)
	}{
		{"stale", func(p *LinkRepairPlan, _ protocol.SourceRef) { p.ProjectionSHA256 = "stale" }},
		{"missing identity", func(p *LinkRepairPlan, _ protocol.SourceRef) { p.Resolutions[0].KeepAuthoredID = "" }},
		{"missing assignment", func(p *LinkRepairPlan, r protocol.SourceRef) { delete(p.Resolutions[0].Assignments, r.ID) }},
		{"missing ack decision", func(p *LinkRepairPlan, _ protocol.SourceRef) { p.Resolutions[0].AcknowledgedIDs = nil }},
		{"discarded ack", func(p *LinkRepairPlan, _ protocol.SourceRef) { p.Resolutions[0].AcknowledgedIDs = []string{} }},
		{"discarded review", func(p *LinkRepairPlan, _ protocol.SourceRef) { p.Resolutions[0].PendingReviewIDs = []string{} }},
		{"unknown review owner", func(p *LinkRepairPlan, _ protocol.SourceRef) { p.Resolutions[0].PendingReviewIDs = []string{"unknown"} }},
		{"unknown assignment", func(p *LinkRepairPlan, _ protocol.SourceRef) { p.Resolutions[0].Assignments["unknown"] = "" }},
		{"binding transfer", func(p *LinkRepairPlan, r protocol.SourceRef) {
			p.Resolutions[0].Assignments[r.ID] = "obsidian:task:two"
		}},
		{"duplicate", func(p *LinkRepairPlan, _ protocol.SourceRef) { p.Resolutions = append(p.Resolutions, p.Resolutions[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, plan, remote := repairFixture(t)
			test.edit(&plan, remote)
			if _, err := RepairTaskLinks(data, plan, map[string]string{remote.Binding().LinkingKey(): "obsidian:task:one"}); err == nil {
				t.Fatal("unsafe repair accepted")
			}
		})
	}
}

func TestTaskLinkRepairCanRetainUnownedEvidenceSeparately(t *testing.T) {
	data, plan, remote := repairFixture(t)
	plan.Resolutions[0].Assignments[remote.ID] = ""
	repaired, err := RepairTaskLinks(data, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	var state persistedState
	_ = json.Unmarshal(repaired, &state)
	if len(state.Records) != 3 || len(state.SourceRefs) != 3 || state.NextTaskID != 9 {
		t.Fatal("separate contributor was discarded")
	}
}
