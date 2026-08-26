package taskrefs

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func WorkspaceName(task protocol.Task) string {
	if ref, ok := WorkspaceCandidate(task); ok {
		return strings.TrimSpace(ref.Presentation.WorkspaceName)
	}
	return strings.TrimSpace(task.Title)
}

func TaskLinkingKey(task protocol.Task) string {
	for _, ref := range task.SourceRefs {
		if ref.Role == protocol.SourceRefRoleAuthoritative && ref.Lifecycle == protocol.SourceRefLifecycleWorkItem && ref.Authority == protocol.SourceRefAuthorityPrimary {
			if key := stableLinkingKey(ref); key != "" {
				return key
			}
		}
	}
	if ref, ok := WorkspaceCandidate(task); ok {
		if key := stableLinkingKey(ref); key != "" {
			return key
		}
	}
	for _, ref := range task.SourceRefs {
		if ref.Role == protocol.SourceRefRoleAuthoritative {
			if key := stableLinkingKey(ref); key != "" {
				return key
			}
		}
	}
	return ""
}

func stableLinkingKey(ref protocol.SourceRef) string {
	canonical := strings.TrimSpace(ref.CanonicalKey)
	for _, key := range ref.LinkingKeys {
		key = strings.TrimSpace(key)
		if key != "" && key == canonical {
			return key
		}
	}
	for _, key := range ref.LinkingKeys {
		if key = strings.TrimSpace(key); key != "" {
			return key
		}
	}
	return ""
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
		if ref.ProvidesWorkspace && ref.Path != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func WorkspaceCandidate(task protocol.Task) (protocol.SourceRef, bool) {
	var fallback protocol.SourceRef
	for _, ref := range task.SourceRefs {
		if ref.Role != protocol.SourceRefRoleAuthoritative || strings.TrimSpace(ref.Presentation.WorkspaceName) == "" {
			continue
		}
		if ref.Repo != "" && ref.Branch != "" {
			return ref, true
		}
		if fallback.ID == "" {
			fallback = ref
		}
	}
	return fallback, fallback.ID != ""
}

func DetectCurrentContext(ctx context.Context, workspaceProvider integration.WorkspaceProvider, multiplexer integration.MultiplexerProvider) protocol.CurrentContext {
	current := protocol.CurrentContext{}
	if cwd, err := os.Getwd(); err == nil {
		current.CWD = filepath.Clean(cwd)
		if workspaceProvider != nil {
			if workspace, ok, err := workspaceProvider.Current(ctx, cwd); err == nil && ok {
				current.Worktree = filepath.Clean(workspace.Path)
			}
		}
	}
	if multiplexer != nil {
		if session, ok, err := multiplexer.Current(ctx); err == nil && ok {
			current.SessionName = session.Name
			current.SessionID = session.ID
		}
	}
	return current
}

func TaskCursorForCurrent(tasks []protocol.Task, current protocol.CurrentContext, multiplexer integration.MultiplexerProvider) (int, bool) {
	if current.Worktree != "" || current.CWD != "" {
		for i, task := range tasks {
			for _, ref := range task.SourceRefs {
				if ref.ProvidesWorkspace && ref.Path != "" && CurrentPathMatches(ref.Path, current) {
					return i, true
				}
			}
		}
	}
	if multiplexer != nil && (current.SessionName != "" || current.SessionID != "") {
		for i, task := range tasks {
			for _, ref := range task.SourceRefs {
				if multiplexer.MatchesCurrent(ref, current) {
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

func SessionTarget(task protocol.Task, multiplexer integration.MultiplexerProvider) string {
	if multiplexer == nil {
		return ""
	}
	return multiplexer.Target(task)
}

func MetadataValue(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if metadata[key] != "" {
			return metadata[key]
		}
	}
	return ""
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
