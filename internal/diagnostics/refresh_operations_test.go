package diagnostics

import (
	"testing"

	"radar/internal/protocol"
)

func TestRefreshOperationIsInstallationOnlyAndNotVerifiedResourceSuccess(t *testing.T) {
	ops := []protocol.ManagedOperation{{ID: "native:1", Kind: "refresh", Status: "succeeded", StartedAt: "2026-11-17T16:00:00Z", FinishedAt: "2026-11-17T16:00:01Z", ElapsedMilliseconds: 1000}}
	items := operationItems(protocol.DiagnosticsRequest{Section: "operations"}, nil, nil, ops)
	if len(items) != 1 || items[0].Operation.Kind != "refresh" || items[0].Operation.ExecutionEvidence != "daemon_handler_result_not_resource_verification" || items[0].Operation.TargetEvidence != "unknown_not_captured" || items[0].Operation.CompletedTargets != nil || items[0].Operation.PhaseTimingEvidence != "unknown_not_captured" {
		t.Fatalf("refresh claims resource success: %+v", items)
	}
	for _, workspace := range []string{"/registered/origin", "/registered/feedback"} {
		if scoped := operationItems(protocol.DiagnosticsRequest{Section: "operations", Workspace: workspace}, nil, nil, ops); len(scoped) != 0 {
			t.Fatal("installation refresh leaked into scoped workspace ownership evidence")
		}
	}
}
