package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestFeedbackRecreationPreviewExactScopeVersionAndEvidence(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, kind := range []string{"valid", "old_schema", "wrong_target", "wrong_status", "missing_view", "pending", "lost"} {
			t.Run(scope+"/"+kind, func(t *testing.T) {
				root, anchor, _ := feedbackDiagnosticsFixture(t)
				origin := filepath.Join(root, "origin")
				reg, err := workspacegroup.Load(root)
				if err != nil {
					t.Fatal(err)
				}
				reg.Workspaces[0].FeedbackOrigin = origin
				reg.Workspaces = append(reg.Workspaces, workspacegroup.Workspace{ID: workspacegroup.ID(origin), Name: "origin", Path: origin})
				if err := workspacegroup.Save(root, reg); err != nil {
					t.Fatal(err)
				}
				dir, err := os.MkdirTemp("", "rp-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(dir)
				socket := filepath.Join(dir, "s")
				t.Setenv("RADAR_SOCKET", socket)
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				observed := make(chan protocol.Request, 1)
				done := make(chan error, 1)
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
					view := protocol.DiagnosticRecreationPreview{ID: "registered", PreviewID: "recreation-preview:" + strings.Repeat("a", 64), Status: "recreate", PreviousState: "running", RuntimeIdentityObserved: true, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "unavailable", LeaseEvidence: "unavailable"}
					page := protocol.DiagnosticsPage{Schema: protocol.DiagnosticsVersion, Section: "recreation_preview", Evidence: "explicit_native_exact_workspace_recreation_proposal", Total: 1, Complete: true, Items: []protocol.DiagnosticItem{{ID: "registered", RecreationPreview: &view}}}
					switch kind {
					case "old_schema":
						page.Schema--
					case "wrong_target":
						view.ID = "other"
					case "wrong_status":
						view.Status = "recreated"
					case "missing_view":
						page.Items[0].RecreationPreview = nil
					case "pending":
						page.ResourceRefreshPending = true
					}
					done <- json.NewEncoder(conn).Encode(protocol.Response{OK: true, Diagnostics: &page})
				}()
				req := protocol.DiagnosticsRequest{Section: "recreation_preview", ParentID: "registered", Workspace: "/must-not-be-forwarded", Limit: 20}
				result, err := feedbackInventory(anchor, scope, req)
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
				if actual.Method != "diagnostics-probe" || actual.Diagnostics.ParentID != req.ParentID || actual.Diagnostics.Workspace != expected || actual.Maintenance != nil || actual.Cleanup != nil {
					t.Fatalf("wrong native selector: %+v", actual)
				}
				if kind == "valid" {
					if err != nil || result["available"] != true {
						t.Fatalf("proposal unavailable: %+v %v", result, err)
					}
				} else if kind == "lost" {
					if err != nil || result["available"] != false || result["reason"] != "daemon_unavailable" {
						t.Fatal("lost reply fabricated evidence")
					}
				} else if err == nil {
					t.Fatal("invalid proposal accepted")
				}
			})
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
