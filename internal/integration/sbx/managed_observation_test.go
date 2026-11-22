package sbx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestLegacyManagedSandboxRefreshesAfterEveryObservation(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			for _, phase := range []string{"initial", "before", "after", "initial-absent"} {
				for _, change := range []string{"owner", "agent", "anchor-replaced", "configured-root", "cancelled", "unrelated"} {
					t.Run(fmt.Sprintf("service=%t/mode=%d/%s/%s", service, mode, phase, change), func(t *testing.T) {
						root, owner, other, target := runtimeAdmissionFixture(t)
						target.WorkspaceID, target.Path = "", ""
						t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						row := fmt.Sprintf(`[{"name":%q,"id":"managed-lifetime"}]`, target.ResourceID)
						runner := &standaloneOutcomeRunner{rows: []string{row, row, "[]"}}
						at := map[string]int{"initial": 1, "before": 2, "after": 3, "initial-absent": 1}[phase]
						if phase == "initial-absent" {
							runner.rows[0] = "[]"
						}
						changed := false
						runner.hook = func(command string, reads int) error {
							assertManagedOutcomeNoteAvailable(t, root)
							if command != "ls" || reads != at || changed {
								return nil
							}
							changed = true
							switch change {
							case "cancelled":
								cancel()
							case "agent":
								registerManagedSandboxAgent(t, owner)
							case "anchor-replaced":
								if err := os.Rename(owner.Path, owner.Path+".old"); err != nil {
									t.Fatal(err)
								}
								if err := os.Mkdir(owner.Path, 0755); err != nil {
									t.Fatal(err)
								}
							case "configured-root":
								if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml"), fmt.Appendf(nil, "workspace: {root_dir: %q}\n", filepath.Join(root, "changed")), 0600); err != nil {
									t.Fatal(err)
								}
							default:
								if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
									for i := range registry.Workspaces {
										g := &registry.Workspaces[i]
										if change == "owner" && g.ID == owner.ID || change == "unrelated" && g.ID == other.ID {
											g.Model = "changed"
										}
									}
									return nil
								}); err != nil {
									t.Fatal(err)
								}
							}
							return nil
						}
						var result protocol.CleanupResult
						var err error
						if service {
							result, err = cleanup.New([]integration.CleanupProvider{admittedFixtureSource{runner: runner}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
						} else {
							var cleaned protocol.CleanupTarget
							cleaned, err = cleanupWithRunner(ctx, runner, integration.CleanupRequest{Target: target, Mode: mode}, false)
							if err == nil {
								result.Targets = []protocol.CleanupTarget{cleaned}
							}
						}
						success := change == "unrelated"
						if !changed || (err == nil) != success || (len(result.Targets) == 1) != success {
							t.Fatal("observation did not refresh actual owner", changed, result, err, runner.calls)
						}
						if change == "cancelled" && !errors.Is(err, context.Canceled) {
							t.Fatal("cancellation lost", err)
						}
						want := []string{"sbx ls --json", "sbx ls --json", "sbx rm --force managed-lifetime", "sbx ls --json"}
						if phase == "initial-absent" || !success && phase == "initial" {
							want = want[:1]
						} else if !success && phase == "before" {
							want = want[:2]
						}
						if !reflect.DeepEqual(runner.calls, want) {
							t.Fatal("continued after stale ownership or replayed removal", runner.calls, want)
						}
						if data, err := os.ReadFile(owner.NotePath); err != nil || string(data) != "canonical retained\n" {
							t.Fatal("canonical note changed", err)
						}
						registry, err := workspacegroup.Load(root)
						if err != nil {
							t.Fatal(err)
						}
						if _, found := workspacegroup.FindByID(registry, owner.ID); !found {
							t.Fatal("owner registration removed")
						}
						actual, found := workspacegroup.FindByID(registry, other.ID)
						if !found || change == "unrelated" && actual.Model != "changed" {
							t.Fatal("unrelated registration lost")
						}
						assertRuntimeAdmissionReleased(t, owner)
					})
				}
			}
		}
	}
}
