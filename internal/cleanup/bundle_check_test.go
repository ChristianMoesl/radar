package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/phasetiming"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type bundleCheckProvider struct {
	fakeProvider
	admit     func(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error)
	preflight func(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error
	cleanup   func(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error)
}

func (p *bundleCheckProvider) AdmitCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	return p.admit(ctx, targets, mode)
}
func (p *bundleCheckProvider) PreflightCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) error {
	return p.preflight(ctx, targets, mode)
}
func (p *bundleCheckProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return p.cleanup(ctx, req)
}
func (*bundleCheckProvider) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return nil, errors.New("bundle check must not rebuild its caller's preview")
}

func TestCheckBundleRetainsWholeAdmissionWithoutDispatch(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		for _, outcome := range []string{"success", "cancel-before", "admit-git", "admit-workspace", "cancel-admit", "preflight-git", "preflight-workspace", "cancel-final-preflight"} {
			t.Run(fmt.Sprintf("mode=%d/%s", mode, outcome), func(t *testing.T) {
				root, preview := capabilityAdmissionFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				extra := filepath.Join(root, "discovered-owner")
				if err := os.Mkdir(extra, 0700); err != nil {
					t.Fatal(err)
				}
				failure := errors.New("fixture native guard rejected changed state")
				var events []string
				var retained context.Context
				assertHeld := func() {
					t.Helper()
					for _, target := range preview.Targets {
						assertPathHeld(t, target.Path)
					}
				}
				var providers []integration.CleanupProvider
				for _, name := range []string{"git", "workspace"} {
					p := &bundleCheckProvider{fakeProvider: fakeProvider{name: name}}
					p.admit = func(ctx context.Context, targets []protocol.CleanupTarget, gotMode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
						events = append(events, "admit:"+name)
						assertHeld()
						var want []protocol.CleanupTarget
						for _, target := range preview.Targets {
							if target.Source == name {
								want = append(want, target)
							}
						}
						if gotMode != mode || !reflect.DeepEqual(targets, want) {
							t.Fatal("admission changed exact mode, target set or order", gotMode, targets)
						}
						if outcome == "admit-"+name {
							return nil, nil, failure
						}
						if name == "git" {
							retained = context.WithoutCancel(ctx)
							release, err := AcquirePaths(ctx, extra, preview.Targets[0].Path)
							if err != nil {
								t.Fatal(err)
							}
							release() // Borrowed/discovered admission belongs to the whole check.
							assertPathHeld(t, extra)
							assertHeld()
							if outcome == "cancel-admit" {
								cancel()
							}
						}
						return p, func() { events = append(events, "release:"+name) }, nil
					}
					p.preflight = func(ctx context.Context, targets []protocol.CleanupTarget, gotMode integration.CleanupMode) error {
						events = append(events, "preflight:"+name)
						assertHeld()
						assertPathHeld(t, extra)
						if gotMode != mode || !reflect.DeepEqual(targets, preview.Targets) {
							t.Fatal("preflight changed full exact bundle", gotMode, targets)
						}
						if outcome == "preflight-"+name {
							return failure
						}
						if outcome == "cancel-final-preflight" && name == "workspace" {
							cancel() // Deliberately return nil after cancelling.
						}
						return nil
					}
					p.cleanup = func(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
						t.Fatal("bundle check entered destructive provider dispatch")
						return protocol.CleanupTarget{}, nil
					}
					providers = append(providers, p)
				}
				if outcome == "cancel-before" {
					cancel()
				}
				err := New(providers).CheckBundle(ctx, preview, ExecuteOptions{Mode: mode})
				if (err == nil) != (outcome == "success") {
					t.Fatal("unexpected check outcome", err)
				}
				if strings.HasPrefix(outcome, "admit-") || strings.HasPrefix(outcome, "preflight-") {
					if !errors.Is(err, failure) || !strings.Contains(err.Error(), "stopped after 0 of 3") {
						t.Fatal("check lost native rejection/recovery evidence", err)
					}
				}
				if strings.HasPrefix(outcome, "cancel-") && !errors.Is(err, context.Canceled) {
					t.Fatal("check lost cancellation", err)
				}
				want := []string{"admit:git", "admit:workspace", "preflight:git", "preflight:workspace", "release:git", "release:workspace"}
				switch outcome {
				case "cancel-before":
					want = nil
				case "admit-git":
					want = []string{"admit:git"}
				case "admit-workspace":
					want = []string{"admit:git", "admit:workspace", "release:git"}
				case "cancel-admit":
					want = []string{"admit:git", "admit:workspace", "release:git", "release:workspace"}
				case "preflight-git":
					want = []string{"admit:git", "admit:workspace", "preflight:git", "release:git", "release:workspace"}
				}
				if !reflect.DeepEqual(events, want) {
					t.Fatal("check changed native guard/release ordering", events, want)
				}
				for _, target := range preview.Targets {
					if data, err := os.ReadFile(target.Path); err != nil || string(data) != "preserve until the whole bundle is admitted" {
						t.Fatal("check changed a resource", target.ResourceID, string(data), err)
					}
					assertPathReleased(t, target.Path)
				}
				assertPathReleased(t, extra)
				if retained != nil {
					release, err := AcquirePaths(retained, extra)
					if err != nil {
						t.Fatal("expired check context could not acquire independent admission", err)
					}
					assertPathHeld(t, extra)
					release()
					assertPathReleased(t, extra)
				}
			})
		}
	}
}

