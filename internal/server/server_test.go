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
		New(store, logger, nil, nil).handle(server)
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
		New(store, logger, nil, nil).handle(server)
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

func TestDeletePreviewDelegatesToDeletableSource(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Title: "deletable", SourceRefs: []protocol.SourceRef{{ID: "fake:ref:1", Source: "fake", Kind: "thing", Path: "/tmp/item"}}}})

	preview, err := NewWithSources(store, logger, nil, nil, []integration.Source{fakeDeleteSource{}}).deletePreview(context.Background(), 1, protocol.CurrentContext{CWD: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if preview.SourceRefID != "fake:ref:1" || preview.Path != "/tmp/item" {
		t.Fatalf("delete preview = %+v", preview)
	}
}

func TestDeleteDelegatesToPreviewSource(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}

	result, err := NewWithSources(store, logger, nil, nil, []integration.Source{fakeDeleteSource{}}).delete(context.Background(), &protocol.DeletePreview{Source: "fake", SourceRefID: "fake:ref:1", Path: "/tmp/item"})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceRefID != "fake:ref:1" || result.Path != "/tmp/item" {
		t.Fatalf("delete result = %+v", result)
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
		New(store, logger, nil, nil).handle(server)
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

type fakeDeleteSource struct{}

func (fakeDeleteSource) Name() string { return "fake" }

func (fakeDeleteSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}

func (fakeDeleteSource) PreviewDelete(_ context.Context, req integration.DeletePreviewRequest) (protocol.DeletePreview, bool, error) {
	for _, ref := range req.Task.SourceRefs {
		if ref.Source == "fake" {
			return protocol.DeletePreview{TaskID: req.Task.ID, SourceRefID: ref.ID, Source: ref.Source, Kind: ref.Kind, Path: ref.Path}, true, nil
		}
	}
	return protocol.DeletePreview{}, false, nil
}

func (fakeDeleteSource) Delete(_ context.Context, preview protocol.DeletePreview) (protocol.DeleteResult, error) {
	return protocol.DeleteResult{SourceRefID: preview.SourceRefID, Source: preview.Source, Kind: preview.Kind, Path: preview.Path}, nil
}
