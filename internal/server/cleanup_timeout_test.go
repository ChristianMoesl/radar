package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestCleanupRespondsWhenRefreshCommandLeavesChildHoldingOutput(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}

	// Model an unrelated Git verification command: the context kills its
	// parent, but a helper also owns the combined-output pipe.
	childPID := filepath.Join(t.TempDir(), "child.pid")
	t.Cleanup(func() {
		data, _ := os.ReadFile(childPID)
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	refreshErr := make(chan error, 1)
	s := New(store, logger, nil, nil, nil, integration.NewRegistry(), cleanup.New([]integration.CleanupProvider{fakeCleanupSource{name: "fake"}})).SetLocalRefresh(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		_, err := (workspace.ExecRunner{}).Run(ctx, "", "/bin/sh", "-c", `sleep 30 & echo "$!" > "$1"; wait`, "sh", childPID)
		refreshErr <- err
		store.SetTasks([]protocol.Task{})
	})
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		s.handle(serverConn)
	}()

	if err := clientConn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	request := protocol.Request{Method: "cleanup", Cleanup: &protocol.CleanupPreview{TaskID: 1, Targets: []protocol.CleanupTarget{{Source: "fake", Kind: "worktree"}}}}
	if err := json.NewEncoder(clientConn).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(clientConn).Decode(&response); err != nil {
		t.Fatalf("cleanup finished but its response was blocked by local refresh: %v", err)
	}
	t.Logf("cleanup response returned in %v after a 250ms refresh deadline", time.Since(start))
	if !response.OK || response.CleanupResult == nil || len(response.CleanupResult.Targets) != 1 || response.Tasks == nil || len(response.Tasks) != 0 {
		t.Fatalf("cleanup response = %+v, want success and refreshed tasks", response)
	}
	if err := <-refreshErr; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh error = %v, want deadline exceeded", err)
	}
	if _, err := os.Stat(childPID); err != nil {
		t.Fatalf("fixture did not spawn a child: %v", err)
	}
	_ = clientConn.Close()
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("server handler did not exit")
	}
}
