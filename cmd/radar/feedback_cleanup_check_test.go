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

func TestFeedbackCleanupCheckUnavailableIsExplicitNoStartupOrFallback(t *testing.T) {
	root, anchor, _ := feedbackDiagnosticsFixture(t)
	before, _ := os.ReadFile(workspacegroup.Path(root))
	for _, scope := range []string{"origin", "feedback", "installation"} {
		view, err := feedbackCleanupCheck(anchor, scope, "record")
		want := "check_outcome_unknown_do_not_replay"
		if scope == "origin" {
			want = "origin_not_registered"
		}
		if err != nil || view["available"] != false || view["reason"] != want || view["cleanup_check"] != nil {
			t.Fatalf("%s %+v %v", scope, view, err)
		}
	}
	after, _ := os.ReadFile(workspacegroup.Path(root))
	if string(before) != string(after) {
		t.Fatal("changed registration")
	}
	if _, err := os.Stat(os.Getenv("RADAR_SOCKET")); !os.IsNotExist(err) {
		t.Fatal("started daemon")
	}
	for _, scope := range []string{"", "all"} {
		if _, err := feedbackCleanupCheck(anchor, scope, "record"); err == nil {
			t.Fatal("bad scope accepted")
		}
	}
	for _, parent := range []string{"", strings.Repeat("p", 513)} {
		if _, err := feedbackCleanupCheck(anchor, "feedback", parent); err == nil {
			t.Fatal("bad parent accepted")
		}
	}
	if _, err := feedbackCleanupCheck(t.TempDir(), "feedback", "record"); err == nil {
		t.Fatal("unregistered caller accepted")
	}
}

func TestFeedbackCleanupCheckUsesExactTypedRPCRegisteredScopeAndNoReplay(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, kind := range []string{"valid", "numeric", "lost", "old_schema", "wrong_target", "wrong_operation", "wrong_mode", "wrong_scope"} {
			t.Run(scope+"/"+kind, func(t *testing.T) {
				root, anchor, _ := feedbackDiagnosticsFixture(t)
				origin := filepath.Join(root, "origin")
				if err := os.MkdirAll(origin, 0700); err != nil {
					t.Fatal(err)
				}
				reg, err := workspacegroup.Load(root)
				if err != nil {
					t.Fatal(err)
				}
				reg.Workspaces[0].FeedbackOrigin = origin
				reg.Workspaces = append(reg.Workspaces, workspacegroup.Workspace{ID: workspacegroup.ID(origin), Path: origin, Name: "Origin"})
				if err := workspacegroup.Save(root, reg); err != nil {
					t.Fatal(err)
				}
				dir, err := os.MkdirTemp("", "rc-")
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
				done := make(chan error, 1)
				seen := make(chan protocol.Request, 1)
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						done <- err
						return
					}
					defer conn.Close()
					var req protocol.Request
					if err = json.NewDecoder(conn).Decode(&req); err != nil {
						done <- err
						return
					}
					seen <- req
					if kind == "lost" {
						done <- nil
						return
					}
					view := &protocol.DiagnosticCleanupCheck{Schema: protocol.DiagnosticsVersion, Operation: "cleanup_check", Mode: "manual", Scope: scope, RecordID: "record", TaskID: 7, Evidence: "native_manual_cleanup_bundle_guard_check_not_execution_eligibility", Status: "guards_passed_at_check", TargetCount: 1}
					switch kind {
					case "old_schema":
						view.Schema--
					case "wrong_target":
						view.RecordID = "other"
					case "wrong_operation":
						view.Operation = "refresh"
					case "wrong_mode":
						view.Mode = "apply"
					case "wrong_scope":
						view.Scope = "all"
					}
					done <- json.NewEncoder(conn).Encode(protocol.Response{OK: true, CleanupCheck: view})
				}()
				parent := "record"
				if kind == "numeric" {
					parent = "7"
				}
				view, err := feedbackCleanupCheck(anchor, scope, parent)
				if kind == "valid" || kind == "numeric" {
					if err != nil || view["available"] != true {
						t.Fatalf("missing check %+v %v", view, err)
					}
				} else if kind == "lost" {
					if err != nil || view["reason"] != "check_outcome_unknown_do_not_replay" {
						t.Fatalf("lost reply %+v %v", view, err)
					}
				} else if err == nil {
					t.Fatal("invalid native identity/schema accepted", view)
				}
				req := <-seen
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				wantPath := ""
				if scope == "origin" {
					wantPath = origin
				}
				if scope == "feedback" {
					wantPath = anchor
				}
				expected := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup_check", Mode: "check", Scope: scope, ParentID: parent, Workspace: wantPath}
				if req.Method != "diagnostics-maintenance" || req.Maintenance == nil || *req.Maintenance != expected || req != (protocol.Request{Method: "diagnostics-maintenance", Maintenance: req.Maintenance}) {
					t.Fatalf("unexpected transport %+v", req)
				}
			})
		}
	}
}
