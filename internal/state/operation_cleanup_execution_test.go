package state

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"radar/internal/protocol"
)

func TestCleanupExecutionCaptureIsNativeImmutableClonedAndVolatile(t *testing.T) {
	store := phaseStore(t)
	targets := returnedTargetFixture(3)
	id := store.BeginOperation("cleanup", 7, "")
	v := protocol.OperationCleanupExecution{Stage: "provider_call", AttemptedTargets: 2}
	store.FinishCleanupOperation(id, protocol.ManagedOperation{Status: "partial", CompletedTargets: 1, TotalTargets: 3}, targets, &v)
	before := store.DashboardSnapshot()
	if before.Operations[0].CleanupExecution == nil || *before.Operations[0].CleanupExecution != v || before.Operations[0].CleanupExecutionUnavailable {
		t.Fatal("missing native dispatch evidence", before)
	}
	v.Stage = "private-caller-edit"
	before.Operations[0].CleanupExecution.Stage = "snapshot-edit"
	_, _, copied, _ := store.OperationInspectionSnapshot()
	copied[0].CleanupExecution.Stage = "inspection-edit"
	if store.DashboardSnapshot().Operations[0].CleanupExecution.Stage != "provider_call" {
		t.Fatal("aliased native memory")
	}
	first := store.DashboardSnapshot()
	store.FinishCleanupOperation(id, protocol.ManagedOperation{Status: "succeeded", CompletedTargets: 3, TotalTargets: 3}, targets, &protocol.OperationCleanupExecution{Stage: "completed", AttemptedTargets: 3})
	if !reflect.DeepEqual(first, store.DashboardSnapshot()) {
		t.Fatal("overwrote first return")
	}
	for _, kind := range []string{"cleanup", "task-note"} {
		x := store.BeginOperation(kind, 7, "")
		forged := protocol.ManagedOperation{Status: "succeeded", CompletedTargets: 3, TotalTargets: 3, CleanupExecution: &protocol.OperationCleanupExecution{Stage: "completed", AttemptedTargets: 3}, CleanupExecutionUnavailable: true}
		if kind == "cleanup" {
			store.FinishOperation(x, forged)
		} else {
			store.FinishCleanupOperation(x, forged, targets, forged.CleanupExecution)
		}
	}
	report := store.ReportResourceMutation(protocol.ResourceMutation{TaskID: 7})
	store.FinishCleanupOperation(report, protocol.ManagedOperation{Status: "succeeded", CompletedTargets: 3, TotalTargets: 3}, targets, &protocol.OperationCleanupExecution{Stage: "completed", AttemptedTargets: 3})
	for _, op := range store.DashboardSnapshot().Operations {
		if op.ID != id && (op.CleanupExecution != nil || op.CleanupExecutionUnavailable) {
			t.Fatal("non-native capture injected", op)
		}
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(store.state)
	if strings.Contains(string(raw), "cleanup_execution") || strings.Contains(string(raw), "attempted_targets") {
		t.Fatal("persisted volatile evidence")
	}
	if err := store.Reset(); err != nil || len(store.DashboardSnapshot().Operations) != 0 {
		t.Fatal("reset retained execution evidence", err)
	}
}

func TestCleanupExecutionCaptureRejectsContradictoryNativeData(t *testing.T) {
	for _, v := range []protocol.OperationCleanupExecution{{Stage: "private-error", AttemptedTargets: 2}, {Stage: "provider_call", AttemptedTargets: -1}, {Stage: "provider_call", AttemptedTargets: 4}, {Stage: "provider_call", AttemptedTargets: 1}, {Stage: "completed", AttemptedTargets: 3}, {Stage: "bundle_preflight", AttemptedTargets: 2}} {
		store := phaseStore(t)
		id := store.BeginOperation("cleanup", 7, "")
		store.FinishCleanupOperation(id, protocol.ManagedOperation{Status: "partial", CompletedTargets: 1, TotalTargets: 3}, returnedTargetFixture(3), &v)
		op := store.DashboardSnapshot().Operations[0]
		if op.CleanupExecution != nil || !op.CleanupExecutionUnavailable || !op.CleanupTargetsCaptured {
			t.Fatal("invalid observation or unrelated target evidence lost", op)
		}
	}
}
