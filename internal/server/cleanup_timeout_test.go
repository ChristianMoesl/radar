package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/taskservice"
)

func TestCleanupAcknowledgesWhileGlobalCollectionIsBlocked(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	service := taskservice.New(store, logger, integration.NewRegistry())
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var refreshes atomic.Int32
	server := New(store, logger, nil, nil, nil, integration.NewRegistry(), cleanup.New([]integration.CleanupProvider{fakeCleanupSource{name: "fake"}})).SetResourcePublication(service.MarkResourceRefreshPending, service.ResourceSnapshot).SetLocalRefresh(func() {
		if refreshes.Add(1) == 1 {
			close(started)
		}
		<-release
	})
	go func() { defer close(finished); server.localRefresh() }()
	<-started
	defer func() { close(release); <-finished }()
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go server.handle(serverConn)
	if err := clientConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	request := protocol.Request{Method: "cleanup", Cleanup: &protocol.CleanupPreview{TaskID: 1, Targets: []protocol.CleanupTarget{{Source: "fake", Kind: "worktree"}}}}
	if err := json.NewEncoder(clientConn).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(clientConn).Decode(&response); err != nil {
		t.Fatalf("completed removal waited for global collection: %v", err)
	}
	if !response.OK || response.CleanupResult == nil || len(response.CleanupResult.Targets) != 1 || !response.ResourceRefreshPending || !response.CleanupResult.ResourceRefreshPending {
		t.Fatalf("pending response: %+v", response)
	}
	if refreshes.Load() != 1 {
		t.Fatal("cleanup invoked another global refresh")
	}
}
