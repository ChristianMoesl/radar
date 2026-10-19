package agentservice

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func collectionFixture(t *testing.T) (*Manager, Registration) {
	t.Helper()
	dir := agentRuntimeTestDir(t)
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(dir, "agents"))
	t.Setenv("RADAR_SOCKET", filepath.Join(dir, "radar.sock"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := Registration{Version: 1, ID: "subscriber", Collection: "github", Workspace: protocol.AgentWorkspaceBinding{ID: "fixture", Path: t.TempDir(), Mode: "local"}}
	instance, _ := instanceDir(reg.ID)
	if err := os.MkdirAll(instance, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(instance, "registration.json"), reg); err != nil {
		t.Fatal(err)
	}
	return New(func(context.Context, string) (protocol.AgentWorkspaceBinding, error) { return reg.Workspace, nil }), reg
}

type collectedGitHub struct {
	result integration.CollectResult
}

func (s collectedGitHub) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "github"}
}
func (s collectedGitHub) CollectionItems(observations []integration.Observation) []integration.CollectionItem {
	items := make([]integration.CollectionItem, 0, len(observations))
	for _, observation := range observations {
		ref := observation.Ref
		payload, _ := json.Marshal(map[string]string{"author": ref.Metadata["author"], "state": ref.Metadata["pr_state"], "headOid": ref.Metadata["head_oid"]})
		items = append(items, integration.CollectionItem{Key: ref.ID, Payload: payload})
	}
	return items
}

func (s collectedGitHub) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return s.result
}
func (s collectedGitHub) Status(context.Context, *slog.Logger) integration.StatusResult {
	return integration.StatusResult{Status: protocol.SourceStatus{Name: "github", Status: "ok"}, CanRun: true}
}
func (s collectedGitHub) Reconcile(context.Context, integration.ReconcileRequest) []integration.Observation {
	return nil
}

type collectionSink struct{ *Manager }

func (collectionSink) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "agent"}
}
func (collectionSink) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{Complete: true}
}

