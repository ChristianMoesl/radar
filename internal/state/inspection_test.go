package state

import (
	"encoding/json"
	"radar/internal/protocol"
	"testing"
	"time"
)

func TestDiagnosticLifecycleInputsReuseProductionAuthoritySelection(t *testing.T) {
	record := TaskRecord{ID: "record"}
	refs := []SourceRefRecord{
		{ID: "primary", TaskRecordID: "record", Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary}},
		{ID: "secondary", TaskRecordID: "record", Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem}},
		{ID: "informational", TaskRecordID: "record", Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleInformational, Lifecycle: protocol.SourceRefLifecycleWorkItem}},
		{ID: "inactive", TaskRecordID: "record", Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary}},
		{ID: "other-record", TaskRecordID: "other", Active: true, Snapshot: protocol.SourceRef{Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary}},
	}
	inputs := LifecycleInputs(record, refs)
	if len(inputs) != 1 || !inputs["primary"] {
		t.Fatalf("wrong production inputs: %v", inputs)
	}
	refs[0].Active = false
	inputs = LifecycleInputs(record, refs)
	if len(inputs) != 1 || !inputs["secondary"] {
		t.Fatalf("wrong fallback production inputs: %v", inputs)
	}
}

func TestDiagnosticLinkingInputsMatchBlockedProductionComponents(t *testing.T) {
	a, b := authoredLinkRef("one", "mark:ABC-123"), authoredLinkRef("two", "mark:ABC-123")
	remote := testJiraIssueRef("jira:issue:ABC-123", "private-title")
	remote.LinkingKeys = []string{"mark:ABC-123"}
	s := reconcileState(persistedState{}, []protocol.Task{makeTask("done", "One", a), makeTask("done", "Two", b), makeTask("attention", "Remote", remote)}, time.Now().UTC())
	if len(s.Records) != 3 {
		t.Fatal("production incorrectly merged fixture")
	}
	before, _ := json.Marshal(s)
	evidence := LinkingInputs(s.Records, s.SourceRefs)
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("linking inspection mutated state")
	}
	authored := 0
	for _, record := range s.Records {
		e := evidence[record.ID]
		if !e.Blocked || e.ConnectedRecords != 3 {
			t.Fatalf("missing production identity blocker: %+v", e)
		}
		authored += e.AuthoredIdentities
	}
	if authored != 2 {
		t.Fatalf("authored identity ownership lost: %d", authored)
	}
}

func TestDiagnosticLinkingInputsRetainInactiveAuthoredOwnership(t *testing.T) {
	a, b := authoredLinkRef("one", "shared"), authoredLinkRef("two", "shared")
	records := []TaskRecord{{ID: "a", Snapshot: protocol.Task{SourceRefs: []protocol.SourceRef{a}}}, {ID: "b", Snapshot: protocol.Task{SourceRefs: []protocol.SourceRef{b}}}}
	refs := []SourceRefRecord{{ID: a.ID, TaskRecordID: "a", Snapshot: a}, {ID: b.ID, TaskRecordID: "b", Active: true, Snapshot: b}, {ID: "workspace:a", TaskRecordID: "a", Active: true, Snapshot: protocol.SourceRef{ID: "workspace:a", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace, LinkingKeys: []string{"shared"}}}}
	evidence := LinkingInputs(records, refs)
	if !evidence["a"].Blocked || !evidence["b"].Blocked || evidence["a"].AuthoredIdentities != 1 || evidence["a"].ConnectedRecords != 2 {
		t.Fatalf("inactive note identity protection lost: %+v", evidence)
	}
	refs[1].Snapshot.LinkingKeys = nil
	evidence = LinkingInputs(records, refs)
	if evidence["a"].Blocked || evidence["b"].Blocked {
		t.Fatal("unconnected records incorrectly blocked")
	}
}
