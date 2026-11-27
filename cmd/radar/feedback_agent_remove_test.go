package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestFeedbackAgentRemoveExactTaskAgentScopeAndFiniteEvidence(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, mode := range []string{"preview", "apply"} {
			for _, kind := range []string{"valid", "old_schema", "task", "agent", "feedback", "anchor", "plan", "pending", "private", "duplicate", "lost"} {
				t.Run(scope+"/"+mode+"/"+kind, func(t *testing.T) {
					_, anchor, origin := feedbackManualFixture(t)
					dir, err := os.MkdirTemp("", "asp-")
					if err != nil {
						t.Fatal(err)
					}
					defer os.RemoveAll(dir)
					path := filepath.Join(dir, "s")
					t.Setenv("RADAR_SOCKET", path)
					listener, err := net.Listen("unix", path)
					if err != nil {
						t.Fatal(err)
					}
					defer listener.Close()
					observed := make(chan protocol.Request, 1)
					done := make(chan error, 1)
					plan := ""
					if mode == "apply" {
						plan = strings.Repeat("c", 64)
					}
					scopeID, target := "", workspacegroup.ID(origin)
					if scope == "origin" {
						scopeID = target
					}
					if scope == "feedback" {
						target, scopeID = workspacegroup.ID(anchor), workspacegroup.ID(anchor)
					}
					go func() {
						conn, err := listener.Accept()
						if err != nil {
							done <- err
							return
						}
						defer conn.Close()
						var req protocol.Request
						if err := json.NewDecoder(conn).Decode(&req); err != nil {
							done <- err
							return
						}
						observed <- req
						if kind == "lost" {
							done <- nil
							return
						}
						v := protocol.DiagnosticAgentRemove{Schema: protocol.DiagnosticsVersion, Operation: "agent_remove", Mode: mode, Scope: scope, RecordID: "record", TaskID: 7, AgentID: "worker", FeedbackWorkspaceID: workspacegroup.ID(anchor), ScopeWorkspaceID: scopeID, AgentWorkspaceID: target, Evidence: "native_exact_agent_removal_plan_not_consent", PlanID: strings.Repeat("c", 64), ExpiresAt: time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano), Revision: 1, SchedulesBefore: 3, EnabledSchedulesBefore: 2, DispatchEvidence: "not_dispatched", Problem: "none", Outcome: "not_attempted", Observation: protocol.UnknownDiagnosticAgentRemoveObservation(), Limitations: protocol.DiagnosticAgentRemoveLimitations()}
						if mode == "apply" {
							v.Outcome, v.DispatchEvidence, v.OperationID, v.ResourceRefreshPending = "native_handler_returned_unverified", "native_remove_callback_entered_not_resource_outcome", "operation", true
						}
						switch kind {
						case "old_schema":
							v.Schema--
						case "task":
							v.RecordID, v.TaskID = "other", 8
						case "agent":
							v.AgentID = "other"
						case "feedback":
							v.FeedbackWorkspaceID = "other"
						case "anchor":
							v.ScopeWorkspaceID = "other"
						case "plan":
							v.PlanID = "PRIVATE"
						case "pending":
							v.ResourceRefreshPending = !v.ResourceRefreshPending
						}
						raw, _ := json.Marshal(protocol.Response{OK: true, DiagnosticAgentRemove: &v})
						if kind == "private" {
							raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"private":"PRIVATE"}`)
						}
						if kind == "duplicate" {
							raw = []byte(strings.Replace(string(raw), `"agent_id":"worker"`, `"agent_id":"other","agent_id":"worker"`, 1))
						}
						_, err = conn.Write(append(raw, '\n'))
						done <- err
					}()
					result, err := feedbackAgentRemove(anchor, scope, "record", "worker", mode, plan)
					actual := <-observed
					if e := <-done; e != nil {
						t.Fatal(e)
					}
					expected := ""
					if scope == "origin" {
						expected = origin
					}
					if scope == "feedback" {
						expected = anchor
					}
					want := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "agent_remove", Mode: mode, Scope: scope, ParentID: "record", AgentID: "worker", PlanID: plan, Workspace: expected, FeedbackWorkspace: anchor}
					if actual.Method != "diagnostics-maintenance" || actual.Maintenance == nil || *actual.Maintenance != want || actual != (protocol.Request{Method: "diagnostics-maintenance", Maintenance: actual.Maintenance}) {
						t.Fatal("wrong exact registered transport", actual)
					}
					if kind == "valid" {
						if err != nil || result["available"] != true || result["agent_remove"] == nil {
							t.Fatal(result, err)
						}
					} else if err == nil && (result["available"] != false || result["reason"] != "agent_remove_outcome_unknown_do_not_replay") {
						t.Fatal("unsafe response accepted", result)
					}
					if err != nil && strings.Contains(err.Error(), "PRIVATE") {
						t.Fatal("private error escaped")
					}
				})
			}
		}
	}
}

func TestFeedbackAgentRemoveCLISelectorIsolationAndNoStartup(t *testing.T) {
	root, anchor, _ := feedbackManualFixture(t)
	before, err := os.ReadFile(workspacegroup.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "missing"))
	env := []string{"HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "RADAR_SOCKET=" + os.Getenv("RADAR_SOCKET")}
	base := []string{"feedback-diagnostics", "--workspace", anchor, "--section", "agent_remove", "--parent-id", "record", "--agent-id", "worker", "--json"}
	for _, extra := range [][]string{{"--parent-id", ""}, {"--agent-id", ""}, {"--agent-id", "Bad/id"}, {"--id", ""}, {"--limit", "20"}, {"--cursor", ""}, {"--state", ""}, {"--workspace-id", ""}, {"--cleanup-mode", "manual"}, {"--since", ""}, {"--action", "apply"}, {"--action", "restart"}, {"--scope", "all"}, {"--plan-id", ""}, {"--plan-id", strings.Repeat("a", 64)}, {"--action", "apply", "--plan-id", strings.Repeat("A", 64)}} {
		if _, _, code := outputCLI(t, append(append([]string{}, base...), extra...), "", env...); code == 0 {
			t.Fatal("unsafe removal selector", extra)
		}
	}
	for _, section := range []string{"overview", "tasks", "refresh", "cleanup_check", "cleanup_preview", "recreation_preview", "retirement_preview", "agent_probe", "agents", "logs", "operations"} {
		for _, agent := range []string{"worker", ""} {
			args := []string{"feedback-diagnostics", "--workspace", anchor, "--section", section, "--agent-id", agent, "--json"}
			if _, _, code := outputCLI(t, args, "", env...); code == 0 {
				t.Fatal("removal selector ignored on other section", section, agent)
			}
		}
	}
	for _, extra := range [][]string{nil, {"--action", "preview"}, {"--scope", "installation"}, {"--scope", "feedback", "--action", "apply", "--plan-id", strings.Repeat("a", 64)}} {
		stdout, stderr, code := outputCLI(t, append(append([]string{}, base...), extra...), "", env...)
		if code != 0 || !strings.Contains(stdout, `"available":false`) || !strings.Contains(stdout, `"reason":"agent_remove_outcome_unknown_do_not_replay"`) || strings.Contains(stdout, `"inventory"`) || strings.Contains(stdout, `"agent_remove":`) {
			t.Fatal("unknown removal became evidence", code, stdout, stderr)
		}
	}
	if _, err := feedbackInventory(anchor, "origin", protocol.DiagnosticsRequest{Section: "agent_remove", ParentID: "record"}); err == nil {
		t.Fatal("agent removal used generic inventory")
	}
	after, err := os.ReadFile(workspacegroup.Path(root))
	if err != nil || string(before) != string(after) {
		t.Fatal("registration changed")
	}
	if _, err := os.Stat(os.Getenv("RADAR_SOCKET")); !os.IsNotExist(err) {
		t.Fatal("CLI started daemon")
	}
}