func TestCollectionRefreshPersistsChangesAcrossStoppedAgentsAndRestart(t *testing.T) {
	m, reg := collectionFixture(t)
	ref := protocol.SourceRef{Source: "github", ID: "github:pr:acme/app:1", Repo: "acme/app", Kind: "pull_request", Title: "Update dependency", Branch: "renovate/library", URL: "https://github.com/acme/app/pull/1", Metadata: map[string]string{"pr_node_id": "PR_1", "head_oid": "aaa", "pr_state": "OPEN", "pr_draft": "false", "author": "renovate", "author_type": "Bot"}}
	refresh := func(complete bool, refs ...protocol.SourceRef) {
		t.Helper()
		observations := make([]integration.Observation, len(refs))
		for i, ref := range refs {
			observations[i] = integration.Observation{Ref: ref}
		}
		collector.Collect(context.Background(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []integration.Source{collectedGitHub{result: integration.CollectResult{Complete: complete, Observations: observations}}, collectionSink{m}})
	}
	state := func() collectionState {
		t.Helper()
		s, err := loadCollection(reg)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	refresh(false, ref)
	if state().Initialized {
		t.Fatal("partial collection established a baseline")
	}
	refresh(true, ref)
	initial := state()
	if len(initial.Pending) != 1 {
		t.Fatalf("baseline not persisted: %+v", initial)
	}
	var event struct {
		Change   string
		Baseline bool
		Data     struct{ Author, State, HeadOID string }
	}
	if err := json.Unmarshal(initial.Pending[0].Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Change != "snapshot" || !event.Baseline || event.Data.Author != "renovate" || event.Data.State != "OPEN" || event.Data.HeadOID != "aaa" {
		t.Fatalf("incomplete source facts: %+v", event)
	}
	refresh(true, ref)
	refresh(true) // Even a successful empty collection is not evidence of closure.
	if len(state().Pending) != 1 {
		t.Fatal("unchanged or absent PR emitted an event")
	}
	ref.Metadata["head_oid"] = "bbb"
	refresh(false, ref)
	if len(state().Pending) != 1 {
		t.Fatal("partial refresh emitted changes")
	}
	// A new manager has no memory of the old collector or queue.
	m = New(m.workspace)
	refresh(true, ref)
	changed := state()
	if len(changed.Pending) != 2 || changed.Pending[1].ID == initial.Pending[0].ID {
		t.Fatal("changed head did not produce a distinct durable event")
	}
	_ = json.Unmarshal(changed.Pending[1].Payload, &event)
	if event.Change != "changed" || !event.Baseline {
		t.Fatal("baseline provenance lost on an updated PR")
	}
	ref.Metadata["head_oid"] = "aaa"
	refresh(true, ref)
	if s := state(); len(s.Pending) != 3 || s.Pending[2].ID == s.Pending[0].ID {
		t.Fatal("A → B → A reused an old event identity")
	}
	ref.Metadata["pr_state"] = "CLOSED"
	refresh(true, ref)
	if len(state().Pending) != 4 {
		t.Fatal("explicit terminal fact was not delivered")
	}
	if err := m.flushCollection(context.Background(), reg); err == nil {
		t.Fatal("unavailable runner accepted delivery")
	}
	var status protocol.AgentStatus
	m.collectionStatus(reg, &status)
	if status.CollectionPending != 4 || status.CollectionError == "" {
		t.Fatalf("pending failure not visible: %+v", status)
	}
}

func TestCollectionLostReceiptRetainsIdenticalEventUntilMatchingAdmission(t *testing.T) {
	m, reg := collectionFixture(t)
	if err := m.ObserveCollection(context.Background(), "github", []integration.CollectionItem{{Key: "github:pr:acme/app:1", Payload: json.RawMessage(`{"state":"OPEN"}`)}}); err != nil {
		t.Fatal(err)
	}
	path, _ := socketPath(reg.ID)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	wires := make(chan string, 3)
	go func() {
		for attempt := 0; attempt < 3; attempt++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wire, _ := bufio.NewReader(conn).ReadBytes('\n')
			var request struct{ Params collectionEvent }
			_ = json.Unmarshal(wire, &request)
			data, _ := json.Marshal(request.Params)
			wires <- string(data)
			if attempt > 0 {
				id := request.Params.ID
				if attempt == 1 {
					id = "wrong-event"
				}
				response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"taskId": 7, "source": request.Params.Source, "eventId": id}})
				_, _ = conn.Write(append(response, '\n'))
			}
			conn.Close()
		}
	}()
	if err := m.flushCollection(context.Background(), reg); err == nil {
		t.Fatal("lost response was accepted")
	}
	m = New(m.workspace)
	if err := m.flushCollection(context.Background(), reg); err == nil {
		t.Fatal("mismatched receipt removed pending work")
	}
	if err := m.flushCollection(context.Background(), reg); err != nil {
		t.Fatal(err)
	}
	one, two, three := <-wires, <-wires, <-wires
	if one != two || one != three {
		t.Fatal("retry changed identity or content")
	}
	state, err := loadCollection(reg)
	if err != nil || len(state.Pending) != 0 || state.Problem != "" {
		t.Fatalf("matching receipt not checkpointed: %+v, %v", state, err)
	}
	if err := m.ObserveCollection(context.Background(), "github", []integration.CollectionItem{{Key: "github:pr:acme/app:1", Payload: json.RawMessage(`{"state":"OPEN"}`)}}); err != nil {
		t.Fatal(err)
	}
	state, _ = loadCollection(reg)
	if len(state.Pending) != 0 {
		t.Fatal("restart/unchanged refresh requeued an admitted event")
	}
}

func TestInvalidCollectionCheckpointIsPreserved(t *testing.T) {
	m, reg := collectionFixture(t)
	data := []byte(`{"version":999}`)
	if err := os.WriteFile(collectionPath(reg), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.ObserveCollection(context.Background(), "github", nil); err == nil {
		t.Fatal("unsupported checkpoint silently reset")
	}
	after, _ := os.ReadFile(collectionPath(reg))
	if string(after) != string(data) {
		t.Fatal("checkpoint changed")
	}
}

func TestRestoringRegistrationRequiresItsRetainedCollection(t *testing.T) {
	m, reg := collectionFixture(t)
	if err := m.ObserveCollection(context.Background(), "github", []integration.CollectionItem{{Key: "github:pr:acme/app:1", Payload: json.RawMessage(`{"state":"OPEN"}`)}}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(collectionPath(reg))
	if _, err := m.Handle(context.Background(), protocol.AgentRequest{Operation: "remove", ID: reg.ID}); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(t.TempDir(), "agent.ts")
	if err := os.WriteFile(entry, []byte("export default {};"), 0600); err != nil {
		t.Fatal(err)
	}
	request := protocol.AgentRequest{Operation: "add", ID: reg.ID, Entrypoint: entry, Workspace: reg.Workspace.Path}
	if _, err := m.Handle(context.Background(), request); err == nil {
		t.Fatal("restoration stranded its retained collection")
	}
	request.Collection = "github"
	if _, err := m.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(collectionPath(reg))
	if string(before) != string(after) {
		t.Fatal("restoration rewrote pending deliveries")
	}
}
