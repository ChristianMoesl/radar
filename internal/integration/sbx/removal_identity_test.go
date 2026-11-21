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

// No real SBX backend: a successful rm exit is separate from the fresh listing.
type removalIdentityRunner struct {
	before, after string
	commands      []string
	removed       bool
	afterRead     func() error
}

func (*removalIdentityRunner) LookPath(string) error { return nil }
func (r *removalIdentityRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	r.commands = append(r.commands, name+" "+strings.Join(args, " "))
	switch args[0] {
	case "ls":
		if !r.removed {
			return r.before, nil
		}
		if r.afterRead != nil {
			if err := r.afterRead(); err != nil {
				return "", err
			}
		}
		return r.after, nil
	case "rm":
		r.removed = true
		return "", nil
	default:
		return "", fmt.Errorf("unexpected fixture command %q", args[0])
	}
}

func TestRegisteredSandboxRemovalIdentityFailurePreservesSuccessfulPrefix(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			root, owner, _, target := runtimeAdmissionFixture(t)
			before, err := workspacegroup.Load(root)
			if err != nil {
				t.Fatal(err)
			}
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
			runner := &removalIdentityRunner{
				before: fmt.Sprintf(`[{"id":"lifetime-1","name":%q,"status":"running","workspaces":[%q]}]`, owner.Sandbox.Name, owner.Path),
				after:  fmt.Sprintf(`[{"id":"lifetime-1","name":"renamed-sandbox","status":"running","workspaces":[%q]}]`, owner.Path),
			}
			first := protocol.CleanupTarget{Source: "sbx-probe", ResourceID: "prefix"}
			last := protocol.CleanupTarget{Source: "sbx-probe", ResourceID: "later"}
			result, err := cleanup.New([]integration.CleanupProvider{probe, admittedFixtureSource{runner: runner}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{first, target, last}}, cleanup.ExecuteOptions{Mode: mode})
			if err == nil || calls != 1 || !reflect.DeepEqual(result.Targets, []protocol.CleanupTarget{first}) {
				t.Fatal("unverified absence crossed ordered failure boundary", result, err, calls)
			}
			if result.ExecutionObservation == nil || result.ExecutionObservation.Stage != "provider_call" || result.ExecutionObservation.AttemptedTargets != 2 {
				t.Fatal("incorrect partial dispatch evidence", result)
			}
			if _, err := os.Stat(prefix); !os.IsNotExist(err) {
				t.Fatal("successful prefix was replayed or rolled back", err)
			}
			for _, path := range []string{later, owner.Path, owner.NotePath} {
				if _, err := os.Stat(path); err != nil {
					t.Fatal("remaining resource lost", path, err)
				}
			}
			after, err := workspacegroup.Load(root)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("registration changed", err)
			}
			if !reflect.DeepEqual(runner.commands, []string{"sbx ls --json", "sbx rm --force lifetime-1", "sbx ls --json"}) {
				t.Fatal("removal replayed or broadened", runner.commands)
			}
			assertRuntimeAdmissionReleased(t, owner)
		})
	}
}

