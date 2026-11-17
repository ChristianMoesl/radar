package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

type overviewNoProbe struct{ calls *int }

func (p overviewNoProbe) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "test"}
}
func (p overviewNoProbe) Status(context.Context, *slog.Logger) integration.StatusResult {
	*p.calls++
	return integration.StatusResult{}
}
func (p overviewNoProbe) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	*p.calls++
	return integration.CollectResult{}
}
func (p overviewNoProbe) FilterTasks(tasks []protocol.Task, _ *slog.Logger) []protocol.Task {
	*p.calls++
	return tasks
}

func TestDiagnosticOverviewReadsDaemonProcessPolicyWithoutProvidersOrMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_STATE", filepath.Join(home, "state.db"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(home, "agents"))
	path, _ := config.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	private := "private-command-token-note-ABC-123"
	data := []byte("workspace: {auto_confirm: false}\nsbx: {env_file: /" + private + ", ready_command: [" + private + "]}\nmodel: " + private + "\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// A FIFO would hang any accidental secrets-file read. The normal config
	// reader does not follow env_file, execute ready_command, or load secrets.
	if err := syscall.Mkfifo(filepath.Join(filepath.Dir(path), "secrets.yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Title: private, SourceRefs: []protocol.SourceRef{{ID: private, Source: "test", Path: "/" + private}}}})
	store.SetSources([]protocol.SourceStatus{{Name: "test", Status: "partial", Detail: private, SourceRefCount: 123}, {Name: "other", Status: private}})
	before, _ := json.Marshal(store.Tasks())
	revision := store.Revision()
	native := config.DiagnosticSettings()
	native.Scope = "daemon_process_global_defaults"
	effects := 0
	server := New(store, logger, func() { effects++ }, func() error { effects++; return nil }, nil, integration.NewRegistry(overviewNoProbe{&effects}), cleanup.New(nil)).SetResourcePublication(func() { effects++ }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} })
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.handle(host) }()
	defer func() { client.Close(); <-done }()
	encode, decode := json.NewEncoder(client), json.NewDecoder(client)
	request := protocol.DiagnosticsRequest{Section: "overview"}
	cases := []protocol.DiagnosticsRequest{request}
	for _, modify := range []func(*protocol.DiagnosticsRequest){func(q *protocol.DiagnosticsRequest) { q.Workspace = "/private" }, func(q *protocol.DiagnosticsRequest) { q.ID = "id" }, func(q *protocol.DiagnosticsRequest) { q.ParentID = "parent" }, func(q *protocol.DiagnosticsRequest) { q.State = "done" }, func(q *protocol.DiagnosticsRequest) { q.Cursor = "cursor" }, func(q *protocol.DiagnosticsRequest) { q.Limit = 1 }, func(q *protocol.DiagnosticsRequest) { q.CleanupMode = "manual" }, func(q *protocol.DiagnosticsRequest) { q.WorkspaceID = "workspace" }} {
		q := request
		modify(&q)
		cases = append(cases, q)
	}
	for i, q := range cases {
		if err := encode.Encode(protocol.Request{Method: "diagnostics", Diagnostics: &q}); err != nil {
			t.Fatal(err)
		}
		var result protocol.Response
		if err := decode.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			if result.OK || result.DiagnosticOverview != nil {
				t.Fatalf("selectors ignored: %+v", q)
			}
			continue
		}
		view := result.DiagnosticOverview
		if !result.OK || view == nil || view.Schema != protocol.DiagnosticsVersion || !result.ResourceRefreshPending || result.Revision != revision || result.Tasks != nil || result.Sources != nil || result.Diagnostics != nil {
			t.Fatalf("invalid overview: %+v", result)
		}
		policy := view.Settings
		policy.CapturedAt = native.CapturedAt
		if !reflect.DeepEqual(policy, native) {
			t.Fatalf("not the same native allowlist/provenance: %+v != %+v", policy, native)
		}
		if len(view.Providers) != 2 || view.Providers[0].Status != "partial" || !view.Providers[0].DetailsAvailable || view.Providers[1].Status != "unknown" {
			t.Fatalf("status facts lost: %+v", view.Providers)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), private) || len(raw) > 32*1024 {
			t.Fatalf("private or unbounded response: %s", raw)
		}
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(data) || store.Revision() != revision {
		t.Fatal("overview mutated configuration or task revision")
	}
	// Reread policy rather than asserting that startup values are still loaded.
	if err := os.WriteFile(path, []byte("workspace: {auto_confirm: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := encode.Encode(protocol.Request{Method: "diagnostics", Diagnostics: &request}); err != nil {
		t.Fatal(err)
	}
	var fresh protocol.Response
	if err := decode.Decode(&fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.DiagnosticOverview == nil || fresh.DiagnosticOverview.Settings.Revision == native.Revision || fresh.DiagnosticOverview.Settings.Settings[0].Value != "true" {
		t.Fatal("stale daemon process projection")
	}
	// Unsafe config remains unavailable, not compiled defaults or CLI fallback.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+"-missing", path); err != nil {
		t.Fatal(err)
	}
	if err := encode.Encode(protocol.Request{Method: "diagnostics", Diagnostics: &request}); err != nil {
		t.Fatal(err)
	}
	var invalid protocol.Response
	if err := decode.Decode(&invalid); err != nil {
		t.Fatal(err)
	}
	if !invalid.OK || invalid.DiagnosticOverview == nil || invalid.DiagnosticOverview.Settings.Available || invalid.DiagnosticOverview.Settings.Scope != "daemon_process_global_defaults" || len(invalid.DiagnosticOverview.Settings.Settings) != 0 {
		t.Fatalf("unsafe policy not unknown: %+v", invalid)
	}
	// Too many/unnamed providers are unavailable rather than partial evidence.
	statuses := make([]protocol.SourceStatus, 33)
	for i := range statuses {
		statuses[i].Name = "test"
	}
	oversized := make([]protocol.SourceStatus, 32)
	for i := range oversized {
		// JSON escaping, not just raw text length, participates in the budget.
		oversized[i].Name = strings.Repeat("\x00", 512)
	}
	for _, sources := range [][]protocol.SourceStatus{statuses, {{Name: ""}}, oversized} {
		store.SetSources(sources)
		if err := encode.Encode(protocol.Request{Method: "diagnostics", Diagnostics: &request}); err != nil {
			t.Fatal(err)
		}
		var result protocol.Response
		if err := decode.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.OK || result.DiagnosticOverview != nil {
			t.Fatal("provider coverage silently truncated")
		}
	}
	after, _ := json.Marshal(store.Tasks())
	if string(before) != string(after) || effects != 0 {
		t.Fatal("overview invoked provider/filter/mutation")
	}
	if _, err := os.Stat(filepath.Join(home, "agents")); !os.IsNotExist(err) {
		t.Fatal("overview read/created agent state")
	}
}
