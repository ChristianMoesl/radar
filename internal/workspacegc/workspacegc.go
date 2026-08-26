package workspacegc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"radar/internal/cleanup"
	"radar/internal/protocol"
	"radar/internal/state"
)

const DefaultRetention = 24 * time.Hour

type Options struct {
	Retention       time.Duration
	WorkspaceRoot   string
	IgnoreRetention bool
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
	TaskID int
	Path   string
	Reason string
}

type Plan struct {
	Candidates []Candidate
	Skipped    []Skipped
}

type Result struct {
	Deleted []Candidate
	Skipped []Skipped
}

func BuildPlan(store *state.Store, now time.Time, options Options) (Plan, error) {
	retention := options.Retention
	if retention == 0 {
		retention = DefaultRetention
	}
	root := strings.TrimSpace(options.WorkspaceRoot)
	if root == "" {
		return Plan{}, fmt.Errorf("workspace root is required")
	}
	root = filepath.Clean(root)

	refsByRecord := activeRefsByRecord(store.SourceRefs())
	plan := Plan{}
	for _, record := range store.Records() {
		if record.State != "done" || (!options.IgnoreRetention && !doneLongEnough(record.DoneAt, now, retention)) {
			continue
		}
		refs := refsByRecord[record.ID]
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
			if !insideRoot(path, root) {
				plan.Skipped = append(plan.Skipped, Skipped{TaskID: record.NumericID, Path: path, Reason: "workspace is outside configured workspace root"})
				continue
			}
			if relatedResourceInUse(refs, path) {
				plan.Skipped = append(plan.Skipped, Skipped{TaskID: record.NumericID, Path: path, Reason: "a related local resource is in use"})
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
	plan, err := BuildPlan(store, now, options)
	if err != nil {
		return Result{}, err
	}
	result := Result{Skipped: append([]Skipped(nil), plan.Skipped...)}
	for _, candidate := range plan.Candidates {
		preview, err := cleanupService.Preview(ctx, candidate.Task)
		if err != nil {
			result.skip(candidate, err, logger)
			continue
		}
		selected, workspaceTarget := targetsForCandidate(preview, candidate)
		if workspaceTarget == nil && candidate.WorkspaceID == "" {
			result.skip(candidate, fmt.Errorf("matching workspace cleanup target was not found"), logger)
			continue
		}
		blocked := false
		for _, target := range selected.Targets {
			for _, safety := range target.Safety {
				if !safety.BlocksAutomatic {
					continue
				}
				result.skip(candidate, errors.New(safety.Message), logger)
				blocked = true
				break
			}
			if blocked {
				break
			}
		}
		if blocked {
			continue
		}
		if _, err := cleanupService.Execute(ctx, selected, cleanup.ExecuteOptions{Force: false}); err != nil {
			result.skip(candidate, err, logger)
			continue
		}
		result.Deleted = append(result.Deleted, candidate)
		if logger != nil {
			logger.Info("workspace gc deleted workspace", "task", candidate.TaskID, "path", candidate.Path)
		}
	}
	return result, nil
}

func targetsForCandidate(preview protocol.CleanupPreview, candidate Candidate) (protocol.CleanupPreview, *protocol.CleanupTarget) {
	selected := protocol.CleanupPreview{TaskID: preview.TaskID, TaskTitle: preview.TaskTitle}
	var workspaceTarget *protocol.CleanupTarget
	for _, target := range preview.Targets {
		groupResource := candidate.WorkspaceID != "" && target.WorkspaceID == candidate.WorkspaceID
		pathResource := samePath(target.Path, candidate.Path)
		if !groupResource && !pathResource {
			continue
		}
		if target.ProvidesWorkspace && workspaceTarget == nil {
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

func insideRoot(path string, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func relatedResourceInUse(refs []protocol.SourceRef, path string) bool {
	for _, ref := range refs {
		if ref.InUse && samePath(ref.Path, path) {
			return true
		}
	}
	return false
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
