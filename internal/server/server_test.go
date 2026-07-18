package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestWatchOldRevisionReturnsTasksImmediately(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Title: "one", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:org/repo:1", Source: "github", Kind: "pull_request"}}}})

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(store, logger, nil, nil, nil, cleanup.New(nil)).handle(server)
	}()
	if _, err := client.Write([]byte("{\"method\":\"watch:0\"}\n")); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	<-done

	if !response.OK || response.Revision != 1 || len(response.Tasks) != 1 {
		t.Fatalf("watch response = %+v, want revision 1 with tasks", response)
	}
}

func TestWatchCurrentRevisionTimesOutWithoutTasks(t *testing.T) {
	previousTimeout := watchTimeout
	watchTimeout = 10 * time.Millisecond
	defer func() { watchTimeout = previousTimeout }()

	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Title: "one", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:org/repo:1", Source: "github", Kind: "pull_request"}}}})

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(store, logger, nil, nil, nil, cleanup.New(nil)).handle(server)
	}()
	if _, err := client.Write([]byte("{\"method\":\"watch:1\"}\n")); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	<-done

	if !response.OK || response.Revision != 1 || response.Tasks != nil {
		t.Fatalf("watch timeout response = %+v, want revision only", response)
	}
}

func TestGarbageCollectionReturnsCallbackResult(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	garbageCollect := func() (protocol.GarbageCollectionResult, error) {
		return protocol.GarbageCollectionResult{Deleted: []protocol.GarbageCollectionItem{{TaskID: 7, Path: "/workspaces/ship"}}}, nil
	}

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(store, logger, nil, nil, garbageCollect, cleanup.New(nil)).handle(serverConn)
	}()
	if _, err := clientConn.Write([]byte("{\"method\":\"gc\"}\n")); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(clientConn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	_ = clientConn.Close()
	<-done

	if !response.OK || response.GarbageCollectionResult == nil || len(response.GarbageCollectionResult.Deleted) != 1 {
		t.Fatalf("gc response = %+v, want one deleted workspace", response)
	}
}

func TestCleanupPreviewCollectsEveryLocalTarget(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Title: "cleanup", SourceRefs: []protocol.SourceRef{
		{ID: "fake-a:ref:1", Source: "fake-a", Kind: "thing", Path: "/tmp/one"},
		{ID: "fake-b:ref:2", Source: "fake-b", Kind: "thing", Path: "/tmp/two"},
	}}})

	preview, err := New(store, logger, nil, nil, nil, cleanup.New([]integration.CleanupProvider{
		fakeCleanupSource{name: "fake-a"},
		fakeCleanupSource{name: "fake-b"},
	})).cleanupPreview(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Targets) != 2 || preview.Targets[0].Path != "/tmp/one" || preview.Targets[1].Path != "/tmp/two" {
		t.Fatalf("cleanup preview = %+v", preview)
	}
}

func TestCleanupExecutesEveryPreviewTarget(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	preview := protocol.CleanupPreview{TaskID: 1, Targets: []protocol.CleanupTarget{
		{Source: "fake", SourceRefID: "fake:ref:1", Path: "/tmp/one"},
		{Source: "fake", SourceRefID: "fake:ref:2", Path: "/tmp/two"},
	}}
	result, err := New(store, logger, nil, nil, nil, cleanup.New([]integration.CleanupProvider{fakeCleanupSource{name: "fake"}})).cleanup(context.Background(), &preview)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 2 || result.Targets[0].Path != "/tmp/one" || result.Targets[1].Path != "/tmp/two" {
		t.Fatalf("cleanup result = %+v", result)
	}
}

func TestAckResponseAppliesFilters(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("RADAR_STATE", filepath.Join(tmp, "state", "tasks.json"))

	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"filters":{"mute_repos":["org/noisy"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{
		{Repo: "org/noisy", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:org/noisy:1", Source: "github", Kind: "pull_request", Repo: "org/noisy"}}},
		{Repo: "org/useful", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:org/useful:2", Source: "github", Kind: "pull_request", Repo: "org/useful"}}},
	})

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(store, logger, nil, nil, nil, cleanup.New(nil)).handle(server)
	}()

	if _, err := client.Write([]byte("{\"method\":\"ack:2\"}\n")); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server handler did not exit")
	}

	if !response.OK {
		t.Fatalf("response not ok: %s", response.Error)
	}
	if len(response.Tasks) != 1 || response.Tasks[0].Repo != "org/useful" {
		t.Fatalf("ack response tasks = %+v, want only useful task", response.Tasks)
	}
	if response.Summary == nil || response.Summary.Attention != 1 {
		t.Fatalf("summary = %+v, want one attention task", response.Summary)
	}
}

type fakeCleanupSource struct{ name string }

func (f fakeCleanupSource) Name() string { return f.name }

func (fakeCleanupSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}

func (f fakeCleanupSource) PreviewCleanup(_ context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	targets := make([]protocol.CleanupTarget, 0)
	for _, ref := range req.Task.SourceRefs {
		if ref.Source == f.name {
			targets = append(targets, protocol.CleanupTarget{SourceRefID: ref.ID, Source: ref.Source, Kind: ref.Kind, Path: ref.Path})
		}
	}
	return targets, nil
}

func (fakeCleanupSource) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return req.Target, nil
}
