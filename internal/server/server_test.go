package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar.nvim/internal/protocol"
	"radar.nvim/internal/state"
)

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
