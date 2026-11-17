package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/phasetiming"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/taskservice"
)

func managedPipeCall(t *testing.T, s *Server, request protocol.Request) protocol.Response {
	t.Helper()
	daemon, client := net.Pipe()
	defer client.Close()
	go s.handle(daemon)
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

type managedCleanupSource struct {
	fakeCleanupSource
	started, release chan struct{}
	calls            int
	fail             bool
}

func (s *managedCleanupSource) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	sample := phasetiming.Start(ctx, "cleanup_member_scan")
	sample()
	sample()
	phasetiming.Start(ctx, "private-fixture-command-error")()
	s.calls++
	if s.calls == 1 {
		close(s.started)
		<-s.release
	} else if s.fail {
		return protocol.CleanupTarget{}, errors.New("fixture second target unavailable")
	}
	return req.Target, nil
}

func TestManagedCleanupPublishesExecutionIdentityAndReconcilesSuccessOrPartialFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("partial_failure=", fail), func(t *testing.T) {
			t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			store, err := state.NewStore(logger)
			if err != nil {
				t.Fatal(err)
			}
			tasks := taskservice.New(store, logger, integration.NewRegistry())
			tasks.Refresh(context.Background(), true)
			var requests atomic.Int32
			tasks.SetLocalRefreshRequest(func() { requests.Add(1) })
			provider := &managedCleanupSource{fakeCleanupSource: fakeCleanupSource{name: "fixture"}, started: make(chan struct{}), release: make(chan struct{}), fail: fail}
			s := New(store, logger, nil, nil, nil, integration.NewRegistry(), cleanup.New([]integration.CleanupProvider{provider})).SetResourcePublication(tasks.MarkResourceRefreshPending, tasks.ResourceSnapshot)
			before := store.Revision()
			finished := make(chan protocol.Response, 1)
			go func() {
				finished <- managedPipeCall(t, s, protocol.Request{Method: "cleanup", Cleanup: &protocol.CleanupPreview{TaskID: 7, Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "worktree"}, {Source: "fixture", Kind: "sandbox"}}}})
			}()
			<-provider.started
			running := managedPipeCall(t, s, protocol.Request{Method: fmt.Sprintf("watch:%d", before)})
			if len(running.Operations) != 1 || running.Operations[0].Status != "running" || running.Operations[0].TaskID != 7 {
				t.Fatalf("blocked CLI/Pi cleanup not observable: %+v", running)
			}
			if running.Operations[0].StartedAt == "" || running.Operations[0].FinishedAt != "" {
				t.Fatal("blocked native handler timing must be start-only")
			}
			runningPhases := running.Operations[0].PhaseTimings
			if len(runningPhases) != 2 || runningPhases[0].Phase != "cleanup_bundle_preflight" || runningPhases[1].Phase != "cleanup_member_scan" || runningPhases[1].CompletedIntervals != 1 {
				t.Fatalf("unfinished/duplicate/private phase escaped: %+v", runningPhases)
			}
			close(provider.release)
			result := <-finished
			wantStatus, wantTargets := "succeeded", 2
			if fail {
				wantStatus, wantTargets = "partial", 1
			}
			if result.OK == fail || result.CleanupResult == nil || len(result.CleanupResult.Targets) != wantTargets || !result.ResourceRefreshPending || requests.Load() != 1 {
				t.Fatalf("result/reconciliation lost: %+v requests=%d", result, requests.Load())
			}
			op := result.Operations[0]
			if op.ID != running.Operations[0].ID || op.ID != result.OperationID || op.ID != result.CleanupResult.OperationID || op.Status != wantStatus || op.CompletedTargets != wantTargets || op.TotalTargets != 2 || result.Revision <= running.Revision {
				t.Fatalf("operation completion identity not fenced: %+v", result)
			}
			if op.StartedAt != running.Operations[0].StartedAt || op.FinishedAt == "" || op.ElapsedMilliseconds < 0 {
				t.Fatal("native cleanup completion lost timing")
			}
			diagnostic := managedPipeCall(t, s, protocol.Request{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "operations", ID: op.ID}})
			if !diagnostic.OK || diagnostic.Diagnostics == nil || diagnostic.Diagnostics.Total != 1 || diagnostic.Diagnostics.Items[0].Operation.TimingEvidence != "native_handler_monotonic_elapsed" || *diagnostic.Diagnostics.Items[0].Operation.ElapsedMilliseconds != op.ElapsedMilliseconds {
				t.Fatalf("diagnostic RPC lost native handler timing: %+v", diagnostic)
			}
			phaseView := diagnostic.Diagnostics.Items[0].Operation
			if phaseView.PhaseTimingEvidence != "native_completed_phase_aggregates" || !reflect.DeepEqual(phaseView.PhaseTimings, op.PhaseTimings) || len(op.PhaseTimings) != 5 {
				t.Fatalf("native phase/RPC bridge lost: %+v", phaseView)
			}
			for _, phase := range op.PhaseTimings {
				if phase.Phase == "cleanup_member_scan" && phase.CompletedIntervals != 2 {
					t.Fatal("duplicate phase or failed interval missed")
				}
			}
			tasks.Refresh(context.Background(), true)
			fresh := managedPipeCall(t, s, protocol.Request{Method: fmt.Sprintf("watch:%d", result.Revision)})
			if fresh.ResourceRefreshPending || fresh.Revision <= result.Revision || !reflect.DeepEqual(fresh.Operations[0], op) {
				t.Fatalf("unchanged authoritative reread failed to publish freshness: %+v", fresh)
			}
		})
	}
}

