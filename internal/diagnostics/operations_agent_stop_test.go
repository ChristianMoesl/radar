package diagnostics

import (
	"testing"

	"radar/internal/protocol"
	"radar/internal/state"
)

func TestDiagnosticAgentStopManagedOperationUsesCachedExactWorkspaceRelationship(t *testing.T) {
	refs := []state.SourceRefRecord{{Snapshot: protocol.SourceRef{Source: "agent", Kind: "agent", AgentID: "worker", Metadata: map[string]string{"workspace_id": "origin", "workspace_path": "/registered/origin"}}}}
	operations := []protocol.ManagedOperation{{ID: "operation", Kind: "agent-stop", Status: "succeeded", TaskID: 7, WorkspaceID: "origin"}}
	for _, scope := range []string{"/registered/origin", "/registered/other", ""} {
		q := protocol.DiagnosticsRequest{Workspace: scope}
		items := operationItems(q, nil, refs, operations)
		if scope == "/registered/other" {
			if len(items) != 0 {
				t.Fatal("explicit target crossed scope")
			}
			continue
		}
		if len(items) != 1 || items[0].Operation.Kind != "agent-stop" || items[0].Operation.ExecutionEvidence != "daemon_handler_result_not_resource_verification" || items[0].Operation.CompletedTargets != nil || items[0].Operation.CleanupExecution != nil {
			t.Fatal("native handler evidence lost or promoted to cleanup/resource outcomes", items)
		}
	}
}
