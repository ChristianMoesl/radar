package state

import (
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"radar/internal/protocol"
)

func returnedTargetFixture(n int) []protocol.CleanupTarget {
	targets := make([]protocol.CleanupTarget, n)
	for i := range targets {
		targets[i] = protocol.CleanupTarget{Source: "git", Kind: "worktree", ResourceID: "private-resource", Path: "/private/path", Title: "private-title", Operation: map[string]string{"secret": "private-operation"}, Safety: []protocol.CleanupSafety{{Message: "private-message"}}}
	}
	return targets
}

func TestCleanupReturnedTargetsAreNativeBoundedPrivateAndImmutable(t *testing.T) {
	for _, sample := range []struct {
		status           string
		completed, total int
	}{{"failed", 0, 3}, {"partial", 1, 3}, {"succeeded", 3, 3}, {"partial", 19, 20}, {"succeeded", 16, 16}, {"failed", 0, 17}, {"succeeded", 512, 512}} {
		t.Run(sample.status+"/"+strconv.Itoa(sample.total), func(t *testing.T) {
			store := phaseStore(t)
			id := store.BeginOperation("cleanup", 7, "")
			if store.DashboardSnapshot().Operations[0].CleanupTargetsCaptured {
				t.Fatal("running capture invented")
			}
			targets := returnedTargetFixture(sample.total)
			store.FinishCleanupOperation(id, protocol.ManagedOperation{Status: sample.status, CompletedTargets: sample.completed, TotalTargets: sample.total}, targets, nil)
			first := store.DashboardSnapshot()
			op := first.Operations[0]
			if !op.CleanupTargetsCaptured || op.CleanupTargetsUnavailable || len(op.CleanupTargets) != min(sample.total, 16) || op.CleanupTargetsTruncated != (sample.total > 16) {
				t.Fatal("capture/bounds", op)
			}
			seen := map[string]bool{}
			for i, target := range op.CleanupTargets {
				outcome := "not_completed_may_be_partial"
				if i < sample.completed {
					outcome = "returned_success_unverified"
				}
				if target.Index != i || len(target.ID) != 79 || !strings.HasPrefix(target.ID, "cleanup-result:") || seen[target.ID] || target.Provider != "git" || target.Kind != "worktree" || target.Outcome != outcome {
					t.Fatal("ordinal/evidence", target)
				}
				seen[target.ID] = true
			}
			raw, _ := json.Marshal(op)
			if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "/private/") {
				t.Fatal("private target retained", string(raw))
			}
			targets[0].ResourceID = "caller-edit"
			store.FinishCleanupOperation(id, protocol.ManagedOperation{Status: "failed", TotalTargets: sample.total}, targets, nil)
			if !reflect.DeepEqual(first, store.DashboardSnapshot()) {
				t.Fatal("duplicate changed first completion")
			}
			first.Operations[0].CleanupTargets[0].Provider = "caller-edit"
			_, _, copied, _ := store.OperationInspectionSnapshot()
			copied[0].CleanupTargets[0].ID = "caller-edit"
			if store.DashboardSnapshot().Operations[0].CleanupTargets[0].Provider != "git" || store.DashboardSnapshot().Operations[0].CleanupTargets[0].ID == "caller-edit" {
				t.Fatal("snapshot aliases store")
			}
			if err := store.Save(); err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(store.state)
			if strings.Contains(string(raw), "cleanup_targets") || strings.Contains(string(raw), "cleanup-result:") {
				t.Fatal("target history persisted")
			}
			restarted, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil || len(restarted.DashboardSnapshot().Operations) != 0 {
				t.Fatal("restart replayed target history", err)
			}
			if err := store.Reset(); err != nil || len(store.DashboardSnapshot().Operations) != 0 {
				t.Fatal("reset retained targets", err)
			}
		})
	}
}

