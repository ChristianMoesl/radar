package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type cleanupCheckRPCSource struct{ previews, admissions, preflights, releases int }

func (*cleanupCheckRPCSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "git"}
}
func (*cleanupCheckRPCSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	panic("collection")
}
func (*cleanupCheckRPCSource) Cleanup(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
	panic("destructive Cleanup")
}
func (p *cleanupCheckRPCSource) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	return []protocol.DiagnosticWorkspace{}, nil
}
func (p *cleanupCheckRPCSource) PreviewCleanup(_ context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if req.Mode != integration.CleanupConfirmed {
		panic("mode")
	}
	p.previews++
	return []protocol.CleanupTarget{{Source: "git", Kind: "worktree", ResourceID: "private-target", Operation: map[string]string{"command": "private-command"}}}, nil
}
func (p *cleanupCheckRPCSource) AdmitCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	p.admissions++
	return p, func() { p.releases++ }, nil
}
func (p *cleanupCheckRPCSource) PreflightCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
	p.preflights++
	return nil
}
func checkRPCFixture(t *testing.T) (*Server, *cleanupCheckRPCSource, protocol.DiagnosticMaintenanceRequest) {
	t.Helper()
	p := &cleanupCheckRPCSource{}
	s, store := refreshFixture(t, p)
	s.SetResourcePublication(nil, func() protocol.Response { return protocol.Response{} })
	store.SetTasks([]protocol.Task{{Attention: "done", SourceRefs: []protocol.SourceRef{{ID: "git:ABC-123", Source: "git", Kind: "worktree"}}}})
	records, _, _ := store.InspectionSnapshot()
	return s, p, protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup_check", Mode: "check", Scope: "installation", ParentID: records[0].ID}
}
func TestDiagnosticCleanupCheckStrictTypedRPCAndNoRefreshOrCleanup(t *testing.T) {
	s, p, req := checkRPCFixture(t)
	revision := s.store.Revision()
	response := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: &req})
	if !response.OK || response.CleanupCheck == nil || response.Maintenance != nil || response.Diagnostics != nil {
		t.Fatalf("missing typed result: %+v", response)
	}
	view := response.CleanupCheck
	if view.Schema != protocol.DiagnosticsVersion || view.Status != "guards_passed_at_check" || view.CleanupDispatched || view.TargetCount != 1 || view.RecordID != req.ParentID || s.store.Revision() != revision || p.previews != 1 || p.admissions != 1 || p.preflights != 1 || p.releases != 1 {
		t.Fatalf("false/side-effectful result: %+v source=%+v", view, p)
	}
	raw, _ := json.Marshal(response)
	if strings.Contains(string(raw), "private-") {
		t.Fatal("private native target escaped")
	}
}
func rawCheckRPC(t *testing.T, s *Server, frame string) protocol.Response {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); s.handle(a) }()
	if _, err := fmt.Fprintln(b, frame); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(b).Decode(&response); err != nil {
		t.Fatal(err)
	}
	b.Close()
	<-done
	return response
}
func TestDiagnosticCleanupCheckRejectsUnknownAndDuplicateWireFieldsBeforeWork(t *testing.T) {
	for _, kind := range []string{"unknown_envelope", "unknown_target", "subset", "cursor", "state", "cleanup_mode", "native_targets", "duplicate_operation", "casefold_mode", "old_schema", "wrong_method", "extra_task", "refresh_parent", "refresh_workspace", "empty_refresh_parent", "null_refresh_workspace", "empty_check_plan", "null_check_plan"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req := checkRPCFixture(t)
			raw, _ := json.Marshal(req)
			frame := `{"method":"diagnostics-maintenance","maintenance":` + string(raw) + `}`
			switch kind {
			case "unknown_envelope":
				frame = strings.TrimSuffix(frame, "}") + `,"private":"secret"}`
			case "unknown_target", "subset", "cursor", "state", "cleanup_mode", "native_targets":
				frame = strings.Replace(frame, `"maintenance":{`, `"maintenance":{"`+kind+`":"private",`, 1)
			case "duplicate_operation":
				frame = strings.Replace(frame, `"operation":"cleanup_check"`, `"operation":"cleanup_check","operation":"refresh"`, 1)
			case "casefold_mode":
				frame = strings.Replace(frame, `"mode":"check"`, `"mode":"check","MODE":"apply"`, 1)
			case "old_schema":
				frame = strings.Replace(frame, fmt.Sprintf(`"diagnostics_schema":%d`, protocol.DiagnosticsVersion), `"diagnostics_schema":30`, 1)
			case "wrong_method":
				frame = strings.Replace(frame, `"method":"diagnostics-maintenance"`, `"method":"diagnostics"`, 1)
			case "extra_task":
				frame = strings.TrimSuffix(frame, "}") + `,"task_id":7}`
			case "empty_refresh_parent", "null_refresh_workspace":
				frame = strings.Replace(frame, `"operation":"cleanup_check"`, `"operation":"refresh"`, 1)
				frame = strings.Replace(frame, `"mode":"check"`, `"mode":"preview"`, 1)
				frame = strings.Replace(frame, `"parent_id":"`+req.ParentID+`"`, `"parent_id":""`, 1)
				if kind == "null_refresh_workspace" {
					frame = strings.Replace(frame, `"parent_id":""`, `"workspace":null`, 1)
				}
			case "empty_check_plan", "null_check_plan":
				value := `""`
				if kind == "null_check_plan" {
					value = "null"
				}
				frame = strings.Replace(frame, `"maintenance":{`, `"maintenance":{"plan_id":`+value+`,`, 1)
			case "refresh_parent", "refresh_workspace":
				r := refreshRequest("preview", "")
				if kind == "refresh_parent" {
					r.ParentID = req.ParentID
				} else {
					r.Workspace = "/private"
				}
				b, _ := json.Marshal(protocol.Request{Method: "diagnostics-maintenance", Maintenance: &r})
				frame = string(b)
			}
			result := rawCheckRPC(t, s, frame)
			if result.OK || result.CleanupCheck != nil || p.previews != 0 || p.admissions != 0 {
				t.Fatalf("invalid wire reached native checks: %+v %+v", result, p)
			}
		})
	}
}
func TestDiagnosticCleanupCheckAdmissionAndPendingPublicationFailClosed(t *testing.T) {
	for _, kind := range []string{"busy", "pending", "no_snapshot", "refresh_running", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			s, p, req := checkRPCFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "busy":
				s.maintenanceMu.Lock()
				defer s.maintenanceMu.Unlock()
			case "pending":
				s.resourceSnapshot = func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} }
			case "no_snapshot":
				s.resourceSnapshot = nil
			case "refresh_running":
				s.refreshPlan = &diagnosticRefreshPlan{view: protocol.DiagnosticMaintenance{Status: "running"}}
			case "cancelled":
				cancel()
			}
			_, err := s.checkDiagnosticCleanup(ctx, req)
			if err == nil || p.previews != 0 || p.admissions != 0 {
				t.Fatal("unsafe admission", err, p)
			}
		})
	}
}