func TestRegisteredSandboxRemovalRequiresOriginalLifetimeAbsence(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			for _, scenario := range []string{"removed", "unrelated", "initial-missing", "renamed-before", "duplicate-id-before", "renamed-after", "duplicate-id-after", "replacement-after", "remaining-after", "malformed-after", "read-error-after", "cancelled-after", "owner-drift-after", "unrelated-update-after"} {
				t.Run(fmt.Sprintf("service=%t/%v/%s", service, mode, scenario), func(t *testing.T) {
					root, owner, other, target := runtimeAdmissionFixture(t)
					original := maps.Clone(target.Operation)
					before, err := workspacegroup.Load(root)
					if err != nil {
						t.Fatal(err)
					}
					row := func(name, id string) string {
						return fmt.Sprintf(`{"id":%q,"name":%q,"status":"running","workspaces":[%q]}`, id, name, owner.Path)
					}
					native := row(owner.Sandbox.Name, "lifetime-1")
					renamed := row("renamed-sandbox", "lifetime-1")
					runner := &removalIdentityRunner{before: "[" + native + "]", after: "[]"}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					failure := errors.New("fixture native read failure")
					checked := false
					runner.afterRead = func() error {
						checked = true
						for _, acquire := range []func() (func(), error){func() (func(), error) { return workspacelease.Acquire(owner.Path, false) }, func() (func(), error) { return workspacelease.AcquireSandbox(owner.Sandbox.Name, false) }} {
							release, err := acquire()
							if err == nil {
								release()
								t.Error("absence observation escaped retained admission")
							}
						}
						switch scenario {
						case "read-error-after":
							return failure
						case "cancelled-after":
							cancel()
						case "owner-drift-after", "unrelated-update-after":
							return workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
								for i := range registry.Workspaces {
									g := &registry.Workspaces[i]
									if scenario == "owner-drift-after" && g.ID == owner.ID || scenario == "unrelated-update-after" && g.ID == other.ID {
										g.Model = "changed"
									}
								}
								return nil
							})
						}
						return nil
					}
					switch scenario {
					case "unrelated":
						runner.after = "[" + row("other-sandbox", "lifetime-2") + "]"
					case "initial-missing":
						runner.before = "[]"
					case "renamed-before":
						runner.before = "[" + renamed + "]"
					case "duplicate-id-before":
						runner.before = "[" + native + "," + renamed + "]"
					case "renamed-after":
						runner.after = "[" + renamed + "]"
					case "duplicate-id-after":
						runner.after = "[" + renamed + "," + row("another-sandbox", "lifetime-1") + "]"
					case "replacement-after":
						runner.after = "[" + row(owner.Sandbox.Name, "lifetime-2") + "]"
					case "remaining-after":
						runner.after = runner.before
					case "malformed-after":
						runner.after = "invalid"
					}
					var result protocol.CleanupResult
					if service {
						result, err = cleanup.New([]integration.CleanupProvider{admittedFixtureSource{runner: runner}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
					} else {
						_, err = cleanupWithRunner(ctx, runner, integration.CleanupRequest{Target: target, Mode: mode}, false)
					}
					success := scenario == "removed" || scenario == "unrelated" || scenario == "initial-missing" || scenario == "unrelated-update-after"
					if (err == nil) != success {
						t.Errorf("outcome err=%v wantSuccess=%t commands=%v", err, success, runner.commands)
					}
					if scenario == "read-error-after" && !errors.Is(err, failure) {
						t.Error("native error identity lost", err)
					}
					if scenario == "cancelled-after" && !errors.Is(err, context.Canceled) {
						t.Error("cancellation lost", err)
					}
					initialOnly := scenario == "initial-missing" || strings.HasSuffix(scenario, "-before")
					expected := []string{"sbx ls --json"}
					if !initialOnly {
						expected = append(expected, "sbx rm --force lifetime-1", "sbx ls --json")
					}
					if !reflect.DeepEqual(runner.commands, expected) || checked == initialOnly {
						t.Error("wrong native effects or missing observation", runner.commands, expected, checked)
					}
					if service && ((len(result.Targets) == 1) != success) {
						t.Error("unverified runtime reported successful", result)
					}
					if !maps.Equal(original, target.Operation) {
						t.Error("caller preview changed")
					}
					after, loadErr := workspacegroup.Load(root)
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					if !strings.Contains(scenario, "drift") && !strings.HasPrefix(scenario, "unrelated-update") && !reflect.DeepEqual(before, after) {
						t.Error("registry changed")
					}
					for _, path := range []string{owner.Path, other.Path, owner.NotePath} {
						if _, err := os.Stat(path); err != nil {
							t.Error("retained resource lost", path, err)
						}
					}
					note, readErr := os.ReadFile(owner.NotePath)
					if readErr != nil || string(note) != "canonical retained\n" {
						t.Error("canonical note changed", readErr)
					}
					assertRuntimeAdmissionReleased(t, owner)
				})
			}
		}
	}
}
