package sbx

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type standaloneOutcomeRunner struct {
	rows    []string
	calls   []string
	reads   int
	hook    func(string, int) error
	missing bool
}

func (r *standaloneOutcomeRunner) LookPath(string) error {
	if r.missing {
		return errors.New("fixture CLI unavailable")
	}
	return nil
}
func (r *standaloneOutcomeRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if args[0] == "ls" {
		r.reads++
	}
	if r.hook != nil {
		if err := r.hook(args[0], r.reads); err != nil {
			return "", err
		}
	}
	switch args[0] {
	case "ls":
		if r.reads > len(r.rows) {
			return "", errors.New("unexpected extra observation")
		}
		return r.rows[r.reads-1], nil
	case "rm":
		return "", nil
	default:
		return "", fmt.Errorf("unexpected fixture command %s", args[0])
	}
}

func TestStandaloneSandboxRequiresObservedLifetimeRemoval(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			for _, scenario := range []string{"removed", "unrelated", "initial-absent", "gone-before", "replacement-before", "renamed-before", "duplicate-name-before", "duplicate-id-before", "malformed-before", "unavailable-before", "cancelled-before", "malformed-initial", "unavailable-initial", "cancelled-initial", "duplicate-name-initial", "missing-cli", "remaining-after", "renamed-after", "replacement-after", "ambiguous-after", "malformed-after", "unavailable-after", "cancelled-after", "rm-error", "rm-not-found-error"} {
				t.Run(fmt.Sprintf("service=%t/mode=%d/%s", service, mode, scenario), func(t *testing.T) {
					root, owner, _, target := runtimeAdmissionFixture(t)
					target.ResourceID, target.WorkspaceID = "standalone-sandbox", ""
					target.Operation = map[string]string{"fixture": "retained"}
					original := maps.Clone(target.Operation)
					registry, err := workspacegroup.Load(root)
					if err != nil {
						t.Fatal(err)
					}
					row := func(name, id string) string { return fmt.Sprintf(`{"name":%q,"id":%q}`, name, id) }
					native := "[" + row(target.ResourceID, "standalone-lifetime") + "]"
					renamed := "[" + row("renamed-standalone", "standalone-lifetime") + "]"
					replacement := "[" + row(target.ResourceID, "replacement-lifetime") + "]"
					runner := &standaloneOutcomeRunner{rows: []string{native, native, "[]"}}
					success := scenario == "removed" || scenario == "unrelated" || scenario == "initial-absent" || scenario == "gone-before"
					switch scenario {
					case "unrelated":
						runner.rows[2] = "[" + row("unrelated", "unrelated-lifetime") + "]"
					case "initial-absent":
						runner.rows[0] = "[]"
					case "gone-before":
						runner.rows[1] = "[]"
					case "replacement-before":
						runner.rows[1] = replacement
					case "renamed-before":
						runner.rows[1] = renamed
					case "duplicate-name-before":
						runner.rows[1] = "[" + row(target.ResourceID, "standalone-lifetime") + "," + row(target.ResourceID, "another-lifetime") + "]"
					case "duplicate-id-before":
						runner.rows[1] = "[" + row(target.ResourceID, "standalone-lifetime") + "," + row("another-name", "standalone-lifetime") + "]"
					case "malformed-before":
						runner.rows[1] = "invalid"
					case "malformed-initial":
						runner.rows[0] = "invalid"
					case "duplicate-name-initial":
						runner.rows[0] = "[" + row(target.ResourceID, "standalone-lifetime") + "," + row(target.ResourceID, "another-lifetime") + "]"
					case "missing-cli":
						runner.missing = true
					case "remaining-after":
						runner.rows[2] = native
					case "renamed-after":
						runner.rows[2] = renamed
					case "replacement-after":
						runner.rows[2] = replacement
					case "ambiguous-after":
						runner.rows[2] = "[" + row("renamed", "standalone-lifetime") + "," + row("another", "standalone-lifetime") + "]"
					case "malformed-after":
						runner.rows[2] = "invalid"
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					failure := errors.New("fixture backend unavailable")
					admissionChecks := 0
					runner.hook = func(command string, reads int) error {
						admissionChecks++
						release, err := workspacelease.AcquireSandbox(target.ResourceID, false)
						if err == nil {
							release()
							t.Error("native command escaped retained name admission")
						}
						if service {
							release, err := workspacelease.Acquire(target.Path, false)
							if err == nil {
								release()
								t.Error("native command escaped Service path admission")
							}
						}
						phase := "initial"
						if reads == 2 {
							phase = "before"
						} else if reads == 3 {
							phase = "after"
						}
						if command == "ls" && scenario == "unavailable-"+phase {
							return failure
						}
						if command == "ls" && scenario == "cancelled-"+phase {
							cancel()
						}
						if command == "rm" {
							if scenario == "rm-error" {
								return failure
							}
							if scenario == "rm-not-found-error" {
								return errors.New("fixture backend endpoint not found")
							}
						}
						return nil
					}
					var result protocol.CleanupResult
					if service {
						result, err = cleanup.New([]integration.CleanupProvider{admittedFixtureSource{runner: runner}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
					} else {
						var cleaned protocol.CleanupTarget
						cleaned, err = cleanupWithRunner(ctx, runner, integration.CleanupRequest{Target: target, Mode: mode}, false)
						if err == nil {
							result.Targets = []protocol.CleanupTarget{cleaned}
						}
					}
					if (err == nil) != success {
						t.Errorf("outcome err=%v want success=%t calls=%v", err, success, runner.calls)
					}
					if strings.HasPrefix(scenario, "unavailable-") || scenario == "rm-error" {
						if !errors.Is(err, failure) {
							t.Error("backend error identity lost", err)
						}
					}
					if strings.HasPrefix(scenario, "cancelled-") && !errors.Is(err, context.Canceled) {
						t.Error("cancellation lost", err)
					}
					if (len(result.Targets) == 1) != success {
						t.Error("incorrect returned-target outcome", result)
					}
					want := []string{"sbx ls --json", "sbx ls --json", "sbx rm --force standalone-lifetime", "sbx ls --json"}
					switch {
					case scenario == "missing-cli":
						want = nil
					case strings.HasSuffix(scenario, "-initial") || scenario == "initial-absent":
						want = want[:1]
					case strings.HasSuffix(scenario, "-before"):
						want = want[:2]
					case strings.HasPrefix(scenario, "rm-"):
						want = want[:3]
					}
					if !reflect.DeepEqual(runner.calls, want) || admissionChecks != len(want) {
						t.Error("native commands replayed, name-addressed or missing observation", runner.calls, want, admissionChecks)
					}
					if !maps.Equal(original, target.Operation) {
						t.Error("caller target changed")
					}
					after, loadErr := workspacegroup.Load(root)
					if loadErr != nil || !reflect.DeepEqual(registry, after) {
						t.Error("unrelated registration changed", loadErr)
					}
					data, readErr := os.ReadFile(owner.NotePath)
					if readErr != nil || string(data) != "canonical retained\n" {
						t.Error("canonical note changed", readErr)
					}
					release, lockErr := workspacelease.AcquireSandbox(target.ResourceID, true)
					if lockErr != nil {
						t.Fatal("name admission not released", lockErr)
					}
					release()
					assertRuntimeAdmissionReleased(t, owner)
				})
			}
		}
	}
}

func TestStandaloneSandboxUnverifiedOutcomeStopsLaterTargets(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			_, owner, _, target := runtimeAdmissionFixture(t)
			target.ResourceID, target.WorkspaceID = "standalone-sandbox", ""
			row := `[{"name":"standalone-sandbox","id":"standalone-lifetime"}]`
			runner := &standaloneOutcomeRunner{rows: []string{row, row, row}}
			prefix, later := filepath.Join(t.TempDir(), "prefix"), filepath.Join(t.TempDir(), "later")
			for _, path := range []string{prefix, later} {
				if err := os.WriteFile(path, []byte("retained\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			probe := managedSandboxProbe{run: func() error {
				calls++
				if calls == 1 {
					return os.Remove(prefix)
				}
				return os.Remove(later)
			}}
			first, last := protocol.CleanupTarget{Source: "sbx-probe", ResourceID: "prefix"}, protocol.CleanupTarget{Source: "sbx-probe", ResourceID: "later"}
			result, err := cleanup.New([]integration.CleanupProvider{probe, admittedFixtureSource{runner: runner}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{first, target, last}}, cleanup.ExecuteOptions{Mode: mode})
			if err == nil || calls != 1 || !reflect.DeepEqual(result.Targets, []protocol.CleanupTarget{first}) {
				t.Fatal("unverified removal crossed ordered dispatch boundary", result, err, calls)
			}
			if result.ExecutionObservation == nil || result.ExecutionObservation.Stage != "provider_call" || result.ExecutionObservation.AttemptedTargets != 2 {
				t.Fatal("incorrect partial execution observation", result)
			}
			if _, err := os.Stat(prefix); !os.IsNotExist(err) {
				t.Fatal("successful prefix replayed or rolled back", err)
			}
			if data, err := os.ReadFile(later); err != nil || string(data) != "retained\n" {
				t.Fatal("later resource changed", err)
			}
			if !reflect.DeepEqual(runner.calls, []string{"sbx ls --json", "sbx ls --json", "sbx rm --force standalone-lifetime", "sbx ls --json"}) {
				t.Fatal("unexpected retry or missing outcome read", runner.calls)
			}
			assertRuntimeAdmissionReleased(t, owner)
		})
	}
}
