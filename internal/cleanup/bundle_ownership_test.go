package cleanup

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// Keep the oracle independent of Service's copying implementation.
func ownBundleTargetsForTest(targets []protocol.CleanupTarget) []protocol.CleanupTarget {
	out := slices.Clone(targets)
	for i := range out {
		out[i].Operation = maps.Clone(out[i].Operation)
		out[i].Safety = slices.Clone(out[i].Safety)
	}
	return out
}

func rewriteBundleTargetsForTest(targets []protocol.CleanupTarget) {
	for i := range targets {
		targets[i].ResourceID = "replacement"
		targets[i].Path += "-replacement"
		if targets[i].Operation != nil {
			targets[i].Operation["identity"] = "replacement"
		}
		if len(targets[i].Safety) != 0 {
			targets[i].Safety[0].Message = "replacement"
		}
	}
}

func TestBundleOwnsInputsAcrossEveryProviderCallback(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		for _, dispatch := range []bool{false, true} {
			for _, boundary := range []string{"admission", "preflight", "cleanup", "caller-during-admission"} {
				t.Run(fmt.Sprintf("mode=%d/dispatch=%t/%s", mode, dispatch, boundary), func(t *testing.T) {
					root, preview := capabilityAdmissionFixture(t)
					t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
					t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
					// Deliberately share nested values across targets. Each callback
					// must own its copy, even within the same provider's bundle.
					operation := map[string]string{"identity": "original"}
					safety := []protocol.CleanupSafety{{Kind: "notice", Message: "original"}}
					for i := range preview.Targets {
						preview.Targets[i].ResourceID = fmt.Sprint(i)
						preview.Targets[i].Operation, preview.Targets[i].Safety = operation, safety
					}
					want := ownBundleTargetsForTest(preview.Targets)
					var borrowed [][]protocol.CleanupTarget
					calls, admissions, preflights := 0, 0, 0
					var providers []integration.CleanupProvider
					for _, name := range []string{"git", "workspace"} {
						p := &bundleCheckProvider{fakeProvider: fakeProvider{name: name}}
						p.admit = func(_ context.Context, targets []protocol.CleanupTarget, gotMode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
							admissions++
							var selected []protocol.CleanupTarget
							for _, target := range want {
								if target.Source == name {
									selected = append(selected, target)
								}
							}
							if gotMode != mode || !reflect.DeepEqual(targets, selected) {
								t.Errorf("admission rewritten: %+v", targets)
							}
							borrowed = append(borrowed, targets)
							if boundary == "admission" {
								rewriteBundleTargetsForTest(targets)
							}
							if boundary == "caller-during-admission" {
								rewriteBundleTargetsForTest(preview.Targets)
							}
							return p, func() {}, nil
						}
						p.preflight = func(_ context.Context, targets []protocol.CleanupTarget, gotMode integration.CleanupMode) error {
							preflights++
							if gotMode != mode || !reflect.DeepEqual(targets, want) {
								t.Errorf("preflight rewritten: %+v", targets)
							}
							for _, target := range want {
								assertPathHeld(t, target.Path)
							}
							borrowed = append(borrowed, targets)
							if boundary == "preflight" {
								rewriteBundleTargetsForTest(targets)
							}
							return nil
						}
						p.cleanup = func(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
							if !dispatch || calls >= len(want) || req.Mode != mode || !reflect.DeepEqual(req.Target, want[calls]) {
								t.Errorf("dispatch rewritten at %d: %+v", calls, req)
							}
							calls++
							borrowed = append(borrowed, []protocol.CleanupTarget{req.Target})
							if boundary == "cleanup" {
								req.Target.Operation["identity"] = "provider-result"
								req.Target.Safety[0].Message = "provider-result"
							}
							return req.Target, nil
						}
						providers = append(providers, p)
					}
					service := New(providers)
					var result protocol.CleanupResult
					var err error
					if dispatch {
						result, err = service.Execute(context.Background(), preview, ExecuteOptions{Mode: mode})
					} else {
						err = service.CheckBundle(context.Background(), preview, ExecuteOptions{Mode: mode})
					}
					wantCalls := 0
					if dispatch {
						wantCalls = len(want)
					}
					if err != nil || calls != wantCalls || admissions != 2 || preflights != 2 || len(result.Targets) != wantCalls {
						t.Fatalf("bundle changed: %+v %v calls=%d admits=%d preflights=%d", result, err, calls, admissions, preflights)
					}
					savedResult := ownBundleTargetsForTest(result.Targets)
					for _, targets := range borrowed {
						rewriteBundleTargetsForTest(targets)
					}
					if !reflect.DeepEqual(result.Targets, savedResult) {
						t.Error("retained callback input rewrote returned result")
					}
					if boundary != "caller-during-admission" && !reflect.DeepEqual(preview.Targets, want) {
						t.Error("provider callback rewrote caller's retained plan")
					}
				})
			}
		}
	}
}

