package workspacegc

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
)

type borrowedGCPreviewProvider struct {
	gcProvider
	borrowed *[]protocol.CleanupTarget
	observed *[]protocol.CleanupTarget
	block    bool
}

func (p borrowedGCPreviewProvider) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	targets, err := p.gcProvider.PreviewCleanup(ctx, req)
	for i := range targets {
		targets[i].Operation = map[string]string{"native_lifetime": "original"}
		targets[i].Safety = []protocol.CleanupSafety{{Kind: "structural", Summary: "original", Message: "original structural guard", BlocksAutomatic: p.block}}
	}
	*p.borrowed = targets
	return targets, err
}
func (p borrowedGCPreviewProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	*p.observed = append(*p.observed, req.Target)
	return p.gcProvider.Cleanup(ctx, req)
}

func TestGCPreviewOwnershipPreservesNativeBundle(t *testing.T) {
	for _, stage := range []string{"retire", "expire"} {
		for _, boundary := range []string{"later-preview", "lifecycle-guard"} {
			for _, mutate := range []bool{false, true} {
				t.Run(stage+"/"+boundary+"/"+map[bool]string{false: "stable", true: "rewritten"}[mutate], func(t *testing.T) {
					store, root, done, _ := expiryFixture(t)
					t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
					t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
					var calls []cleanupCall
					var borrowed, observed []protocol.CleanupTarget
					rewrite := func() {
						if !mutate {
							return
						}
						if len(borrowed) != 1 {
							t.Fatalf("missing original target: %+v", borrowed)
						}
						borrowed[0].Operation["native_lifetime"] = "replacement"
						borrowed[0].Safety[0].Summary = "replacement"
					}
					provider := borrowedGCPreviewProvider{gcProvider: gcProvider{name: "tmux", calls: &calls}, borrowed: &borrowed, observed: &observed}
					workspace := expiryProvider{gcProvider: gcProvider{name: "workspace", calls: &calls}}
					if boundary == "later-preview" {
						workspace.previewHook = rewrite
					}
					guards := 0
					options := Options{WorkspaceRoot: root, GuardExecution: func(_ context.Context, _ protocol.Task, execute func() error) error {
						guards++
						if boundary == "lifecycle-guard" {
							rewrite()
						}
						return execute()
					}}
					now := done.Add(25 * time.Hour)
					if stage == "expire" {
						now = done.Add(ExpiryRetention)
					}
					result, err := Run(context.Background(), store, cleanup.New([]integration.CleanupProvider{provider, workspace}), nil, now, options)
					if err != nil || guards != 1 || len(result.Skipped) != 0 || len(calls) != 2 || len(observed) != 1 || len(result.Retired)+len(result.Deleted) != 1 {
						t.Fatalf("native bundle blocked: %+v %v calls=%+v observed=%+v guards=%d", result, err, calls, observed, guards)
					}
					if calls[0].source != "tmux" || calls[1].source != "workspace" || observed[0].Operation["native_lifetime"] != "original" || observed[0].Safety[0].Summary != "original" {
						t.Fatalf("borrowed values changed native dispatch: calls=%+v observed=%+v", calls, observed)
					}
					if mutate && borrowed[0].Operation["native_lifetime"] != "replacement" {
						t.Fatal("fixture did not rewrite provider-owned evidence")
					}
				})
			}
		}
	}
}

func TestGCPreviewOwnershipCannotEraseStructuralBlocker(t *testing.T) {
	for _, stage := range []string{"retire", "expire"} {
		t.Run(stage, func(t *testing.T) {
			store, root, done, _ := expiryFixture(t)
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
			var calls []cleanupCall
			var borrowed, observed []protocol.CleanupTarget
			provider := borrowedGCPreviewProvider{gcProvider: gcProvider{name: "tmux", calls: &calls}, borrowed: &borrowed, observed: &observed, block: true}
			rewritten := false
			workspace := expiryProvider{gcProvider: gcProvider{name: "workspace", calls: &calls}, previewHook: func() {
				if len(borrowed) != 1 {
					t.Fatal("earlier preview not observed")
				}
				borrowed[0].Safety[0].BlocksAutomatic = false
				rewritten = true
			}}
			now := done.Add(25 * time.Hour)
			if stage == "expire" {
				now = done.Add(ExpiryRetention)
			}
			result, err := Run(context.Background(), store, cleanup.New([]integration.CleanupProvider{provider, workspace}), nil, now, Options{WorkspaceRoot: root})
			if err != nil || !rewritten || len(calls) != 0 || len(observed) != 0 || len(result.Retired)+len(result.Deleted) != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "original structural guard") {
				t.Fatalf("later provider erased native blocker: %+v %v calls=%+v observed=%+v", result, err, calls, observed)
			}
			if reflect.DeepEqual(borrowed[0].Safety, []protocol.CleanupSafety{{Kind: "structural", Summary: "original", Message: "original structural guard", BlocksAutomatic: true}}) {
				t.Fatal("blocker was not rewritten by fixture")
			}
		})
	}
}
