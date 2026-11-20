package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/protocol"
)

func TestWorkspaceWatchHonorsDisabledCollection(t *testing.T) {
	t.Setenv("RADAR_DISABLE_COLLECTION", "1")
	done := watchWorkspacePresence(context.Background(), func() []protocol.Task {
		t.Fatal("disabled watcher read snapshot")
		return nil
	}, func() { t.Error("disabled watcher requested collection") }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	select {
	case <-done:
	default:
		t.Fatal("disabled watcher started background work")
	}
}

func TestWorkspaceWatchRootFailureRequestsRecoveryWithoutStarting(t *testing.T) {
	t.Setenv("RADAR_DISABLE_COLLECTION", "0")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.Mkdir(filepath.Join(dir, "radar"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "radar", "config.yaml"), []byte("workspace: [invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	done := watchWorkspacePresence(context.Background(), func() []protocol.Task { t.Fatal("unconfigured watcher read snapshot"); return nil }, func() { requests++ }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	select {
	case <-done:
	default:
		t.Fatal("unconfigured watcher started background work")
	}
	if requests != 1 {
		t.Fatalf("recovery requests=%d", requests)
	}
}
