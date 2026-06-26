package taskrefs

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/integration/tmux"
	"radar/internal/protocol"
	"radar/internal/workspace"
)

func WorkspaceName(task protocol.Task) string {
	if ref, ok := JiraIssue(task); ok {
		if title := strings.TrimSpace(ref.Title); title != "" {
			return title
		}
		if key := MetadataValue(ref.Metadata, "key"); key != "" {
			return key
		}
		if key, ok := strings.CutPrefix(ref.ID, "jira:issue:"); ok {
			return key
		}
	}
	if ref, ok := GitHubPullRequest(task); ok {
		if name := PullRequestWorkspaceName(ref); name != "" {
			return name
		}
	}
	return strings.TrimSpace(task.Title)
}

func Worktree(task protocol.Task) (protocol.SourceRef, bool) {
	refs := Worktrees(task)
	if len(refs) == 0 {
		return protocol.SourceRef{}, false
	}
	return refs[0], true
}

func CurrentWorktree(task protocol.Task, current protocol.CurrentContext) (protocol.SourceRef, bool) {
	for _, ref := range Worktrees(task) {
		if CurrentPathMatches(ref.Path, current) {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func Worktrees(task protocol.Task) []protocol.SourceRef {
	refs := make([]protocol.SourceRef, 0)
	for _, ref := range task.SourceRefs {
		if ref.Source == "git" && ref.Kind == "worktree" && ref.Path != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func JiraIssue(task protocol.Task) (protocol.SourceRef, bool) {
	for _, ref := range task.SourceRefs {
		if ref.Source == "jira" && ref.Kind == "issue" {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func GitHubPullRequest(task protocol.Task) (protocol.SourceRef, bool) {
	for _, ref := range task.SourceRefs {
		if ref.Source == "github" && ref.Kind == "pull_request" {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func PullRequestWorkspaceName(ref protocol.SourceRef) string {
	branch := strings.TrimSpace(ref.Branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "origin/")
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return branch
}

func DetectCurrentContext() protocol.CurrentContext {
	current := protocol.CurrentContext{}
	if cwd, err := os.Getwd(); err == nil {
		current.CWD = filepath.Clean(cwd)
		runner := workspace.ExecRunner{}
		if worktree, err := runner.Run(context.Background(), cwd, "git", "rev-parse", "--show-toplevel"); err == nil {
			current.Worktree = filepath.Clean(worktree)
		}
	}
	if os.Getenv("TMUX") != "" {
		runner := workspace.ExecRunner{}
		if output, err := runner.Run(context.Background(), current.CWD, "tmux", "display-message", "-p", "#{session_name}\t#{session_id}"); err == nil {
			fields := strings.Split(output, "\t")
			if len(fields) > 0 {
				current.SessionName = strings.TrimSpace(fields[0])
			}
			if len(fields) > 1 {
				current.SessionID = strings.TrimSpace(fields[1])
			}
		}
	}
	return current
}

func TaskCursorForCurrent(tasks []protocol.Task, current protocol.CurrentContext) (int, bool) {
	if current.Worktree != "" || current.CWD != "" {
		for i, task := range tasks {
			for _, ref := range task.SourceRefs {
				if ref.Source == "git" && ref.Kind == "worktree" && ref.Path != "" && CurrentPathMatches(ref.Path, current) {
					return i, true
				}
			}
		}
	}
	if current.SessionName != "" || current.SessionID != "" {
		for i, task := range tasks {
			if task.Kind == "session" && metadataMatchesSession(task.Metadata, current) {
				return i, true
			}
			for _, ref := range task.SourceRefs {
				if ref.Source == "tmux" && ref.Kind == "session" && tmux.SessionRefMatchesCurrent(ref, current) {
					return i, true
				}
			}
		}
	}
	return 0, false
}

func CurrentPathMatches(refPath string, current protocol.CurrentContext) bool {
	refPath = filepath.Clean(refPath)
	return samePath(current.Worktree, refPath) || sameOrDescendant(current.CWD, refPath)
}

func SessionTarget(task protocol.Task) string {
	return tmux.SessionTarget(task)
}

func MetadataValue(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if metadata[key] != "" {
			return metadata[key]
		}
	}
	return ""
}

func metadataMatchesSession(metadata map[string]string, current protocol.CurrentContext) bool {
	for _, key := range []string{"switch_target", "session_id", "session"} {
		value := metadata[key]
		if value != "" && (value == current.SessionID || value == current.SessionName) {
			return true
		}
	}
	return false
}

func samePath(left string, right string) bool {
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}

func sameOrDescendant(path string, root string) bool {
	if path == "" || root == "" {
		return false
	}
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
