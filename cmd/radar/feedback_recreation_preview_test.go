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

func TestFeedbackRecreationPreviewExactScopeVersionAndEvidence(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, mode := range []string{"preview", "apply"} {
			for _, kind := range []string{"valid", "old_schema", "wrong_target", "wrong_status", "wrong_feedback", "pending", "lost", "private"} {
				t.Run(scope+"/"+mode+"/"+kind, func(t *testing.T) {
					_, anchor, origin := feedbackManualFixture(t)
					parent := workspacegroup.ID(origin)
					if scope == "feedback" {
						parent = workspacegroup.ID(anchor)
					}
					dir, err := os.MkdirTemp("", "rp-")
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
						scopeID := parent
						if scope == "installation" {
							scopeID = ""
						}
						v := protocol.DiagnosticRecreation{Schema: protocol.DiagnosticsVersion, Operation: "recreate", Mode: mode, Scope: scope, FeedbackWorkspaceID: workspacegroup.ID(anchor), ScopeWorkspaceID: scopeID, Evidence: "native_exact_workspace_recreation_plan_not_consent", PlanID: strings.Repeat("c", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Revision: 1, Proposal: protocol.DiagnosticRecreationPreview{ID: parent, PreviewID: "recreation-preview:" + strings.Repeat("b", 64), Status: "recreate", PreviousState: "running", RuntimeIdentityObserved: true, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "unavailable", LeaseEvidence: "unavailable"}, Problem: "none", Outcome: "not_attempted", Limitations: protocol.DiagnosticRecreationLimitations()}
						if mode == "apply" {
							v.Outcome = "native_restoration_checks_returned"
							v.OperationID = "operation"
							v.ResourceRefreshPending = true
						}
						switch kind {
						case "old_schema":
							v.Schema--
						case "wrong_target":
							v.Proposal.ID = "other"
						case "wrong_status":
							v.Proposal.Status = "recreated"
						case "wrong_feedback":
							v.FeedbackWorkspaceID = "other"
						case "pending":
							v.ResourceRefreshPending = !v.ResourceRefreshPending
						}
						raw, _ := json.Marshal(protocol.Response{OK: true, DiagnosticRecreation: &v})
						if kind == "private" {
							raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"private":"PRIVATE"}`)
						}
						_, err = conn.Write(append(raw, '\n'))
						done <- err
					}()
					result, err := feedbackRecreation(anchor, scope, parent, mode, plan)
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
					if actual.Method != "diagnostics-maintenance" || actual.Maintenance == nil || actual.Maintenance.Operation != "recreate" || actual.Maintenance.ParentID != parent || actual.Maintenance.Workspace != expected || actual.Maintenance.FeedbackWorkspace != anchor || actual.Maintenance.Mode != mode || actual.Maintenance.PlanID != plan || actual.Diagnostics != nil || actual.Cleanup != nil {
						t.Fatalf("wrong registered selector: %+v", actual)
					}
					if kind == "valid" {
						if err != nil || result["available"] != true || result["recreation"] == nil {
							t.Fatal("native view unavailable", result, err)
						}
					} else if err == nil && (result["available"] != false || result["reason"] != "recreation_outcome_unknown_do_not_replay") {
						t.Fatal("invalid/lost evidence accepted", result)
					}
				})
			}
		}
	}
}

func TestFeedbackRecreationPreviewRejectsSubsetAndApplySelectors(t *testing.T) {
	_, anchor, _ := feedbackDiagnosticsFixture(t)
	for name, change := range map[string]func(*protocol.DiagnosticsRequest){"parent": func(q *protocol.DiagnosticsRequest) { q.ParentID = "" }, "id": func(q *protocol.DiagnosticsRequest) { q.ID = "id" }, "state": func(q *protocol.DiagnosticsRequest) { q.State = "done" }, "cursor": func(q *protocol.DiagnosticsRequest) { q.Cursor = "opaque" }, "cleanup_mode": func(q *protocol.DiagnosticsRequest) { q.CleanupMode = "manual" }, "workspace_id": func(q *protocol.DiagnosticsRequest) { q.WorkspaceID = "registered" }, "limit": func(q *protocol.DiagnosticsRequest) { q.Limit = 51 }} {
		t.Run(name, func(t *testing.T) {
			req := protocol.DiagnosticsRequest{Section: "recreation_preview", ParentID: "registered", Limit: 20}
			change(&req)
			if _, err := feedbackInventory(anchor, "installation", req); err == nil {
				t.Fatal("invalid selector ignored")
			}
		})
	}
}

func TestFeedbackRecreationCLIRejectsUnsafeSelectorsAndNeverStartsDaemon(t *testing.T) {
	root, anchor, _ := feedbackManualFixture(t)
	before, err := os.ReadFile(workspacegroup.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "missing"))
	env := []string{"HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "RADAR_SOCKET=" + os.Getenv("RADAR_SOCKET")}
	base := []string{"feedback-diagnostics", "--workspace", anchor, "--section", "recreation_preview", "--parent-id", "registered", "--json"}
	for _, extra := range [][]string{{"--parent-id", ""}, {"--id", ""}, {"--limit", "20"}, {"--cursor", ""}, {"--state", ""}, {"--workspace-id", ""}, {"--cleanup-mode", "manual"}, {"--since", ""}, {"--action", "apply"}, {"--action", "force"}, {"--scope", "all"}, {"--plan-id", ""}, {"--plan-id", strings.Repeat("a", 64)}, {"--action", "apply", "--plan-id", "recreation-preview:" + strings.Repeat("a", 64)}, {"--action", "apply", "--plan-id", strings.Repeat("A", 64)}} {
		if _, _, code := outputCLI(t, append(append([]string{}, base...), extra...), "", env...); code == 0 {
			t.Fatalf("unsafe recreation CLI selector: %v", extra)
		}
	}
	for _, extra := range [][]string{nil, {"--action", "preview"}, {"--scope", "installation"}, {"--scope", "feedback", "--action", "apply", "--plan-id", strings.Repeat("a", 64)}} {
		stdout, stderr, code := outputCLI(t, append(append([]string{}, base...), extra...), "", env...)
		if code != 0 || !strings.Contains(stdout, `"available":false`) || !strings.Contains(stdout, `"reason":"recreation_outcome_unknown_do_not_replay"`) || strings.Contains(stdout, `"inventory"`) || strings.Contains(stdout, `"recreation":`) {
			t.Fatalf("unknown recreation fabricated outcome: %d %s %s", code, stdout, stderr)
		}
	}
	after, err := os.ReadFile(workspacegroup.Path(root))
	if err != nil || string(before) != string(after) {
		t.Fatal("CLI changed registration")
	}
	if _, err := os.Stat(os.Getenv("RADAR_SOCKET")); !os.IsNotExist(err) {
		t.Fatal("CLI started daemon")
	}
}