func TestCheckBundlePreservesNativeAdmissionRejections(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		for _, problem := range []string{"unsupported-first", "unsupported-later", "empty-source", "whitespace-source", "ambiguous-source", "invalid-mode", "no-targets", "automatic-safety", "busy-path", "invalid-path", "owner", "unknown-owner", "busy-sandbox", "invalid-sandbox"} {
			t.Run(fmt.Sprintf("mode=%d/%s", mode, problem), func(t *testing.T) {
				root, preview := capabilityAdmissionFixture(t)
				var events []string
				providers := []integration.CleanupProvider{
					capabilityAdmissionProvider{fakeProvider: fakeProvider{name: "git"}, events: &events},
					capabilityAdmissionProvider{fakeProvider: fakeProvider{name: "workspace"}, events: &events},
					capabilityAdmissionProvider{fakeProvider: fakeProvider{name: "sbx"}, events: &events},
				}
				options := ExecuteOptions{Mode: mode}
				var unblock func()
				var err error
				switch problem {
				case "unsupported-first":
					preview.Targets[0].Source = "missing"
				case "unsupported-later":
					preview.Targets[2].Source = "missing"
				case "empty-source":
					preview.Targets[2].Source = ""
				case "whitespace-source":
					preview.Targets[2].Source = "git "
				case "ambiguous-source":
					providers = append(providers, capabilityAdmissionProvider{fakeProvider: fakeProvider{name: "git"}, events: &events})
				case "invalid-mode":
					options.Mode = integration.CleanupMode(255)
				case "no-targets":
					preview.Targets = nil
				case "automatic-safety":
					preview.Targets[2].Safety = []protocol.CleanupSafety{{Kind: "ownership_conflict", BlocksAutomatic: true, Message: "fixture safety"}}
				case "busy-path":
					unblock, err = workspacelease.Acquire(preview.Targets[2].Path, false)
				case "invalid-path":
					preview.Targets[2].Path = filepath.Join(root, "dangling")
					err = os.Symlink(filepath.Join(root, "missing"), preview.Targets[2].Path)
				case "owner":
					owner := filepath.Join(root, "agents", "fixture-owner")
					if err = os.MkdirAll(owner, 0700); err == nil {
						data, marshalErr := json.Marshal(agentservice.Registration{Version: 1, ID: "fixture-owner", Workspace: protocol.AgentWorkspaceBinding{ID: "fixture", Path: preview.Targets[2].Path, Mode: "local"}})
						if marshalErr != nil {
							t.Fatal(marshalErr)
						}
						err = os.WriteFile(filepath.Join(owner, "registration.json"), data, 0600)
					}
				case "unknown-owner":
					err = os.WriteFile(filepath.Join(root, "agents"), []byte("unreadable registry"), 0600)
				case "busy-sandbox", "invalid-sandbox":
					preview.Targets[2].Source, preview.Targets[2].Kind = "sbx", "sandbox"
					if problem == "busy-sandbox" {
						preview.Targets[2].ResourceID = "shared-runtime"
						unblock, err = workspacelease.AcquireSandbox("shared-runtime", false)
					} else {
						preview.Targets[2].ResourceID = "invalid\nname"
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if unblock != nil {
					defer unblock()
				}
				service := New(providers)
				err = service.CheckBundle(context.Background(), preview, options)
				if problem == "automatic-safety" && mode == integration.CleanupConfirmed {
					if err != nil {
						t.Fatal("check changed explicitly confirmed safety policy", err)
					}
					for _, event := range events {
						if strings.HasPrefix(event, "cleanup:") {
							t.Fatal("check dispatched explicitly confirmed target", events)
						}
					}
					return
				}
				if err == nil || len(events) != 0 {
					t.Fatal("invalid check reached provider callbacks", err, events)
				}
				result, executeErr := service.Execute(context.Background(), preview, options)
				if executeErr == nil || executeErr.Error() != err.Error() || len(events) != 0 || len(result.Targets) != 0 || result.ExecutionObservation.AttemptedTargets != 0 {
					t.Fatal("check and execution admission diverged", err, executeErr, events, result)
				}
				for _, name := range []string{"one", "two", "three"} {
					if data, readErr := os.ReadFile(filepath.Join(root, name)); readErr != nil || string(data) != "preserve until the whole bundle is admitted" {
						t.Fatal("rejected check changed resource", name, readErr)
					}
				}
				assertPathReleased(t, filepath.Join(root, "one"))
				assertPathReleased(t, filepath.Join(root, "two"))
			})
		}
	}
}

func TestCheckBundleDeduplicatesOnlyLeasesNotExactTargets(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			physical, alias, extra := pathAdmissionFixture(t)
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{
				sandboxLeaseTarget("shared-runtime", physical), sandboxLeaseTarget("shared-runtime", alias), sandboxLeaseTarget("shared-runtime", extra),
			}}
			p := &bundleCheckProvider{fakeProvider: fakeProvider{name: "sbx"}}
			var events []string
			p.admit = func(ctx context.Context, targets []protocol.CleanupTarget, gotMode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
				events = append(events, "admit")
				if gotMode != mode || !reflect.DeepEqual(targets, preview.Targets) {
					t.Fatal("check deduplicated or reordered distinct targets", targets)
				}
				for _, path := range []string{physical, alias, extra} {
					assertPathHeld(t, path)
				}
				if release, err := workspacelease.AcquireSandbox("shared-runtime", false); err == nil {
					release()
					t.Fatal("check did not retain shared sandbox name")
				}
				return p, func() { events = append(events, "release") }, nil
			}
			p.preflight = func(_ context.Context, targets []protocol.CleanupTarget, gotMode integration.CleanupMode) error {
				events = append(events, "preflight")
				if gotMode != mode || !reflect.DeepEqual(targets, preview.Targets) {
					t.Fatal("preflight lost full exact target set", targets)
				}
				return nil
			}
			p.cleanup = func(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
				t.Fatal("check entered bound provider Cleanup")
				return protocol.CleanupTarget{}, nil
			}
			if err := New([]integration.CleanupProvider{p}).CheckBundle(context.Background(), preview, ExecuteOptions{Mode: mode}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(events, []string{"admit", "preflight", "release"}) {
				t.Fatal(events)
			}
			for _, path := range []string{physical, alias, extra} {
				assertPathReleased(t, path)
			}
			assertSandboxNameReleased(t, "shared-runtime")
		})
	}
}