func TestClientWorkspaceReportIsNotDaemonExecution(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	tasks := taskservice.New(store, logger, integration.NewRegistry())
	var requests atomic.Int32
	tasks.SetLocalRefreshRequest(func() { requests.Add(1) })
	s := New(store, logger, nil, nil, nil, integration.NewRegistry(), cleanup.New(nil)).SetResourcePublication(tasks.MarkResourceRefreshPending, tasks.ResourceSnapshot)
	response := managedPipeCall(t, s, protocol.Request{Method: "resources-mutated", ResourceMutation: &protocol.ResourceMutation{TaskID: 9, WorkspaceID: "workspace-one"}})
	if !response.OK || !response.ResourceRefreshPending || requests.Load() != 1 || len(response.Operations) != 1 {
		t.Fatalf("report not acknowledged/scheduled: %+v", response)
	}
	op := response.Operations[0]
	if op.Status != "reported" || op.TaskID != 9 || op.WorkspaceID != "workspace-one" {
		t.Fatalf("client report claimed daemon execution or lost target: %+v", op)
	}
	if op.StartedAt != "" || op.FinishedAt != "" || op.ElapsedMilliseconds != 0 {
		t.Fatal("client report fabricated native timing")
	}
	unscoped := managedPipeCall(t, s, protocol.Request{Method: "resources-mutated", ResourceMutation: &protocol.ResourceMutation{}})
	if !unscoped.OK || !unscoped.ResourceRefreshPending || unscoped.OperationID != "" || len(unscoped.Operations) != 1 || requests.Load() != 2 {
		t.Fatal("an unidentified partial change must invalidate without inventing operation scope")
	}
}

func TestCleanupResponseDoesNotUndoCompletedReconciliation(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	tasks := taskservice.New(store, logger, integration.NewRegistry())
	tasks.Refresh(context.Background(), true)
	// Simulate the background collector winning before response construction.
	mark := func() { tasks.MarkResourceRefreshPending(); tasks.Refresh(context.Background(), true) }
	s := New(store, logger, nil, nil, nil, integration.NewRegistry(), cleanup.New([]integration.CleanupProvider{fakeCleanupSource{name: "fixture"}})).SetResourcePublication(mark, tasks.ResourceSnapshot)
	response := managedPipeCall(t, s, protocol.Request{Method: "cleanup", Cleanup: &protocol.CleanupPreview{TaskID: 7, Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "worktree"}}}})
	if !response.OK || response.ResourceRefreshPending || response.CleanupResult == nil || response.CleanupResult.ResourceRefreshPending {
		t.Fatalf("fresh snapshot mislabeled pending: %+v", response)
	}
}

func TestExternalTaskMutationResponseCorrelatesItsOwnOperation(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	s := New(store, logger, nil, nil, nil, integration.NewRegistry(), cleanup.New(nil)).SetTaskMutation(func(_ context.Context, method string, mutation *protocol.TaskMutation) (protocol.Task, error) {
		if method != "task-title" || mutation.TaskID != 7 {
			t.Error("wrong mutation routed")
		}
		return protocol.Task{ID: 7, Title: mutation.Title}, nil
	})
	response := managedPipeCall(t, s, protocol.Request{Method: "task-title", TaskMutation: &protocol.TaskMutation{TaskID: 7, Title: "Updated title"}})
	if !response.OK || response.Task == nil || response.Task.Title != "Updated title" || len(response.Operations) != 1 || response.OperationID != response.Operations[0].ID || response.Operations[0].TaskID != 7 || response.Operations[0].Status != "succeeded" {
		t.Fatalf("task result lost execution identity: %+v", response)
	}
}