func TestCleanupReturnedTargetsRejectUnsafeAndIncompleteInputWithoutRawFallback(t *testing.T) {
	for _, mode := range []string{"empty", "excess", "private provider", "private kind", "missing identity", "identity bound", "path bound", "aggregate bound", "total mismatch", "negative", "excess complete", "false success", "false failed", "false partial", "off-summary unsupported"} {
		t.Run(mode, func(t *testing.T) {
			store := phaseStore(t)
			id := store.BeginOperation("cleanup", 7, "")
			targets := returnedTargetFixture(3)
			result := protocol.ManagedOperation{Status: "partial", CompletedTargets: 1, TotalTargets: 3}
			switch mode {
			case "empty":
				targets = nil
				result.TotalTargets = 0
			case "excess":
				targets = returnedTargetFixture(513)
				result.TotalTargets = len(targets)
			case "private provider":
				targets[0].Source = "private-provider"
			case "private kind":
				targets[0].Kind = "private-command"
			case "missing identity":
				targets[0].ResourceID = ""
			case "identity bound":
				targets[0].ResourceID = strings.Repeat("x", 4097)
			case "path bound":
				targets[0].Path = strings.Repeat("x", 4097)
			case "aggregate bound":
				targets = returnedTargetFixture(300)
				result.TotalTargets = 300
				for i := range targets {
					targets[i].Path = strings.Repeat("x", 4096)
				}
			case "total mismatch":
				result.TotalTargets = 4
			case "negative":
				result.CompletedTargets = -1
			case "excess complete":
				result.CompletedTargets = 4
			case "false success":
				result.Status = "succeeded"
			case "false failed":
				result.Status = "failed"
			case "false partial":
				result.CompletedTargets = 0
			case "off-summary unsupported":
				targets = returnedTargetFixture(20)
				result.TotalTargets = 20
				targets[19].Kind = "private-command"
			}
			store.FinishCleanupOperation(id, result, targets, nil)
			op := store.DashboardSnapshot().Operations[0]
			if !op.CleanupTargetsUnavailable || op.CleanupTargetsCaptured || len(op.CleanupTargets) != 0 || op.CleanupTargetsTruncated {
				t.Fatal("bad capture returned plausible subset", op)
			}
		})
	}
}

func TestCleanupReturnedTargetsCannotBeInjectedByGenericResultsOrReports(t *testing.T) {
	store := phaseStore(t)
	for _, kind := range []string{"cleanup", "task-note"} {
		id := store.BeginOperation(kind, 7, "")
		forged := protocol.ManagedOperation{Status: "succeeded", CompletedTargets: 1, TotalTargets: 1, CleanupTargetsCaptured: true, CleanupTargets: []protocol.OperationCleanupTarget{{ID: "private-forged"}}}
		if kind == "cleanup" {
			store.FinishOperation(id, forged)
		} else {
			store.FinishCleanupOperation(id, forged, returnedTargetFixture(1), nil)
		}
	}
	report := store.ReportResourceMutation(protocol.ResourceMutation{TaskID: 7})
	before := store.Revision()
	store.FinishCleanupOperation(report, protocol.ManagedOperation{Status: "succeeded", CompletedTargets: 1, TotalTargets: 1}, returnedTargetFixture(1), nil)
	if before != store.Revision() {
		t.Fatal("report became native")
	}
	for _, op := range store.DashboardSnapshot().Operations {
		if op.CleanupTargetsCaptured || op.CleanupTargetsUnavailable || len(op.CleanupTargets) > 0 {
			t.Fatal("forged/native-other/report target capture", op)
		}
	}
	a := store.BeginOperation("cleanup", 7, "")
	store.FinishCleanupOperation(a, protocol.ManagedOperation{Status: "succeeded", CompletedTargets: 1, TotalTargets: 1}, returnedTargetFixture(1), nil)
	b := store.BeginOperation("cleanup", 7, "")
	store.FinishCleanupOperation(b, protocol.ManagedOperation{Status: "succeeded", CompletedTargets: 1, TotalTargets: 1}, returnedTargetFixture(1), nil)
	ops := store.DashboardSnapshot().Operations
	if ops[len(ops)-1].CleanupTargets[0].ID == ops[len(ops)-2].CleanupTargets[0].ID {
		t.Fatal("opaque IDs claimed cross-operation identity")
	}
	for range operationHistoryLimit {
		store.ReportResourceMutation(protocol.ResourceMutation{TaskID: 7})
	}
	for _, op := range store.DashboardSnapshot().Operations {
		if len(op.CleanupTargets) > 0 {
			t.Fatal("target summaries bypassed terminal retention")
		}
	}
}

func TestCleanupReturnedTargetKindsRemainFixed(t *testing.T) {
	targets := []protocol.CleanupTarget{{Source: "git", Kind: "worktree", ResourceID: "private"}, {Source: "sbx", Kind: "sandbox", ResourceID: "private"}, {Source: "tmux", Kind: "session", ResourceID: "private"}, {Source: "workspace", Kind: "workspace", ResourceID: "private"}, {Source: "workspace", Kind: "missing_member", ResourceID: "private"}}
	views, ok := cleanupReturnedTargets("op", protocol.ManagedOperation{Status: "succeeded", CompletedTargets: 5, TotalTargets: 5}, targets)
	if !ok || len(views) != 5 {
		t.Fatal("fixed provider-kind evidence lost")
	}
	for i, v := range views {
		if v.Provider != targets[i].Source || v.Kind != targets[i].Kind || v.Outcome != "returned_success_unverified" {
			t.Fatal("changed provider-kind projection", v)
		}
	}
}