func TestCheckBundleDoesNotAuthorizeLaterExecution(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		for _, change := range []string{"lease", "admission", "preflight", "last-moment", "partial", "none"} {
			t.Run(fmt.Sprintf("mode=%d/%s", mode, change), func(t *testing.T) {
				_, preview := capabilityAdmissionFixture(t)
				failure := errors.New("fresh execution rejected changed state")
				changed := false
				var events []string
				p := &bundleCheckProvider{fakeProvider: fakeProvider{name: "git"}}
				for i := range preview.Targets {
					preview.Targets[i].Source = "git"
				}
				p.admit = func(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
					events = append(events, "admit")
					if changed && change == "admission" {
						return nil, nil, failure
					}
					// Distinct bound provider proves execution uses fresh admission.
					bound := &bundleCheckProvider{fakeProvider: p.fakeProvider}
					bound.cleanup = func(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
						events = append(events, "cleanup:"+req.Target.ResourceID)
						if !changed {
							t.Fatal("check dispatched a target")
						}
						if change == "last-moment" || change == "partial" && req.Target.ResourceID == "two" {
							return protocol.CleanupTarget{}, failure
						}
						return req.Target, os.Remove(req.Target.Path)
					}
					return bound, func() { events = append(events, "release") }, nil
				}
				p.preflight = func(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
					events = append(events, "preflight")
					if changed && change == "preflight" {
						return failure
					}
					return nil
				}
				p.cleanup = func(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
					t.Fatal("execution bypassed freshly bound native provider")
					return protocol.CleanupTarget{}, nil
				}
				service := New([]integration.CleanupProvider{p})
				options := ExecuteOptions{Mode: mode}
				if err := service.CheckBundle(context.Background(), preview, options); err != nil || !reflect.DeepEqual(events, []string{"admit", "preflight", "release"}) {
					t.Fatal("initial check failed or dispatched", err, events)
				}
				changed, events = true, nil
				var unblock func()
				if change == "lease" {
					var err error
					unblock, err = workspacelease.Acquire(preview.Targets[2].Path, false)
					if err != nil {
						t.Fatal("check retained a lease/reservation", err)
					}
					defer unblock()
				}
				result, err := service.Execute(context.Background(), preview, options)
				wantEvents, stage, attempted, completed := []string{"admit", "preflight", "cleanup:one", "cleanup:two", "cleanup:three", "release"}, "completed", 3, 3
				switch change {
				case "lease":
					wantEvents, stage, attempted, completed = nil, "ownership_and_leases", 0, 0
				case "admission":
					wantEvents, stage, attempted, completed = []string{"admit"}, "ownership_and_leases", 0, 0
				case "preflight":
					wantEvents, stage, attempted, completed = []string{"admit", "preflight", "release"}, "bundle_preflight", 0, 0
				case "last-moment":
					wantEvents, stage, attempted, completed = []string{"admit", "preflight", "cleanup:one", "release"}, "provider_call", 1, 0
				case "partial":
					wantEvents, stage, attempted, completed = []string{"admit", "preflight", "cleanup:one", "cleanup:two", "release"}, "provider_call", 2, 1
				}
				if (err == nil) != (change == "none") || !reflect.DeepEqual(events, wantEvents) || len(result.Targets) != completed || result.ExecutionObservation.Stage != stage || result.ExecutionObservation.AttemptedTargets != attempted {
					t.Fatal("check changed fresh execution or partial semantics", err, events, result, result.ExecutionObservation)
				}
				if change != "none" && change != "lease" && !errors.Is(err, failure) {
					t.Fatal("execution lost changed-state failure", err)
				}
				for i, target := range preview.Targets {
					_, statErr := os.Stat(target.Path)
					if (i < completed && !os.IsNotExist(statErr)) || (i >= completed && statErr != nil) {
						t.Fatal("actual resource effects differ from successful prefix", i, statErr, result)
					}
					if change != "lease" || i != 2 {
						assertPathReleased(t, target.Path)
					}
				}
			})
		}
	}
}

func TestCheckBundleWithoutOptionalProviderGuardsDoesNotReportExecution(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			var phases, calls []string
			ctx := phasetiming.WithObserver(context.Background(), func(phase string, _ time.Duration) { phases = append(phases, phase) })
			service := New([]integration.CleanupProvider{fakeProvider{name: "fixture", calls: &calls, failPath: "/never"}})
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", ResourceID: "one"}}}
			options := ExecuteOptions{Mode: mode}
			if err := service.CheckBundle(ctx, preview, options); err != nil || len(calls) != 0 || !reflect.DeepEqual(phases, []string{"cleanup_bundle_preflight"}) {
				t.Fatal("guard check pretended to execute or required optional interfaces", err, phases, calls)
			}
			phases = nil
			if _, err := service.Execute(ctx, preview, options); err != nil || !reflect.DeepEqual(calls, []string{"fixture:"}) || !reflect.DeepEqual(phases, []string{"cleanup_bundle_preflight", "cleanup_resource", "cleanup_execution"}) {
				t.Fatal("execution timing or dispatch changed", err, phases, calls)
			}
		})
	}
}
