package workspacegc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/phasetiming"
	"radar/internal/protocol"
	"radar/internal/state"
)

const DefaultRetention = 24 * time.Hour
const ExpiryRetention = 7 * 24 * time.Hour

// ExpiryAt is shared by GC and read-only presentation. Only registered
// workspaces have an automatic destructive deadline; observed worktrees do not.
func ExpiryAt(task protocol.Task) (time.Time, bool) {
	if task.Attention != "done" {
		return time.Time{}, false
	}
	doneAt, err := time.Parse(time.RFC3339, task.DoneAt)
	if err != nil || doneAt.IsZero() {
		return time.Time{}, false
	}
	for _, ref := range task.SourceRefs {
		if ref.ProvidesWorkspace && ref.WorkspaceID != "" && ref.Path != "" {
			return doneAt.Add(ExpiryRetention), true
		}
	}
	return time.Time{}, false
}

type Options struct {
	Retention       time.Duration
	WorkspaceRoot   string
	IgnoreRetention bool
	// GuardExecution serializes final lifecycle validation and removal with
	// authored mutations. Slow previews intentionally run outside this guard.
	GuardExecution func(context.Context, protocol.Task, func() error) error
}

type Candidate struct {
	TaskID      int
	RecordID    string
	DoneAt      string
	Path        string
	WorkspaceID string
	Reason      string
	Task        protocol.Task
}

type Skipped struct {
	TaskID      int
	Path        string
	Reason      string
	Decision    string // Allowlisted planner code; runtime failures remain unclassified.
	WorkspaceID string
}

type Plan struct {
	Candidates []Candidate
	Skipped    []Skipped
}

type Result struct {
	Retired []Candidate
	Deleted []Candidate
	Skipped []Skipped
}

func BuildPlan(store *state.Store, now time.Time, options Options) (Plan, error) {
	records, refs, _ := store.InspectionSnapshot()
	return InspectPlan(records, refs, now, options)
}

