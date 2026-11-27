package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	workspacegroup "radar/internal/integration/workspace/group"
)

type delayedRepositoryPolicyDeadline struct {
	context.Context
	deadline time.Time
}

func (c delayedRepositoryPolicyDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func TestRepositoryPolicyNativeCancellationAndAbsoluteExpiry(t *testing.T) {
	for _, absent := range []bool{false, true} {
		for _, kind := range []string{"expired_at_entry", "expired_during_capture", "cancelled_during_capture", "allowed"} {
			t.Run(map[bool]string{false: "present/", true: "absent/"}[absent]+kind, func(t *testing.T) {
				root, repo, scope, id := repositoryPolicyFixture(t)
				path := filepath.Join(repo, ".radar.yaml")
				data := []byte("model: private-model\n")
				if !absent {
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.ReadFile(workspacegroup.Path(root))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				deadline := time.Now().Add(time.Hour)
				if kind == "expired_at_entry" {
					deadline = time.Now().Add(-time.Second)
				} else if kind == "expired_during_capture" {
					deadline = time.Now().Add(20 * time.Millisecond)
				}
				requestContext := delayedRepositoryPolicyDeadline{Context: ctx, deadline: deadline}
				calls := 0
				capture := func(repository string) (*repositoryConfigurationCapture, string) {
					calls++
					captured, reason := captureRepositoryConfiguration(repository)
					if captured == nil {
						t.Fatal("fixture capture failed", reason)
					}
					if kind == "expired_during_capture" {
						time.Sleep(max(time.Until(deadline), 0) + time.Millisecond)
					} else if kind == "cancelled_during_capture" {
						cancel()
					}
					return captured, reason
				}
				view, err := diagnosticRepositoryPolicy(requestContext, root, workspacegroup.ID(scope), id, scope, capture)
				if err != nil || view.ID != id || view.WorkspaceID != workspacegroup.ID(scope) || view.Origin != "current_registered_member_repository" {
					t.Fatal("fixture failed exact registration admission", view, err)
				}
				if kind == "allowed" {
					if !view.Available || len(view.Settings) != 10 || calls != 1 {
						t.Fatal("healthy capture lost declarations", view, calls)
					}
				} else {
					if view.Available || view.Evidence != "unavailable" || view.Reason != "cancelled_or_timed_out" || view.Settings == nil || len(view.Settings) != 0 {
						t.Fatal("cancelled/expired capture retained declarations", view)
					}
					wantCalls := 1
					if kind == "expired_at_entry" {
						wantCalls = 0
					}
					if calls != wantCalls {
						t.Fatal("expired request read configuration or retried capture", calls)
					}
				}
				if kind != "cancelled_during_capture" && requestContext.Err() != nil {
					t.Fatal("fixture did not isolate expiry before timer notification")
				}
				after, err := os.ReadFile(workspacegroup.Path(root))
				if err != nil || string(before) != string(after) {
					t.Fatal("capture changed registration", err)
				}
				if absent {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatal("capture created absent configuration", err)
					}
				} else if after, err := os.ReadFile(path); err != nil || string(after) != string(data) {
					t.Fatal("capture changed configuration", err)
				}
			})
		}
	}
}
