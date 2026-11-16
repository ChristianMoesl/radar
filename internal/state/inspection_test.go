package state

import (
	"radar/internal/protocol"
	"testing"
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
