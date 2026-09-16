package cleanup

import (
	"context"
	"path/filepath"
	"strings"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// Issue identifies both the local resource and the reason cleanup cannot proceed.
type Issue struct {
	Ref     protocol.SourceRef
	Message string
}

// BlockingMessages is shared by collection and GC. Nonblocking effects (such as
// deleting a published local branch) are not unresolved issues.
func BlockingMessages(targets []protocol.CleanupTarget) []string {
	var messages []string
	for _, target := range targets {
		for _, safety := range target.Safety {
			if safety.BlocksAutomatic {
				messages = append(messages, safety.Message)
			}
		}
	}
	return messages
}

// LocationIssue is independent of task lifecycle and retention eligibility.
func LocationIssue(path, root string) string {
	path, root = filepath.Clean(path), filepath.Clean(root)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "workspace is outside configured workspace root"
	}
	return ""
}

// InUseIssues uses the same path association as the automatic-cleanup planner.
func InUseIssues(refs []protocol.SourceRef, path string) []Issue {
	var issues []Issue
	for _, ref := range refs {
		if ref.InUse && strings.TrimSpace(ref.Path) != "" && strings.TrimSpace(path) != "" && filepath.Clean(ref.Path) == filepath.Clean(path) {
			issues = append(issues, Issue{Ref: ref, Message: "a related local resource is in use"})
		}
	}
	return issues
}

// Unresolved combines collected provider checks with current local-use signals.
// It requires no I/O, so rendering and authored task mutations remain fast.
func Unresolved(task protocol.Task) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	add := func(issue Issue) {
		key := issue.Ref.ID + "\x00" + issue.Ref.Path + "\x00" + issue.Message
		if issue.Message != "" && !seen[key] {
			seen[key] = true
			issues = append(issues, issue)
		}
	}
	for _, ref := range task.SourceRefs {
		for _, message := range ref.CleanupIssues {
			add(Issue{Ref: ref, Message: message})
		}
		if ref.ProvidesWorkspace && ref.Path != "" {
			for _, issue := range InUseIssues(task.SourceRefs, ref.Path) {
				add(issue)
			}
		}
	}
	return issues
}

// ObserveIssues attaches provider safety checks without performing cleanup.
func ObserveIssues(ctx context.Context, provider integration.CleanupProvider, refs []protocol.SourceRef) {
	for i, ref := range refs {
		targets, err := provider.PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}})
		refs[i].CleanupIssues = BlockingMessages(targets)
		if err != nil {
			refs[i].CleanupIssues = append(refs[i].CleanupIssues, err.Error())
		}
	}
}
