package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestNativeCleanupReturnedTargetsReachDiagnosticsWithoutExecutingOnInspection(t *testing.T) {
	for _, mode := range []string{"success", "partial", "cancelled-before-provider"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			store, err := state.NewStore(logger)
			if err != nil {
				t.Fatal(err)
			}
			provider := &managedCleanupSource{fakeCleanupSource: fakeCleanupSource{name: "git", targets: []protocol.CleanupTarget{
				{Source: "git", Kind: "worktree", ResourceID: "private-first", Title: "private-title", Operation: map[string]string{"secret": "private-operation"}},
				{Source: "git", Kind: "worktree", ResourceID: "private-second"},
			}}, started: make(chan struct{}), release: make(chan struct{}), fail: mode == "partial"}
			close(provider.release)
			s := New(store, logger, nil, nil, nil, integration.NewRegistry(), cleanup.New([]integration.CleanupProvider{provider}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled-before-provider" {
				cancel()
			}
			preview := previewFixtureCleanup(t, s)
			result, err := s.cleanup(ctx, &preview)
			if (err == nil) != (mode == "success") || !result.ResourceRefreshPending {
				t.Fatal("native cleanup changed", result, err)
			}
			before := store.DashboardSnapshot()
			calls := provider.calls
			view := managedPipeCall(t, s, protocol.Request{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "operations", ID: result.OperationID}})
			if !view.OK || view.Diagnostics == nil || view.Diagnostics.Schema != protocol.DiagnosticsVersion || view.Diagnostics.Total != 1 {
				t.Fatal("RPC bridge", view)
			}
			op := view.Diagnostics.Items[0].Operation
			stage := "completed"
			if mode == "partial" {
				stage = "provider_call"
			} else if mode == "cancelled-before-provider" {
				stage = "ownership_and_leases"
			}
			if op.CleanupExecutionEvidence != "native_executor_dispatch_boundaries_not_resource_verification" || op.CleanupExecution == nil || op.CleanupExecution.Stage != stage || op.CleanupExecution.AttemptedTargets != provider.calls {
				t.Fatal("native executor dispatch bridge lost", op)
			}
			if op.CleanupTargetEvidence != "native_target_returns_with_dispatch_boundaries_not_resource_verification" || len(op.CleanupTargets) != 2 || op.CleanupTargetsTruncated == nil || *op.CleanupTargetsTruncated {
				t.Fatal("capture missing", op)
			}
			for i, target := range op.CleanupTargets {
				want := "not_dispatched_by_cleanup_executor"
				if i < len(result.Targets) {
					want = "returned_success_unverified"
				} else if stage == "provider_call" && i == len(result.Targets) {
					want = "returned_error_may_be_partial"
				}
				if target.Index != i || target.Provider != "git" || target.Kind != "worktree" || target.Outcome != want {
					t.Fatal("wrong ordinal/evidence", target)
				}
			}
			raw, _ := json.Marshal(view)
			if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), preview.PlanID) || provider.calls != calls || store.Revision() != before.Revision {
				t.Fatal("inspection leaked or executed", string(raw))
			}
		})
	}
}
