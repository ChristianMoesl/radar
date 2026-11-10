package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"radar/internal/integration"
)

type unavailableRemovalStatus struct{ removed bool }

func (*unavailableRemovalStatus) LookPath(string) error { return nil }
func (r *unavailableRemovalStatus) Run(_ context.Context, _ string, _ string, args ...string) (string, error) {
	if strings.Contains(strings.Join(args, " "), "status --porcelain") {
		return "", errors.New("status unavailable")
	}
	if strings.Contains(strings.Join(args, " "), "worktree remove") {
		r.removed = true
	}
	return "", nil
}

func TestOnlyConfirmedRemovalCanProceedAfterStatusFailure(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupExpired, integration.CleanupConfirmed} {
		runner := &unavailableRemovalStatus{}
		_, err := RemoveWorktree(context.Background(), runner, t.TempDir(), mode)
		if mode == integration.CleanupConfirmed {
			if err != nil || !runner.removed {
				t.Fatalf("confirmed removal blocked: %v", err)
			}
		} else if err == nil || runner.removed {
			t.Fatalf("automatic mode %v bypassed inspection", mode)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &unavailableRemovalStatus{}
	if _, err := RemoveWorktree(ctx, runner, t.TempDir(), integration.CleanupConfirmed); !errors.Is(err, context.Canceled) || runner.removed {
		t.Fatal("canceled confirmation performed deletion", err)
	}
}
