package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/version"
)

func TestAgentWorkspaceCLIProvisioningAndConfirmation(t *testing.T) {
	dir, err := os.MkdirTemp("", "rawc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requests := make(chan protocol.AgentRequest, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var req protocol.Request
			_ = json.NewDecoder(conn).Decode(&req)
			response := protocol.Response{OK: true, Version: version.Current()}
			if req.Agent != nil {
				requests <- *req.Agent
				if req.Agent.Preview {
					response.AgentResult, _ = json.Marshal(integration.WorkspaceReconcilePlan{PlanID: "accepted", WorkspaceID: "owned", Note: &integration.DesiredWorkspaceNote{Path: "/notes/agent.md", LinkingKey: "obsidian:task:fixture"}})
				} else {
					response.AgentResult, _ = json.Marshal(protocol.AgentStatus{ID: "fixture"})
				}
			}
			_ = json.NewEncoder(conn).Encode(response)
			_ = conn.Close()
		}
	}()
	for _, args := range [][]string{
		{"add", "./agent.ts", "--id", "fixture"},
		{"add", "./agent.ts", "--id", "fixture", "--workspace", "/explicit/workspace"},
		{"add", "./agent.ts", "--id", "fixture", "--preview"},
		{"add", "./agent.ts", "--id", "fixture", "--plan-id", "accepted", "--prepared-note", `{"path":"/notes/agent.md","linking_key":"obsidian:task:fixture"}`},
		{"start", "fixture", "--preview"},
		{"restart", "fixture", "--plan-id", "accepted"},
	} {
		stdout, stderr, code := outputCLI(t, append(append([]string{"agent"}, args...), "--json"), "", "RADAR_SOCKET="+path)
		if code != 0 || !json.Valid([]byte(stdout)) {
			t.Fatalf("%v: exit %d: %s %s", args, code, stdout, stderr)
		}
		req := <-requests
		if req.ID != "fixture" || req.Operation != args[0] {
			t.Fatalf("request changed: %+v", req)
		}
		for index, arg := range args {
			switch arg {
			case "--preview":
				if !req.Preview {
					t.Fatal("preview flag lost")
				}
			case "--workspace":
				if req.Workspace != args[index+1] {
					t.Fatal("explicit binding lost")
				}
			case "--plan-id":
				if req.PlanID != args[index+1] {
					t.Fatal("accepted plan lost")
				}
			case "--prepared-note":
				if string(req.PreparedNote) != args[index+1] {
					t.Fatal("prepared note lost")
				}
			}
		}
		if req.Operation == "add" && !filepath.IsAbs(req.Entrypoint) {
			t.Fatal("definition path is not absolute")
		}
	}
}
