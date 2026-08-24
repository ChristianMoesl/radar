package workspacegc

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"radar/internal/cleanup"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspace"
	"radar/internal/workspacegroup"
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
	Branch      string
	SessionName string
	SandboxName string
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
		var err error
		root, err = workspace.DefaultRoot()
		if err != nil {
			return Plan{}, err
		}
	}
	root = filepath.Clean(root)

	refsByRecord := activeRefsByRecord(store.SourceRefs())
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return Plan{}, err
	}
	groups := map[string]workspacegroup.Workspace{}
	for _, group := range registry.Workspaces {
		groups[group.ID] = group
	}
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
			return orderedRefs[i].Source == "workspace" && orderedRefs[j].Source != "workspace"
		})
		for _, ref := range orderedRefs {
			managedWorkspace := ref.Source == "workspace" && ref.Kind == "workspace"
			gitWorkspace := ref.Source == "git" && ref.Kind == "worktree"
			if (!managedWorkspace && !gitWorkspace) || strings.TrimSpace(ref.Path) == "" {
				continue
			}
			workspaceID := strings.TrimSpace(ref.Metadata["workspace_id"])
			if managedWorkspace && workspaceID == "" {
				workspaceID = strings.TrimPrefix(ref.ID, "workspace:")
			}
			if workspaceID != "" && seenGroups[workspaceID] {
				continue
			}
			path := filepath.Clean(ref.Path)
			if group, ok := groups[workspaceID]; ok {
				path = group.Path
				seenGroups[workspaceID] = true
			}
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
		selected, worktreeTarget := targetsForCandidate(preview, candidate)
		if worktreeTarget == nil && candidate.WorkspaceID == "" {
			result.skip(candidate, fmt.Errorf("matching worktree cleanup target was not found"), logger)
			continue
		}
		dirty := worktreeTarget != nil && worktreeTarget.Dirty
		for _, target := range selected.Targets {
			if target.Source == "git" && target.Kind == "worktree" && target.Dirty {
				dirty = true
			}
		}
		if dirty {
			result.skip(candidate, fmt.Errorf("workspace has local changes"), logger)
			continue
		}
		unsafeBranch := false
		for _, target := range selected.Targets {
			if target.Source != "git" || target.Kind != "worktree" || !target.DeleteBranch {
				continue
			}
			switch {
			case target.PublicationUnknown:
				result.skip(candidate, fmt.Errorf("branch publication could not be verified"), logger)
				unsafeBranch = true
			case target.Unpublished:
				result.skip(candidate, fmt.Errorf("branch has commits not found on a remote-tracking branch"), logger)
				unsafeBranch = true
			}
			if unsafeBranch {
				break
			}
		}
		if unsafeBranch {
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
	var worktree *protocol.CleanupTarget
	groupRefIDs := map[string]bool{}
	if candidate.WorkspaceID != "" {
		for _, ref := range candidate.Task.SourceRefs {
			if ref.Source == "git" && ref.Kind == "worktree" && ref.Metadata["workspace_id"] == candidate.WorkspaceID {
				groupRefIDs[ref.ID] = true
			}
		}
	}
	for _, target := range preview.Targets {
		groupWorktree := candidate.WorkspaceID != "" && target.Source == "git" && target.Kind == "worktree" && groupRefIDs[target.SourceRefID]
		pathResource := samePath(target.Path, candidate.Path) && ((target.Source == "git" && target.Kind == "worktree") || (target.Source == "tmux" && target.Kind == "session") || (target.Source == "sbx" && target.Kind == "sandbox") || (target.Source == "workspace" && target.Kind == "workspace"))
		if !groupWorktree && !pathResource {
			continue
		}
		if target.Source == "git" && target.Kind == "worktree" && worktree == nil {
			copy := target
			worktree = &copy
		}
		selected.Targets = append(selected.Targets, target)
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
