package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
)

// Exercise the real confirmed-recreation path, not just the removal helper.
// External actors do not share Radar's workspace admission.
func TestRecreationConfirmationLifetimeSurvivesReconciliationHandoff(t *testing.T) {
	for _, route := range []string{"direct", "batch"} {
		for _, stage := range []int{1, 2, 3} {
			for _, scenario := range []string{"replacement", "absent", "renamed", "renamed-with-replacement", "duplicate-identity", "cancelled"} {
				t.Run(fmt.Sprintf("%s/stage-%d/%s", route, stage, scenario), func(t *testing.T) {
					req, groups, fixture := recreationOutcomeFixture(t, "running", 1)
					r := fixture.base
					plan, err := PreviewRecreateSandboxes(context.Background(), r, req)
					if err != nil {
						t.Fatal(err)
					}
					original := r.live["sandbox-0"]
					marker := filepath.Join(groups[0].Path, "retained")
					if err := os.WriteFile(marker, []byte("retained\n"), 0600); err != nil {
						t.Fatal(err)
					}
					registryBefore, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					validations, reads := 0, 0
					neededValidations := 1
					if route == "batch" {
						neededValidations = 2
					}
					fired := false
					var expected map[string]listedSandbox
					r.beforeRun = func(args []string) {
						if args[0] == "policy" {
							validations++
						}
						if args[0] != "ls" || validations < neededValidations {
							return
						}
						reads++
						if reads != stage {
							return
						}
						fired = true
						switch scenario {
						case "replacement":
							replacement := original
							replacement.ID = "external-replacement"
							r.live[original.Name] = replacement
						case "absent":
							delete(r.live, original.Name)
						case "renamed", "renamed-with-replacement":
							delete(r.live, original.Name)
							renamed := original
							renamed.Name = "renamed-original"
							r.live[renamed.Name] = renamed
							if scenario == "renamed-with-replacement" {
								replacement := original
								replacement.ID = "external-replacement"
								r.live[replacement.Name] = replacement
							}
						case "duplicate-identity":
							duplicate := original
							duplicate.Name = "unrelated"
							r.live[duplicate.Name] = duplicate
						case "cancelled":
							cancel()
						}
						expected = map[string]listedSandbox{}
						for name, runtime := range r.live {
							expected[name] = runtime
						}
					}
					r.calls = nil
					var result integration.SandboxRecreateResult
					if route == "direct" {
						err = recreateOneSandbox(ctx, r, nil, req.WorkspaceRoot, groups[0], plan.Targets[0], sandboxReconcilePolicy{forceRecreate: true})
						if err == nil {
							t.Error("confirmed lifetime drift was accepted")
						}
						if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
							t.Errorf("cancellation lost: %v", err)
						}
					} else {
						result, err = recreateSandboxes(ctx, r, nil, req, sandboxReconcilePolicy{})
						if err != nil || result.OK || result.Recreated != 0 || result.Failed != 1 || len(result.Targets) != 1 || result.Targets[0].Status != "failed" {
							t.Errorf("changed confirmed lifetime was not a failed target: result=%+v err=%v", result, err)
						}
					}
					if !fired {
						t.Fatalf("fixture did not reach handoff stage %d: %v", stage, r.calls)
					}
					assertNoRecreationMutation(t, r)
					if !reflect.DeepEqual(r.live, expected) {
						t.Errorf("unconfirmed lifetime mutated: got=%+v want=%+v", r.live, expected)
					}
					after, readErr := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if readErr != nil || !reflect.DeepEqual(registryBefore, after) {
						t.Fatal("registration changed", readErr)
					}
					data, readErr := os.ReadFile(marker)
					if readErr != nil || string(data) != "retained\n" {
						t.Fatal("workspace contents changed", readErr)
					}
					release, leaseErr := acquireWorkspaceLeases(context.Background(), groups[0])
					if leaseErr != nil {
						t.Fatal("admission not released", leaseErr)
					}
					release()
				})
			}
		}
	}
}

type confirmationCreateRunner struct {
	base    *recreationRunner
	creates int
	failure string
}

func (r *confirmationCreateRunner) LookPath(name string) error { return r.base.LookPath(name) }
func (r *confirmationCreateRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	output, err := r.base.Run(ctx, cwd, name, args...)
	if err == nil && args[0] == "create" {
		r.creates++
		if r.creates == 1 && r.failure != "" {
			runtime := r.base.live[args[2]]
			runtime.ID = "failed-create-lifetime"
			r.base.live[runtime.Name] = runtime
			return "", errors.New(r.failure)
		}
	}
	return output, err
}

func TestRecreationConfirmationDoesNotPinFailedCreateCleanupToOriginal(t *testing.T) {
	for _, route := range []string{"direct", "batch"} {
		for _, failure := range []string{"none", "transient", "permanent"} {
			t.Run(route+"/"+failure, func(t *testing.T) {
				req, groups, fixture := recreationOutcomeFixture(t, "running", 1)
				plan, err := PreviewRecreateSandboxes(context.Background(), fixture.base, req)
				if err != nil {
					t.Fatal(err)
				}
				r := &confirmationCreateRunner{base: fixture.base}
				switch failure {
				case "transient":
					r.failure = "[POST /sandboxes][500 Internal Server Error] failed to run sandbox container"
				case "permanent":
					r.failure = "invalid sandbox kit"
				}
				r.base.calls = nil
				policy := sandboxReconcilePolicy{forceRecreate: true, createAttempts: 2}
				var result integration.SandboxRecreateResult
				if route == "direct" {
					err = recreateOneSandbox(context.Background(), r, nil, req.WorkspaceRoot, groups[0], plan.Targets[0], policy)
					if (err == nil) != (failure != "permanent") {
						t.Fatalf("err=%v", err)
					}
				} else {
					result, err = recreateSandboxes(context.Background(), r, nil, req, policy)
					if err != nil || len(result.Targets) != 1 || result.OK != (failure != "permanent") || (result.Recreated == 1) != (failure != "permanent") {
						t.Fatalf("result=%+v err=%v", result, err)
					}
				}
				removals := []string{}
				for _, c := range r.base.calls {
					if c.args[0] == "rm" {
						removals = append(removals, c.args[2])
					}
				}
				want := []string{"original-sandbox-0"}
				if failure != "none" {
					want = append(want, "failed-create-lifetime")
				}
				if !reflect.DeepEqual(removals, want) {
					t.Fatalf("confirmed identity leaked to retries: got=%v want=%v", removals, want)
				}
				wantCreates := 1
				if failure == "transient" {
					wantCreates = 2
				}
				if r.creates != wantCreates {
					t.Fatalf("creates=%d want=%d", r.creates, wantCreates)
				}
				if failure == "permanent" {
					if len(r.base.live) != 0 {
						t.Fatal("failed runtime not removed", r.base.live)
					}
				} else if runtime := r.base.live["sandbox-0"]; runtime.ID != "new-sandbox-0" || runtime.Status != "running" {
					t.Fatal("valid recreated outcome lost", runtime)
				}
			})
		}
	}
}
