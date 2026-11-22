package tmux

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/protocol"
)

func TestBundleCheckUsesNativeTmuxAdmissionWithoutRuntimeEffects(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		for _, outcome := range []string{"success", "busy-first", "busy-later", "stale-runtime", "cancel-before"} {
			t.Run(fmt.Sprintf("mode=%d/%s", mode, outcome), func(t *testing.T) {
				runner := admissionFixture(t)
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", ResourceID: "first-effect"}}}
				for _, name := range []string{"workspace-alpha", "workspace-beta"} {
					target := sessionTarget(runner, name)
					if mode == integration.CleanupRetire {
						target.ResourceID = name
						target.Operation = map[string]string{"runtime_id": runner.sessions[name].Key()}
					}
					preview.Targets = append(preview.Targets, target)
				}
				var unblock func()
				switch outcome {
				case "busy-first", "busy-later":
					name := "workspace-alpha"
					if outcome == "busy-later" {
						name = "workspace-beta"
					}
					active, err := prepareAttach(ctx, runner, integration.SessionTarget{Name: name})
					if err != nil {
						t.Fatal(err)
					}
					unblock = active.Release
					defer unblock()
				case "stale-runtime":
					preview.Targets[2].SourceRefID = "tmux:session:99:34:$8"
					if mode == integration.CleanupRetire {
						preview.Targets[2].Operation["runtime_id"] = "tmux:session:99:34:$8"
					}
				case "cancel-before":
					cancel()
				}
				effects := 0
				service := cleanup.New([]integration.CleanupProvider{effectProvider{calls: &effects}, fixtureAdmissionProvider{runner: runner}})
				err := service.CheckBundle(ctx, preview, cleanup.ExecuteOptions{Mode: mode})
				if (err == nil) != (outcome == "success") || effects != 0 || len(runner.killed) != 0 || len(runner.switched) != 0 {
					t.Fatal("native check bypassed identity/activity admission or dispatched", err, effects, runner.killed, runner.switched)
				}
				if outcome == "cancel-before" && !errors.Is(err, context.Canceled) {
					t.Fatal("check lost cancellation", err)
				}
				if unblock != nil {
					unblock()
				}
				for _, name := range []string{"workspace-alpha", "workspace-beta"} {
					probe := sessionlease.New(true)
					_, err := probe.Admit(context.Background(), runner, sessionlease.Request{Selector: runner.sessions[name].ID, Name: name})
					probe.Close()
					if err != nil {
						t.Fatal("native check leaked name/incarnation admission", err)
					}
				}
			})
		}
	}
}