func TestBundleOwnsReturnedPrefixBeforeLaterCallbacks(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		for _, outcome := range []string{"success", "provider-error", "cancel-after-first"} {
			t.Run(fmt.Sprintf("mode=%d/%s", mode, outcome), func(t *testing.T) {
				root, preview := capabilityAdmissionFixture(t)
				t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("native provider failed after first result")
				var first []protocol.CleanupTarget
				var want []protocol.CleanupTarget
				var providers []integration.CleanupProvider
				calls, releases := 0, 0
				for _, name := range []string{"git", "workspace"} {
					p := &bundleCheckProvider{fakeProvider: fakeProvider{name: name}}
					p.admit = func(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
						return p, func() { releases++; rewriteBundleTargetsForTest(first) }, nil
					}
					p.preflight = func(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error { return nil }
					p.cleanup = func(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
						calls++
						if calls == 1 {
							req.Target.Operation = map[string]string{"identity": "returned-native-evidence", "runtime_retired": "true"}
							req.Target.Safety = []protocol.CleanupSafety{{Kind: "notice", Message: "returned-native-evidence"}}
							first = []protocol.CleanupTarget{req.Target}
							want = ownBundleTargetsForTest(first)
							if outcome == "cancel-after-first" {
								cancel()
							}
						} else {
							rewriteBundleTargetsForTest(first)
							if outcome == "provider-error" {
								return protocol.CleanupTarget{}, failure
							}
							want = append(want, req.Target)
						}
						return req.Target, nil
					}
					providers = append(providers, p)
				}
				result, err := New(providers).Execute(ctx, preview, ExecuteOptions{Mode: mode})
				if (err == nil) != (outcome == "success") || outcome == "provider-error" && !errors.Is(err, failure) || outcome == "cancel-after-first" && !errors.Is(err, context.Canceled) || releases != 2 {
					t.Fatalf("native result/error or release changed: %+v %v releases=%d", result, err, releases)
				}
				if !reflect.DeepEqual(result.Targets, want) || result.ExecutionObservation == nil || result.ExecutionObservation.AttemptedTargets != calls {
					t.Fatalf("later provider/release rewrote completed prefix: %+v want=%+v", result, want)
				}
				rewriteBundleTargetsForTest(first)
				if !reflect.DeepEqual(result.Targets, want) {
					t.Fatal("provider retained mutable result after return")
				}
			})
		}
	}
}

func TestBundleCopiesPreserveNativeNilAndEmptyValues(t *testing.T) {
	for _, shape := range []string{"nil", "empty", "populated"} {
		t.Run(shape, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
			target := protocol.CleanupTarget{Source: "provider", Kind: "runtime", ResourceID: "original"}
			if shape != "nil" {
				target.Operation, target.Safety = map[string]string{}, []protocol.CleanupSafety{}
			}
			if shape == "populated" {
				target.Operation["identity"] = "original"
				target.Safety = append(target.Safety, protocol.CleanupSafety{Kind: "notice", Message: "original"})
			}
			p := borrowedPreviewProvider{name: "provider"}
			preview := protocol.CleanupPreview{TaskID: 7, TaskTitle: "ABC-123", Targets: []protocol.CleanupTarget{target}}
			result, err := New([]integration.CleanupProvider{p}).Execute(context.Background(), preview, ExecuteOptions{Mode: integration.CleanupSafe})
			if err != nil || result.TaskID != 7 || !reflect.DeepEqual(result.Targets, preview.Targets) {
				t.Fatalf("native values or nil/empty distinction changed: %+v %v", result, err)
			}
		})
	}
}