// InspectPlan is the production candidate/skip planner on one immutable snapshot.
// It performs no I/O or provider preflight and never establishes execution
// eligibility, observed presence, ownership, leases or user consent.
func InspectPlan(records []state.TaskRecord, refs []state.SourceRefRecord, now time.Time, options Options) (Plan, error) {
	root := strings.TrimSpace(options.WorkspaceRoot)
	if root == "" {
		return Plan{}, fmt.Errorf("workspace root is required")
	}
	root = filepath.Clean(root)

	refsByRecord := activeRefsByRecord(refs)
	plan := Plan{}
	for _, record := range records {
		refs := refsByRecord[record.ID]
		gate := retentionGate(record, refs, now, options)
		if gate.Decision == retentionHeld {
			plan.Skipped = append(plan.Skipped, Skipped{TaskID: record.NumericID, Reason: "agent work or result review is pending", Decision: retentionHeld})
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
				plan.Skipped = append(plan.Skipped, Skipped{TaskID: record.NumericID, Path: path, Reason: reason, Decision: "outside_workspace_root", WorkspaceID: workspaceID})
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

func Run(ctx context.Context, store *state.Store, cleanupService cleanup.Service, logger *slog.Logger, now time.Time, options Options) (Result, error) {
	ctx = phasetiming.WithLogger(ctx, logger)
	plan, err := BuildPlan(store, now, options)
	if err != nil {
		return Result{}, err
	}
	result := Result{Skipped: append([]Skipped(nil), plan.Skipped...)}
	for _, candidate := range plan.Candidates {
		mode, err := CandidateMode(candidate, now)
		if err != nil {
			result.skip(candidate, err, logger)
			continue
		}
		preview, err := cleanupService.Preview(ctx, candidate.Task, mode)
		if err != nil {
			if mode != integration.CleanupRetire && errors.Is(err, cleanup.ErrNoResources) && pathMissing(candidate.Path) {
				result.Deleted = append(result.Deleted, candidate)
				continue // Already gone; the post-GC local refresh drops stale refs.
			}
			result.skip(candidate, err, logger)
			continue
		}
		selected, workspaceTarget := SelectTargetsForCandidate(preview, candidate)
		if workspaceTarget == nil && (candidate.WorkspaceID != "" || !pathMissing(candidate.Path)) {
			result.skip(candidate, fmt.Errorf("matching workspace cleanup target was not found"), logger)
			continue
		}
		if mode != integration.CleanupRetire && len(selected.Targets) == 0 && pathMissing(candidate.Path) {
			result.Deleted = append(result.Deleted, candidate)
			continue
		}
		if messages := cleanup.AutomaticBlockingMessages(selected.Targets, mode == integration.CleanupExpired); len(messages) > 0 {
			result.skip(candidate, errors.New(messages[0]), logger)
			continue
		}
		runtimeChanged := false
		execute := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !candidateStillCurrent(store, candidate, now, options) {
				return fmt.Errorf("task completion changed or workspace candidate changed during cleanup; preview again")
			}
			cleaned, err := cleanupService.Execute(ctx, selected, cleanup.ExecuteOptions{Mode: mode})
			for _, target := range cleaned.Targets {
				runtimeChanged = runtimeChanged || target.Operation["runtime_retired"] == "true"
			}
			return err
		}
		var executeErr error
		if options.GuardExecution != nil {
			executeErr = options.GuardExecution(ctx, candidate.Task, execute)
		} else {
			executeErr = execute()
		}
		if err := executeErr; err != nil {
			result.skip(candidate, err, logger)
			continue
		}
		if mode == integration.CleanupRetire {
			if runtimeChanged {
				result.Retired = append(result.Retired, candidate)
			}
			continue
		}
		result.Deleted = append(result.Deleted, candidate)
		if logger != nil {
			logger.Info("workspace gc deleted workspace", "task", candidate.TaskID, "path", candidate.Path, "expired", mode == integration.CleanupExpired)
		}
	}
	return result, nil
}

// CandidateMode is the production next-stage choice, not execution permission.
func CandidateMode(candidate Candidate, now time.Time) (integration.CleanupMode, error) {
	if candidate.WorkspaceID == "" {
		return integration.CleanupSafe, nil
	}
	deadline, ok := ExpiryAt(candidate.Task)
	if !ok {
		return integration.CleanupSafe, fmt.Errorf("registered workspace completion time is invalid")
	}
	if !now.Before(deadline) {
		return integration.CleanupExpired, nil
	}
	return integration.CleanupRetire, nil
}

// Rebuild from one owned, atomic task/reference snapshot at the final guarded
// boundary. Completion alone cannot authorize a preview whose owning workspace
// or linked resources have since changed. Share the original planner, options
// and decision time; do not escalate retirement into expiry during slow reads.
// This is a point-in-time comparison, not a reservation against later changes.
func candidateStillCurrent(store *state.Store, candidate Candidate, now time.Time, options Options) bool {
	plan, err := BuildPlan(store, now, options)
	if err != nil {
		return false
	}
	for _, current := range plan.Candidates {
		if reflect.DeepEqual(current, candidate) {
			return true
		}
	}
	return false
}

// SelectTargetsForCandidate is the production GC resource-boundary selection.
// Selection alone does not validate the candidate, establish safety or execute.
func SelectTargetsForCandidate(preview protocol.CleanupPreview, candidate Candidate) (protocol.CleanupPreview, *protocol.CleanupTarget) {
	selected := protocol.CleanupPreview{TaskID: preview.TaskID, TaskTitle: preview.TaskTitle}
	var workspaceTarget *protocol.CleanupTarget
	for _, target := range preview.Targets {
		groupResource := candidate.WorkspaceID != "" && target.WorkspaceID == candidate.WorkspaceID
		pathResource := target.WorkspaceID == "" && samePath(target.Path, candidate.Path)
		if !groupResource && !pathResource {
			continue
		}
		if target.ProvidesWorkspace && samePath(target.Path, candidate.Path) && workspaceTarget == nil {
			copy := target
			workspaceTarget = &copy
		}
		selected.Targets = append(selected.Targets, target)
	}
	return selected, workspaceTarget
}

func (r *Result) skip(candidate Candidate, err error, logger *slog.Logger) {
	r.Skipped = append(r.Skipped, Skipped{TaskID: candidate.TaskID, Path: candidate.Path, Reason: err.Error()})
	if logger != nil {
		logger.Debug("workspace gc skipped", "task", candidate.TaskID, "path", candidate.Path, "error", err)
	}
}

func activeRefsByRecord(records []state.SourceRefRecord) map[string][]protocol.SourceRef {
	refsByRecord := map[string][]protocol.SourceRef{}
	for _, record := range records {
		if !record.Active || record.TaskRecordID == "" || record.Snapshot.ID == "" {
			continue
		}
		refsByRecord[record.TaskRecordID] = append(refsByRecord[record.TaskRecordID], record.Snapshot)
	}
	return refsByRecord
}

func doneLongEnough(doneAt string, now time.Time, retention time.Duration) bool {
	if strings.TrimSpace(doneAt) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, doneAt)
	if err != nil {
		return false
	}
	return !parsed.After(now.Add(-retention))
}

func samePath(left string, right string) bool {
	return strings.TrimSpace(left) != "" && strings.TrimSpace(right) != "" && filepath.Clean(left) == filepath.Clean(right)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (p Plan) String() string {
	return fmt.Sprintf("%d candidates, %d skipped", len(p.Candidates), len(p.Skipped))
}

func pathMissing(path string) bool {
	_, err := os.Lstat(path)
	return os.IsNotExist(err)
}
