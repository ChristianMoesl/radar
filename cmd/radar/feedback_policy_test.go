package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestFeedbackWorkspacePolicyExactScopeAndVersion(t *testing.T) {
	root, anchor, _ := feedbackDiagnosticsFixture(t)
	origin := filepath.Join(root, "origin")
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	registry.Workspaces[0].FeedbackOrigin = origin
	registry.Workspaces = append(registry.Workspaces, workspacegroup.Workspace{ID: workspacegroup.ID(origin), Name: "origin", Path: origin})
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "fp-")
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
	observed := make(chan protocol.Request, 4)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 4; i++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			var req protocol.Request
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				conn.Close()
				done <- err
				return
			}
			observed <- req
			schema := protocol.DiagnosticsVersion
			if i == 3 {
				schema = 28
			}
			err = json.NewEncoder(conn).Encode(protocol.Response{OK: true, Diagnostics: &protocol.DiagnosticsPage{Schema: schema, Section: "workspace_policy", Evidence: "fresh_registration_recorded_recipe_not_runtime", Complete: true}})
			conn.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for index, scope := range []string{"origin", "feedback", "installation", "installation"} {
		req := protocol.DiagnosticsRequest{Section: "workspace_policy", ParentID: "exact-registered", Workspace: "/must-not-pass-through", Limit: 20}
		result, err := feedbackInventory(anchor, scope, req)
		actual := <-observed
		expected := ""
		if scope == "origin" {
			expected = origin
		}
		if scope == "feedback" {
			expected = anchor
		}
		if actual.Method != "diagnostics" || actual.Diagnostics.ParentID != req.ParentID || actual.Diagnostics.Workspace != expected {
			t.Fatalf("wrong exact scope: %+v", actual)
		}
		if actual.Diagnostics.Section != "workspace_policy" {
			t.Fatal("wrong section")
		}
		if index == 3 {
			if err == nil || result != nil {
				t.Fatal("old diagnostic schema accepted")
			}
			continue
		}
		if err != nil || result["available"] != true {
			t.Fatalf("read failed: %+v %v", result, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, req := range []protocol.DiagnosticsRequest{
		{Section: "workspace_policy", Limit: 20},
		{Section: "workspace_policy", ParentID: "registered", ID: "row", Limit: 20},
		{Section: "workspace_policy", ParentID: "registered", State: "done", Limit: 20},
		{Section: "workspace_policy", ParentID: "registered", Cursor: "opaque", Limit: 20},
	} {
		if _, err := feedbackInventory(anchor, "installation", req); err == nil {
			t.Fatal("selector accepted")
		}
	}
}
