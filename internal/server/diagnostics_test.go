package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestDiagnosticsRPCIsBoundedReadOnlyAndDoesNotFilterHiddenState(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Attention: "done", Title: "private-title", SourceRefs: []protocol.SourceRef{{ID: "test:ABC-123", Source: "test", EntityID: "test:ABC-123", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", Metadata: map[string]string{"completed_at": "2026-01-02T10:20:30Z"}}}}})
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture must be hidden")
	}
	mutations := 0
	server := New(store, logger, func() { mutations++ }, func() error { mutations++; return nil }, nil, integration.NewRegistry(), cleanup.New(nil)).SetResourcePublication(func() { mutations++ }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} })
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.handle(host) }()
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	for _, req := range []protocol.Request{{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "tasks", Limit: 1}}, {Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "source_refs"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "bindings"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "tasks", ParentID: "must-not-be-ignored"}},
		{Method: "diagnostics"}, {Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "execute"}}} {
		if err := encoder.Encode(req); err != nil {
			t.Fatal(err)
		}
		var response protocol.Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if req.Diagnostics != nil && req.Diagnostics.ParentID == "" && (req.Diagnostics.Section == "tasks" || req.Diagnostics.Section == "source_refs" || req.Diagnostics.Section == "bindings") {
			expected := 1
			if req.Diagnostics.Section == "bindings" {
				expected = 0
			}
			if !response.OK || response.Diagnostics == nil || response.Diagnostics.Total != expected || response.Diagnostics.Schema != protocol.DiagnosticsVersion || !response.Diagnostics.ResourceRefreshPending {
				t.Fatalf("response=%+v", response)
			}
		} else if response.OK {
			t.Fatal("invalid request accepted")
		}
	}
	_ = client.Close()
	<-done
	if mutations != 0 {
		t.Fatal("inspection caused mutation or refresh")
	}
}
