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

func TestFeedbackRepositoryPolicyExactScopeAndVersion(t *testing.T) {
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
				schema = 29
			}
			err = json.NewEncoder(conn).Encode(protocol.Response{OK: true, Diagnostics: &protocol.DiagnosticsPage{Schema: schema, Section: "repository_policy", Evidence: "explicit_current_registered_member_repository_declarations", Complete: true}})
			conn.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for index, scope := range []string{"origin", "feedback", "installation", "installation"} {
		req := protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: "exact-registered", ID: "workspace-resource:" + strings.Repeat("a", 64), Workspace: "/must-not-pass-through", Limit: 20}
		result, err := feedbackInventory(anchor, scope, req)
		actual := <-observed
		expected := ""
		if scope == "origin" {
			expected = origin
		}
		if scope == "feedback" {
			expected = anchor
		}
		if actual.Method != "diagnostics-probe" || actual.Diagnostics.ParentID != req.ParentID || actual.Diagnostics.Workspace != expected {
			t.Fatalf("wrong exact scope: %+v", actual)
		}
		if actual.Diagnostics.Section != "repository_policy" {
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
		{Section: "repository_policy", Limit: 20},
		{Section: "repository_policy", ParentID: "registered", Limit: 20},
		{Section: "repository_policy", ParentID: "registered", ID: "arbitrary-path", Limit: 20},
		{Section: "repository_policy", ParentID: "registered", ID: "workspace-resource:" + strings.Repeat("a", 64), State: "done", Limit: 20},
		{Section: "repository_policy", ParentID: "registered", ID: "workspace-resource:" + strings.Repeat("a", 64), Cursor: "opaque", Limit: 20},
	} {
		if _, err := feedbackInventory(anchor, "installation", req); err == nil {
			t.Fatal("selector accepted")
		}
	}
}
