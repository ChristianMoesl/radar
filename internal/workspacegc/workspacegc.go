package workspacegc

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"radar/internal/cleanup"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspace"
)

const DefaultRetention = 24 * time.Hour

type Options struct {
	Retention     time.Duration
	WorkspaceRoot string
}

type Candidate struct {
	TaskID      int
	RecordID    string
	DoneAt      string
	Path        string
	Branch      string
	SessionName string
	SandboxName string
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
		var err error
		root, err = workspace.DefaultRoot()
		if err != nil {
			return Plan{}, err
		}
	}
	root = filepath.Clean(root)

	refsByRecord := activeRefsByRecord(store.SourceRefs())
	plan := Plan{}
	for _, record := range store.Records() {
		if record.State != "done" || !doneLongEnough(record.DoneAt, now, retention) {
			continue
		}
		refs := refsByRecord[record.ID]
		task := record.Snapshot
		task.ID = record.NumericID
		task.Attention = record.State
		task.Reason = record.Reason
		task.DoneAt = record.DoneAt
		task.SourceRefs = append([]protocol.SourceRef(nil), refs...)
		for _, ref := range refs {
			if ref.Source != "git" || ref.Kind != "worktree" || strings.TrimSpace(ref.Path) == "" {
				continue
			}
			path := filepath.Clean(ref.Path)
			if !insideRoot(path, root) {
				plan.Skipped = append(plan.Skipped, Skipped{TaskID: record.NumericID, Path: path, Reason: "workspace is outside configured workspace root"})
				continue
			}
			sessionName, attached := matchingSession(refs, path)
			if attached {
				plan.Skipped = append(plan.Skipped, Skipped{TaskID: record.NumericID, Path: path, Reason: "tmux session is attached"})
				continue
			}
			plan.Candidates = append(plan.Candidates, Candidate{
				TaskID:      record.NumericID,
				RecordID:    record.ID,
				DoneAt:      record.DoneAt,
				Path:        path,
				Branch:      ref.Branch,
				SessionName: sessionName,
				SandboxName: matchingSandboxName(refs, path),
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
		selected, worktreeTarget := targetsForPath(preview, candidate.Path)
		if worktreeTarget == nil {
			result.skip(candidate, fmt.Errorf("matching worktree cleanup target was not found"), logger)
			continue
		}
		if worktreeTarget.Dirty {
			result.skip(candidate, fmt.Errorf("workspace has local changes"), logger)
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

func targetsForPath(preview protocol.CleanupPreview, path string) (protocol.CleanupPreview, *protocol.CleanupTarget) {
	selected := protocol.CleanupPreview{TaskID: preview.TaskID, TaskTitle: preview.TaskTitle}
	var worktree *protocol.CleanupTarget
	for _, target := range preview.Targets {
		if !samePath(target.Path, path) {
			continue
		}
		switch {
		case target.Source == "git" && target.Kind == "worktree":
			copy := target
			worktree = &copy
			selected.Targets = append(selected.Targets, target)
		case target.Source == "tmux" && target.Kind == "session":
			selected.Targets = append(selected.Targets, target)
		case target.Source == "sbx" && target.Kind == "sandbox":
			selected.Targets = append(selected.Targets, target)
		}
	}
	return selected, worktree
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

func matchingSession(refs []protocol.SourceRef, path string) (string, bool) {
	var sessionName string
	for _, ref := range refs {
		if ref.Source != "tmux" || ref.Kind != "session" || !samePath(ref.Path, path) {
			continue
		}
		if sessionAttached(ref) {
			return "", true
		}
		if sessionName == "" {
			sessionName = firstNonEmpty(ref.Metadata["switch_target"], ref.Metadata["session_id"], ref.Metadata["session"], ref.Title)
		}
	}
	return sessionName, false
}

func sessionAttached(ref protocol.SourceRef) bool {
	if strings.EqualFold(ref.Status, "attached") {
		return true
	}
	if countText := strings.TrimSpace(ref.Metadata["attached_count"]); countText != "" {
		count, err := strconv.Atoi(countText)
		return err == nil && count > 0
	}
	return false
}

func matchingSandboxName(refs []protocol.SourceRef, path string) string {
	for _, ref := range refs {
		if ref.Source != "sbx" || ref.Kind != "sandbox" || !samePath(ref.Path, path) {
			continue
		}
		return firstNonEmpty(ref.Metadata["name"], ref.Title, strings.TrimPrefix(ref.ID, "sbx:sandbox:"))
	}
	return ""
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
