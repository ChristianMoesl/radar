package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/integration"
	agentintegration "radar/internal/integration/agent"
	"radar/internal/protocol"
)

type supervisionFixture struct {
	diagnosticIntegrationTrap
	calls int
	ids   []string
	views map[string]protocol.DiagnosticAgentSupervision
}

func (p *supervisionFixture) DiagnosticAgentSupervision(ids []string) map[string]protocol.DiagnosticAgentSupervision {
	p.calls++
	p.ids = append([]string(nil), ids...)
	return p.views
}

func TestAgentSupervisionScopedCompletePagesAndMemoryDrift(t *testing.T) {
	store, _, workspace := fixture(t)
	for _, entry := range []struct {
		id, path string
		desired  bool
	}{{"first", workspace, true}, {"second", filepath.Join(workspace, "backing"), false}, {"outside", workspace + "-other", true}} {
		dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), entry.id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		reg := agentservice.Registration{Version: 1, ID: entry.id, Desired: entry.desired, Entrypoint: "private-command", Workspace: protocol.AgentWorkspaceBinding{ID: entry.id, Path: entry.path, Mode: "local"}}
		data, _ := json.Marshal(reg)
		if err := os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	provider := &supervisionFixture{diagnosticIntegrationTrap: diagnosticIntegrationTrap{name: "agent"}, views: map[string]protocol.DiagnosticAgentSupervision{
		"first":   {RetryRecordEvidence: "current_record", Retry: &protocol.DiagnosticAgentRetry{FailedStartAttempts: 2, RetryNotBefore: "2026-11-17T13:00:00Z", StartProblem: true}, ScheduleDeliveryProblem: true},
		"second":  {RetryRecordEvidence: "no_current_record"},
		"outside": {RetryRecordEvidence: "current_record", Retry: &protocol.DiagnosticAgentRetry{FailedStartAttempts: 999, RetryNotBefore: "2026-11-17T13:00:00Z"}},
	}}
	registry := integration.NewRegistry(provider)
	req := protocol.DiagnosticsRequest{Section: "agents", Workspace: workspace, Limit: 1}
	page, err := Read(store, req, true, registry)
	if err != nil || page.Total != 2 || page.Complete || page.NextCursor == "" || !page.ResourceRefreshPending || page.Items[0].Agent.Supervision.Retry.FailedStartAttempts != 2 || !reflect.DeepEqual(provider.ids, []string{"first", "second"}) {
		t.Fatalf("page=%+v ids=%v err=%v", page, provider.ids, err)
	}
	raw, _ := json.Marshal(page)
	if len(raw) > maxPageBytes {
		t.Fatal("page exceeded output bound")
	}
	req.Cursor = page.NextCursor
	last, err := Read(store, req, true, registry)
	if err != nil || !last.Complete || len(last.Items) != 1 || last.Items[0].Agent.Desired || last.Items[0].Agent.OwnershipHold != "registered_owner_even_when_stopped" || last.Items[0].Agent.Supervision.RetryRecordEvidence != "no_current_record" {
		t.Fatalf("stopped owner/evidence lost: %+v %v", last, err)
	}
	// Changes beyond the first page must invalidate its cursor, even if task
	// revision, checkpoints and the first visible agent stay unchanged.
	provider.views["second"] = protocol.DiagnosticAgentSupervision{RetryRecordEvidence: "no_current_record", ScheduleDeliveryProblem: true}
	if _, err := Read(store, req, true, registry); err == nil {
		t.Fatal("cursor accepted changed current-daemon evidence")
	}
	page, err = Read(store, protocol.DiagnosticsRequest{Section: "agents", ID: "second", Workspace: workspace}, false, registry)
	if err != nil || page.Total != 1 || !reflect.DeepEqual(provider.ids, []string{"second"}) || !page.Items[0].Agent.Supervision.ScheduleDeliveryProblem {
		t.Fatal("exact agent selector lost before memory lookup")
	}
	calls := provider.calls
	page, err = Read(store, protocol.DiagnosticsRequest{Section: "agents", ID: "outside", Workspace: workspace}, false, registry)
	if err != nil || page.Total != 0 || provider.calls != calls {
		t.Fatal("out-of-scope request reached manager")
	}
	for _, registry := range []integration.Registry{integration.NewRegistry(), integration.NewRegistry(provider, provider)} {
		calls = provider.calls
		page, err = Read(store, protocol.DiagnosticsRequest{Section: "agents", ID: "first"}, false, registry)
		if err != nil || page.Items[0].Agent.Supervision != nil || page.Items[0].Agent.SupervisionEvidence != "unavailable_not_observed" || provider.calls != calls {
			t.Fatal("missing/ambiguous supervisor fabricated no-retry evidence")
		}
	}
	provider.views = nil
	page, err = Read(store, protocol.DiagnosticsRequest{Section: "agents", ID: "first"}, false, integration.NewRegistry(provider))
	if err != nil || page.Items[0].Agent.Supervision != nil || page.Items[0].Agent.SupervisionEvidence != "unavailable_not_observed" {
		t.Fatal("busy/missing evidence fabricated no-retry conclusion")
	}
	// Actual integration/manager bridge must work without calling the nil
	// workspace dependency, a runner socket, or status/collection methods.
	page, err = Read(store, protocol.DiagnosticsRequest{Section: "agents", ID: "second"}, false, integration.NewRegistry(agentintegration.NewSource(nil)))
	if err != nil || page.Items[0].Agent.SupervisionEvidence != "current_daemon_retry_and_schedule_errors" || page.Items[0].Agent.Supervision.RetryRecordEvidence != "no_current_record" {
		t.Fatalf("native bridge unavailable: %+v %v", page, err)
	}
}
