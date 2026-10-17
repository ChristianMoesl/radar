package agentservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"radar/internal/protocol"
	"testing"
	"time"
)

func TestCrashBackoffAndIntentionallyStoppedRegistration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RADAR_AGENT_ROOT", root)
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
	for _, id := range []string{"crashing", "stopped"} {
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		reg := Registration{Version: 1, ID: id, Desired: id == "crashing", Workspace: protocol.AgentWorkspaceBinding{ID: "fixture", Path: root, Mode: "local"}}
		if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
			t.Fatal(err)
		}
	}
	manager := New(func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
		return protocol.AgentWorkspaceBinding{}, errors.New("fixture environment unavailable")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { manager.Run(ctx, nil); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		manager.mu.Lock()
		retry := manager.retry["crashing"]
		_, stopped := manager.retry["stopped"]
		manager.mu.Unlock()
		if stopped {
			t.Fatal("intentionally stopped registration was restarted")
		}
		if retry.attempts >= 2 {
			if time.Until(retry.next) < 3*time.Second {
				t.Fatal("crash backoff did not increase")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervisor did not retry failed runner")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	reg, err := load("stopped")
	if err != nil || reg.Desired {
		t.Fatalf("stopped intention changed: %+v %v", reg, err)
	}
}
