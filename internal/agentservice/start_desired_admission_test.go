package agentservice

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// Retained desired-running state is execution authority for the supervisor.
// A refused request must not enable it, rewrite the registration, or clear retry
// evidence before native preparation has admitted the requested workspace.
func TestNativeStartDesiredStateWaitsForPreparation(t *testing.T) {
	for _, operation := range []string{"start", "restart"} {
		t.Run(operation, func(t *testing.T) {
			for _, desired := range []bool{false, true} {
				name := "stopped"
				if desired {
					name = "already_desired"
				}
				t.Run(name, func(t *testing.T) {
					for _, failure := range []string{"confirmation", "stale_plan", "busy_bundle", "native_error", "binding_drift", "cancelled_before", "cancelled_at_preparation", "workspace_drift", "uncertain_runner"} {
						t.Run(failure, func(t *testing.T) {
							reg := probeFixture(t)
							reg.Desired = desired
							dir, _ := instanceDir(reg.ID)
							file := filepath.Join(dir, "registration.json")
							if err := atomicJSON(file, reg); err != nil {
								t.Fatal(err)
							}
							before, err := os.ReadFile(file)
							if err != nil {
								t.Fatal(err)
							}
							info, err := os.Stat(file)
							if err != nil {
								t.Fatal(err)
							}
							ctx, cancel := context.WithCancel(context.Background())
							defer cancel()
							if failure == "cancelled_before" {
								cancel()
							}
							if failure == "uncertain_runner" {
								nativeStatusPeer(t, reg, `{"protocol":99,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
							}
							prepared := 0
							manager := New(workspaceFixture{
								lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
									binding := reg.Workspace
									if failure == "workspace_drift" {
										binding.ID = "changed"
									}
									return binding, nil
								},
								prepare: func(_ context.Context, req integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
									prepared++
									observed, err := os.ReadFile(file)
									if err != nil || !bytes.Equal(observed, before) {
										t.Error("desired state changed before native preparation admission", err)
									}
									if req.Expected == nil || *req.Expected != reg.Workspace || req.PlanID != "accepted-plan" || string(req.Note) != `{"path":"original-note"}` {
										t.Error("lost exact preparation request", req)
									}
									switch failure {
									case "binding_drift":
										changed := reg.Workspace
										changed.ID = "changed"
										return changed, integration.WorkspaceReconcilePlan{}, nil
									case "cancelled_at_preparation":
										cancel()
										return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
									default:
										return protocol.AgentWorkspaceBinding{}, integration.WorkspaceReconcilePlan{}, errors.New("fixture " + failure)
									}
								},
							})
							retry := retryState{attempts: 3, problem: "retained failure"}
							manager.retry[reg.ID] = retry
							_, err = manager.Handle(ctx, protocol.AgentRequest{ID: reg.ID, Operation: operation, PlanID: "accepted-plan", PreparedNote: []byte(`{"path":"original-note"}`)})
							if err == nil {
								t.Fatal("refused start became successful")
							}
							after, readErr := os.ReadFile(file)
							afterInfo, statErr := os.Stat(file)
							if readErr != nil || statErr != nil || !bytes.Equal(after, before) || !os.SameFile(info, afterInfo) {
								t.Error("refused start rewrote/enabled retained desired state", err, readErr, statErr)
							}
							if manager.retry[reg.ID] != retry {
								t.Error("refused start cleared retained retry evidence")
							}
							want := 1
							if failure == "cancelled_before" || failure == "workspace_drift" || failure == "uncertain_runner" {
								want = 0
							}
							if prepared != want {
								t.Errorf("preparations %d want %d", prepared, want)
							}
							// Run one real supervisor iteration after refusal. An originally stopped
							// registration must not become a queued automatic preparation/restart.
							if !desired {
								preparedBefore := prepared
								runCtx, stop := context.WithCancel(context.Background())
								manager.Run(runCtx, stop)
								stop()
								if prepared != preparedBefore {
									t.Error("refused request enabled later supervisor preparation")
								}
							}
						})
					}
				})
			}
		})
	}
}

func TestNativeStartAcceptedPreparationRetainsIntentOnLaterLaunchFailure(t *testing.T) {
	for _, operation := range []string{"start", "restart"} {
		t.Run(operation, func(t *testing.T) {
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			file := filepath.Join(dir, "registration.json")
			// An unavailable launcher makes the AFTER-admission boundary deterministic;
			// this test launches no process and never relaxes runtime authentication.
			t.Setenv("PATH", t.TempDir())
			prepared := 0
			manager := New(workspaceFixture{
				lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) { return reg.Workspace, nil },
				prepare: func(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
					prepared++
					observed, err := load(reg.ID)
					if err != nil || observed.Desired {
						t.Error("desired state enabled before successful preparation", err)
					}
					return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
				},
			})
			if _, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: operation}); err == nil {
				t.Fatal("missing launcher became success")
			}
			observed, err := load(reg.ID)
			expected := reg
			expected.Desired = true
			if err != nil || observed != expected || prepared != 1 {
				t.Fatal("accepted preparation/partial launch lost its durable intent", observed, err, prepared)
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeStartAlreadyRunningPersistsDesiredWithoutPreparation(t *testing.T) {
	reg := probeFixture(t)
	nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
	prepared := 0
	manager := nativeStatusManager(reg, &prepared)
	manager.retry[reg.ID] = retryState{attempts: 2, problem: "old failure"}
	if _, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: "start"}); err != nil {
		t.Fatal(err)
	}
	observed, err := load(reg.ID)
	expected := reg
	expected.Desired = true
	if err != nil || observed != expected || prepared != 0 {
		t.Fatal("healthy existing runner did not accept desired state", observed, err)
	}
	if _, exists := manager.retry[reg.ID]; exists {
		t.Fatal("accepted start retained obsolete retry evidence")
	}
}

func TestNativeStartPreviewRetainsDesiredAndRetryEvidence(t *testing.T) {
	for _, operation := range []string{"start", "restart"} {
		t.Run(operation, func(t *testing.T) {
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			file := filepath.Join(dir, "registration.json")
			before, _ := os.ReadFile(file)
			prepared := 0
			manager := New(workspaceFixture{
				lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
					t.Fatal("preview inspected runner workspace")
					return reg.Workspace, nil
				},
				prepare: func(_ context.Context, req integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
					prepared++
					if !req.Preview || req.Expected == nil || *req.Expected != reg.Workspace {
						t.Fatal("preview lost original binding")
					}
					return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
				},
			})
			retry := retryState{attempts: 4, problem: "retained failure"}
			manager.retry[reg.ID] = retry
			if _, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: operation, Preview: true}); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(before, after) || !reflect.DeepEqual(manager.retry[reg.ID], retry) || prepared != 1 {
				t.Fatal("preview changed retained supervision")
			}
		})
	}
}
