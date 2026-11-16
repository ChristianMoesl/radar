package workspacegc

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

// Frozen pre-extraction candidate/skip contract from 28172c5. Do not replace
// with a call to InspectPlan: this guards production policy independently.
func frozenGCPlan(records []state.TaskRecord, refRecords []state.SourceRefRecord, now time.Time, options Options) (Plan, error) {
	root := strings.TrimSpace(options.WorkspaceRoot)
	if root == "" {
		return Plan{}, fmt.Errorf("workspace root is required")
	}
	root = filepath.Clean(root)

	refsByRecord := activeRefsByRecord(refRecords)
	plan := Plan{}
	for _, record := range records {
		refs := refsByRecord[record.ID]
		gate := retentionGate(record, refs, now, options)
		if gate.Decision == retentionHeld {
			plan.Skipped = append(plan.Skipped, Skipped{TaskID: record.NumericID, Reason: "agent work or result review is pending"})
			continue
		}
		if gate.Decision != retentionPassed {
			continue
		}
		task := record.Snapshot
		task.ID = record.NumericID
		task.Attention = record.State
		task.Reason = record.Reason
		task.DoneAt = record.DoneAt
		task.SourceRefs = append([]protocol.SourceRef(nil), refs...)
		seenGroups := map[string]bool{}
		orderedRefs := append([]protocol.SourceRef(nil), refs...)
		sort.SliceStable(orderedRefs, func(i, j int) bool {
			return orderedRefs[i].WorkspaceEntry && !orderedRefs[j].WorkspaceEntry
		})
		for _, ref := range orderedRefs {
			// Manual GC may bypass standalone retention, never either registered
			// workspace deadline. Explicit confirmed cleanup is a separate action.
			if ref.WorkspaceID != "" && !doneLongEnough(record.DoneAt, now, DefaultRetention) {
				continue
			}
			if !ref.ProvidesWorkspace || strings.TrimSpace(ref.Path) == "" {
				continue
			}
			workspaceID := strings.TrimSpace(ref.WorkspaceID)
			if workspaceID != "" && seenGroups[workspaceID] {
				continue
			}
			path := filepath.Clean(ref.Path)
			if workspaceID != "" {
				seenGroups[workspaceID] = true
			}
			if reason := cleanup.LocationIssue(path, root); reason != "" {
				plan.Skipped = append(plan.Skipped, Skipped{TaskID: record.NumericID, Path: path, Reason: reason})
				continue
			}
			plan.Candidates = append(plan.Candidates, Candidate{
				TaskID:      record.NumericID,
				RecordID:    record.ID,
				DoneAt:      record.DoneAt,
				Path:        path,
				WorkspaceID: workspaceID,
				Reason:      firstNonEmpty(record.Reason, "task done"),
				Task:        task,
			})
		}
	}
	return plan, nil
}

func TestSnapshotPlannerMatchesFrozenProductionContract(t *testing.T) {
	now := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	count := 0
	for flags := 0; flags < 256; flags++ {
		for _, age := range []time.Duration{0, 23 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 8 * 24 * time.Hour} {
			for _, done := range []string{now.Add(-age).Format(time.RFC3339), "invalid", ""} {
				record := state.TaskRecord{ID: "record", NumericID: 7, State: "done", DoneAt: done, Reason: "private-reason"}
				if flags&1 != 0 {
					record.State = "in_progress"
				}
				refs := []state.SourceRefRecord{}
				for i, path := range []string{"/workspaces/anchor/member", "/elsewhere/member", "/workspaces/anchor", "/workspaces", ""} {
					ref := protocol.SourceRef{ID: fmt.Sprintf("ref-%d", i), Path: path, ProvidesWorkspace: true}
					if flags&2 != 0 {
						ref.WorkspaceID = "group"
					}
					if flags&4 != 0 {
						ref.WorkspaceEntry = i == 2
					}
					if flags&8 != 0 {
						ref.ProvidesWorkspace = i != 0
					}
					if flags&16 != 0 {
						ref.Role = protocol.SourceRefRoleAuthoritative
						ref.Activity = protocol.ActivityBusy
					}
					if flags&32 != 0 {
						ref.AgentReview = true
					}
					refs = append(refs, state.SourceRefRecord{ID: ref.ID, TaskRecordID: record.ID, Active: flags&64 == 0, Snapshot: ref})
				}
				options := Options{WorkspaceRoot: "/workspaces", IgnoreRetention: flags&128 != 0}
				want, wantErr := frozenGCPlan([]state.TaskRecord{record}, refs, now, options)
				got, err := InspectPlan([]state.TaskRecord{record}, refs, now, options)
				if fmt.Sprint(err) != fmt.Sprint(wantErr) {
					t.Fatalf("errors differ: %v %v", err, wantErr)
				}
				for i := range got.Skipped {
					got.Skipped[i].Decision = ""
					got.Skipped[i].WorkspaceID = ""
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("flags=%d age=%s done=%q\ngot=%+v\nwant=%+v", flags, age, done, got, want)
				}
				for _, candidate := range got.Candidates {
					wantMode := integration.CleanupSafe
					invalid := false
					if candidate.WorkspaceID != "" {
						deadline, ok := ExpiryAt(candidate.Task)
						invalid = !ok
						wantMode = integration.CleanupRetire
						if !now.Before(deadline) {
							wantMode = integration.CleanupExpired
						}
					}
					mode, modeErr := CandidateMode(candidate, now)
					if (modeErr != nil) != invalid || (!invalid && mode != wantMode) {
						t.Fatalf("mode changed: %+v mode=%v err=%v", candidate, mode, modeErr)
					}
				}
				count++
			}
		}
	}
	if count != 3840 {
		t.Fatalf("parity coverage changed: %d", count)
	}
}

func TestSnapshotPlanValidatesRootAndDoesNotMutateInputs(t *testing.T) {
	record := state.TaskRecord{ID: "record", NumericID: 1, State: "done", DoneAt: "2026-01-02T10:20:30Z"}
	refs := []state.SourceRefRecord{{ID: "member", TaskRecordID: record.ID, Active: true, Snapshot: protocol.SourceRef{ID: "member", Path: "/workspaces/group/member", WorkspaceID: "group", ProvidesWorkspace: true}}, {ID: "anchor", TaskRecordID: record.ID, Active: true, Snapshot: protocol.SourceRef{ID: "anchor", Path: "/workspaces/group", WorkspaceID: "group", WorkspaceEntry: true, ProvidesWorkspace: true}}}
	before := append([]state.SourceRefRecord(nil), refs...)
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	plan, err := InspectPlan([]state.TaskRecord{record}, refs, now, Options{WorkspaceRoot: "/workspaces"})
	if err != nil || len(plan.Candidates) != 1 || plan.Candidates[0].Path != "/workspaces/group" {
		t.Fatalf("anchor/dedup changed: %+v %v", plan, err)
	}
	if !reflect.DeepEqual(before, refs) {
		t.Fatal("planner reordered caller refs")
	}
	if _, err := InspectPlan(nil, nil, now, Options{}); err == nil {
		t.Fatal("missing root accepted")
	}
	mode, err := CandidateMode(Candidate{WorkspaceID: "group", Task: protocol.Task{Attention: "done", DoneAt: "invalid"}}, now)
	if err == nil || mode != integration.CleanupSafe {
		t.Fatal("invalid completion accepted")
	}
}
