package agentservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// Model an absolute deadline whose timer notification has not arrived yet.
// No sleeps or Err call counts select the tested boundary.
type startAbsoluteContext struct {
	context.Context
	expired   func() bool
	liveUntil time.Time
}

func (c startAbsoluteContext) Deadline() (time.Time, bool) {
	if c.expired() {
		return time.Now().Add(-time.Second), true
	}
	return c.liveUntil, true
}

func TestNativeStartAbsoluteExpiryAfterCompletion(t *testing.T) {
	for _, kind := range []string{"expired", "cancelled", "native_error", "healthy"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			var expired atomic.Bool
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := startAbsoluteContext{Context: parent, expired: expired.Load, liveUntil: time.Now().Add(time.Hour)}
			nativeErr := errors.New("fixture native error after accepted preparation")
			calls := 0
			manager := New(runnerCompletionWorkspace{binding: reg.Workspace, prepare: func(c context.Context, req integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
				calls++
				nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
				if err := req.Complete(c, reg.Workspace); err != nil {
					t.Fatal("live completion failed", err)
				}
				switch kind {
				case "expired":
					expired.Store(true)
				case "cancelled":
					cancel()
				case "native_error":
					expired.Store(true)
					return reg.Workspace, integration.WorkspaceReconcilePlan{}, nativeErr
				}
				return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
			}})
			manager.retry[reg.ID] = retryState{attempts: 3, problem: "retained retry evidence"}
			err := manager.start(ctx, reg, protocol.AgentRequest{})
			if calls != 1 {
				t.Fatal("preparation replayed", calls)
			}
			switch kind {
			case "expired":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("expired native completion certified success", err)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost", err)
				}
			case "native_error":
				if !errors.Is(err, nativeErr) {
					t.Fatal("expiry erased original native error", err)
				}
			case "healthy":
				if err != nil {
					t.Fatal("live completion refused", err)
				}
			}
			got, err := load(reg.ID)
			if err != nil || !got.Desired || got.Workspace != reg.Workspace {
				t.Fatal("accepted durable intent rolled back/rebound", got, err)
			}
			// Desired persistence already accepted and cleared retry state. A late error
			// cannot restore it or imply rollback/permission to retry.
			if _, ok := manager.retry[reg.ID]; ok {
				t.Fatal("accepted desired persistence was undone")
			}
		})
	}
}

func TestNativeStartShorterCompletionAbsoluteExpiryIsSticky(t *testing.T) {
	reg := probeFixture(t)
	var expired atomic.Bool
	calls := 0
	manager := New(runnerCompletionWorkspace{binding: reg.Workspace, prepare: func(_ context.Context, req integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
		rawDiagnosticAgentPeer(t, reg, func(method string) string {
			if method == "hello" {
				return `{"jsonrpc":"2.0","id":1,"result":{"protocol":1,"instanceId":"worker"}}`
			}
			expired.Store(true)
			return `{"jsonrpc":"2.0","id":1,"result":` + nativeStatusSnapshot(reg) + `}`
		})
		c := startAbsoluteContext{Context: context.Background(), expired: expired.Load, liveUntil: time.Now().Add(time.Hour)}
		err := req.Complete(c, reg.Workspace)
		calls++
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Error("shorter completion context certified expired readiness", err)
		}
		// A provider swallowing this error cannot cause a successful outer start.
		return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
	}})
	if err := manager.start(context.Background(), reg, protocol.AgentRequest{}); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal("swallowed expired completion became success", err, calls)
	}
	got, err := load(reg.ID)
	if err != nil || !got.Desired {
		t.Fatal("accepted desired intent rolled back", err)
	}
}

func TestNativeStartAbsoluteExpiryBeforeLaunch(t *testing.T) {
	for _, expired := range []bool{true, false} {
		name := "live_launch_control"
		if expired {
			name = "expired_before_launch"
		}
		t.Run(name, func(t *testing.T) {
			node, err := exec.LookPath("node")
			if err != nil {
				t.Fatal("Node runtime required for native launch regression", err)
			}
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			marker, host := filepath.Join(t.TempDir(), "launched"), filepath.Join(t.TempDir(), "fixture.cjs")
			quoted, _ := json.Marshal(marker)
			if err := os.WriteFile(host, []byte("require('node:fs').writeFileSync("+string(quoted)+", 'launched');\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RADAR_AGENT_HOST", host)
			t.Setenv("PATH", filepath.Dir(node))
			logPath := filepath.Join(dir, "runner.log")
			ctx := startAbsoluteContext{Context: context.Background(), liveUntil: time.Now().Add(time.Hour), expired: func() bool { _, err := os.Stat(logPath); return expired && err == nil }}
			manager := New(workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) { return reg.Workspace, nil }, prepare: func(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
				return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
			}})
			err = manager.start(ctx, reg, protocol.AgentRequest{})
			if expired && !errors.Is(err, context.DeadlineExceeded) {
				t.Error("expired launch was not refused at final boundary", err)
			}
			if _, err := os.Stat(logPath); err != nil {
				t.Fatal("test did not reach final launcher preparation", err)
			}
			if _, err := os.Stat(marker); expired && !os.IsNotExist(err) {
				t.Fatal("expired caller launched native child", err)
			} else if !expired && err != nil {
				t.Fatal("live launch control did not run native child", err)
			}
			if !expired && (err == nil || errors.Is(err, context.DeadlineExceeded)) {
				t.Fatal("fixture child exit incorrectly became ready/expired", err)
			}
			got, err := load(reg.ID)
			if err != nil || !got.Desired {
				t.Fatal("accepted desired intent was rolled back", err)
			}

		})
	}
}

func TestNativeStartAbsoluteExpiryBeforeWorkspacePreparation(t *testing.T) {
	reg := probeFixture(t)
	var expired atomic.Bool
	calls := 0
	ctx := startAbsoluteContext{Context: context.Background(), expired: expired.Load, liveUntil: time.Now().Add(time.Hour)}
	manager := New(workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
		expired.Store(true)
		return reg.Workspace, nil
	}, prepare: func(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
		calls++
		return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
	}})
	if err := manager.start(ctx, reg, protocol.AgentRequest{}); err == nil {
		t.Fatal("expired native startup accepted")
	}
	if calls != 0 {
		t.Fatal("expired caller entered native preparation", calls)
	}
	got, err := load(reg.ID)
	if err != nil || got.Desired {
		t.Fatal("refused preparation changed desired state", err)
	}
}
