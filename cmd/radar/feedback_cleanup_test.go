package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func feedbackManualFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root, anchor, _ := feedbackDiagnosticsFixture(t)
	origin := filepath.Join(root, "origin")
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	registry.Workspaces[0].FeedbackOrigin = origin
	registry.Workspaces = append(registry.Workspaces, workspacegroup.Workspace{ID: workspacegroup.ID(origin), Name: "Origin", Path: origin})
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	return root, anchor, origin
}
func manualCLIView(scope, feedbackID, scopeID string) protocol.DiagnosticCleanup {
	zero := 0
	return protocol.DiagnosticCleanup{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: "preview", Scope: scope, RecordID: "record", TaskID: 7, FeedbackWorkspaceID: feedbackID, ScopeWorkspaceID: scopeID, Evidence: "native_exact_manual_cleanup_plan_not_consent", Status: "preview", Problem: "none", PlanID: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Revision: 1, Targets: []protocol.DiagnosticCleanupTarget{{ID: "cleanup-target:" + strings.Repeat("b", 64), RecordID: "record", TaskID: 7, Provider: "git", Effect: "remove_worktree_and_local_changes", BranchEffect: "delete_local_branch", CleanupMode: "manual", Preserved: []string{"remote_repositories"}, Safety: []protocol.DiagnosticCleanupSafety{}, OwnershipEvidence: "fresh_bounded_registration", AgentOwnerCount: &zero, LeaseEvidence: "unknown_no_lock_file", ExecutionEligibility: "unknown_fresh_execution_guards_not_run"}}, Limitations: protocol.DiagnosticCleanupLimitations()}
}
func serveManualCLI(t *testing.T, handle func(protocol.Request) any) <-chan protocol.Request {
	t.Helper()
	dir, err := os.MkdirTemp("", "mc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	t.Setenv("RADAR_SOCKET", path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	seen := make(chan protocol.Request, 1)
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		var req protocol.Request
		if err = json.NewDecoder(conn).Decode(&req); err == nil {
			seen <- req
			err = json.NewEncoder(conn).Encode(handle(req))
		}
		done <- err
	}()
	t.Cleanup(func() {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("manual RPC did not finish")
		}
	})
	return seen
}
func TestFeedbackManualCleanupExactScopeRPCAndReturnedPartial(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, mode := range []string{"preview", "apply"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				_, anchor, origin := feedbackManualFixture(t)
				scopePath, scopeID := "", ""
				if scope == "origin" {
					scopePath, scopeID = origin, workspacegroup.ID(origin)
				}
				if scope == "feedback" {
					scopePath, scopeID = anchor, workspacegroup.ID(anchor)
				}
				view := manualCLIView(scope, workspacegroup.ID(anchor), scopeID)
				plan := ""
				if mode == "apply" {
					plan = view.PlanID
					view.Mode, view.Status, view.Problem, view.OperationID = "apply", "returned", "native_error", "operation:1"
					view.ResourceRefreshPending = true
					zero := 0
					view.CompletedTargets = &zero
					view.Execution = &protocol.OperationCleanupExecution{Stage: "provider_call", AttemptedTargets: 1}
				}
				seen := serveManualCLI(t, func(protocol.Request) any { return protocol.Response{OK: true, DiagnosticCleanup: &view} })
				out, err := feedbackManualCleanup(anchor, scope, "record", mode, plan)
				if err != nil || out["available"] != true || !reflect.DeepEqual(out["cleanup"], &view) {
					t.Fatalf("result=%+v err=%v", out, err)
				}
				req := <-seen
				want := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: mode, Scope: scope, PlanID: plan, ParentID: "record", Workspace: scopePath, FeedbackWorkspace: anchor}
				if req.Method != "diagnostics-maintenance" || req.Maintenance == nil || *req.Maintenance != want || req.Diagnostics != nil || req.Cleanup != nil {
					t.Fatalf("unexpected native request %+v", req)
				}
			})
		}
	}
}
func TestFeedbackManualCleanupRejectsChangedPrivateOrUnavailableNativeResults(t *testing.T) {
	for _, kind := range []string{"schema", "record", "feedback", "origin", "scope", "preview_expired", "private_sibling", "private_nested", "private_error", "missing_field", "null_field", "apply_without_result", "unknown_effect", "owned", "contended", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			_, anchor, origin := feedbackManualFixture(t)
			view := manualCLIView("origin", workspacegroup.ID(anchor), workspacegroup.ID(origin))
			raw, _ := json.Marshal(protocol.Response{OK: true, DiagnosticCleanup: &view})
			var frame map[string]any
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatal(err)
			}
			v := frame["diagnostic_cleanup"].(map[string]any)
			target := v["targets"].([]any)[0].(map[string]any)
			switch kind {
			case "schema":
				v["diagnostics_schema"] = protocol.DiagnosticsVersion - 1
			case "record":
				v["record_id"] = "different"
			case "feedback":
				v["feedback_workspace_id"] = "different"
			case "origin":
				v["scope_workspace_id"] = "different"
			case "scope":
				v["scope"] = "feedback"
			case "preview_expired":
				v["expires_at"] = "2026-01-01T00:00:00Z"
			case "private_sibling":
				frame["secret"] = "private-sentinel"
			case "private_nested":
				target["path"] = "private-sentinel"
			case "private_error":
				frame = map[string]any{"ok": false, "error": "private-sentinel"}
			case "missing_field":
				delete(v, "resource_refresh_pending")
			case "null_field":
				v["resource_refresh_pending"] = nil
			case "apply_without_result":
				v["mode"] = "apply"
			case "unknown_effect":
				target["effect"] = "private-sentinel"
			case "owned":
				target["agent_owner_count"] = 1
			case "contended":
				target["lease_evidence"] = "contended_at_probe"
			case "oversized":
				frame["secret"] = strings.Repeat("x", 40*1024)
			}
			seen := serveManualCLI(t, func(protocol.Request) any { return frame })
			out, err := feedbackManualCleanup(anchor, "origin", "record", "preview", "")
			<-seen
			encoded, _ := json.Marshal(out)
			if (err == nil && (out == nil || out["available"] != false)) || strings.Contains(string(encoded), "private-sentinel") || (err != nil && strings.Contains(err.Error(), "private-sentinel")) {
				t.Fatalf("unsafe result %s %v", encoded, err)
			}
		})
	}
}
func TestFeedbackManualCleanupUnavailableNeverStartsDaemonOrChangesRegistration(t *testing.T) {
	root, anchor, _ := feedbackManualFixture(t)
	before, _ := os.ReadFile(workspacegroup.Path(root))
	for _, mode := range []string{"preview", "apply"} {
		plan := ""
		if mode == "apply" {
			plan = strings.Repeat("a", 64)
		}
		out, err := feedbackManualCleanup(anchor, "origin", "7", mode, plan)
		if err != nil || out["available"] != false || out["reason"] != "cleanup_outcome_unknown_do_not_replay" || out["cleanup"] != nil {
			t.Fatalf("%+v %v", out, err)
		}
	}
	after, _ := os.ReadFile(workspacegroup.Path(root))
	if string(before) != string(after) {
		t.Fatal("changed registry")
	}
	if _, err := os.Stat(os.Getenv("RADAR_SOCKET")); !os.IsNotExist(err) {
		t.Fatal("daemon started")
	}
}
func TestFeedbackManualCleanupCLIRejectsPartialUnsafeAndExpiredApplySelectors(t *testing.T) {
	_, anchor, _ := feedbackManualFixture(t)
	env := []string{"HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "RADAR_SOCKET=" + os.Getenv("RADAR_SOCKET")}
	base := []string{"feedback-diagnostics", "--workspace", anchor, "--section", "cleanup_preview", "--parent-id", "record", "--json"}
	for _, extra := range [][]string{{"--parent-id", ""}, {"--id", ""}, {"--limit", "20"}, {"--cursor", ""}, {"--state", ""}, {"--workspace-id", ""}, {"--since", ""}, {"--action", "apply"}, {"--action", "delete"}, {"--scope", "all"}, {"--plan-id", ""}, {"--plan-id", strings.Repeat("a", 64)}, {"--action", "apply", "--plan-id", "bad"}, {"--cleanup-mode", "expired", "--workspace-id", "registered", "--action", "apply", "--plan-id", strings.Repeat("a", 64)}} {
		if _, _, code := outputCLI(t, append(append([]string{}, base...), extra...), "", env...); code == 0 {
			t.Fatalf("unsafe CLI selectors %v", extra)
		}
	}
	for _, extra := range [][]string{nil, {"--cleanup-mode", "manual"}, {"--scope", "installation", "--action", "preview"}, {"--scope", "feedback", "--action", "apply", "--plan-id", strings.Repeat("a", 64)}} {
		stdout, stderr, code := outputCLI(t, append(append([]string{}, base...), extra...), "", env...)
		if code != 0 || !strings.Contains(stdout, `"available":false`) || strings.Contains(stdout, `"inventory"`) {
			t.Fatalf("valid selector failure %v: %d %s %s", extra, code, stdout, stderr)
		}
	}
}
