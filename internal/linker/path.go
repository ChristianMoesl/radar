package linker

import (
	"path/filepath"
	"strings"

	"radar.nvim/internal/protocol"
)

func linkSourceRefsByPath(items []protocol.Task, sourceRefs []protocol.SourceRef) []protocol.Task {
	for i := range items {
		itemPaths := pathsForTask(items[i])
		for _, sourceRef := range sourceRefs {
			if pathsMatch(itemPaths, pathsForSourceRef(sourceRef)) {
				items[i].SourceRefs = appendSourceRef(items[i].SourceRefs, sourceRef)
			}
		}
	}
	return items
}

func pathsForTask(task protocol.Task) []string {
	paths := make([]string, 0)
	for _, sourceRef := range task.SourceRefs {
		paths = append(paths, pathsForSourceRef(sourceRef)...)
	}
	return uniquePaths(paths)
}

func pathsForSourceRef(sourceRef protocol.SourceRef) []string {
	paths := []string{sourceRef.Path}
	for _, key := range []string{"path", "working_directory", "session_path", "pane_current_path"} {
		if sourceRef.Metadata != nil {
			paths = append(paths, sourceRef.Metadata[key])
		}
	}
	return uniquePaths(paths)
}

func uniquePaths(values []string) []string {
	paths := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		path := cleanLinkPath(value)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

func cleanLinkPath(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) {
		return ""
	}
	return cleaned
}

func pathsMatch(left, right []string) bool {
	for _, l := range left {
		for _, r := range right {
			if sameOrNestedPath(l, r) {
				return true
			}
		}
	}
	return false
}

func sourceRefPathsMatch(left, right protocol.SourceRef) bool {
	return pathsMatch(pathsForSourceRef(left), pathsForSourceRef(right))
}

func sameOrNestedPath(left, right string) bool {
	return left == right || isNestedPath(left, right) || isNestedPath(right, left)
}

func isNestedPath(parent, child string) bool {
	if parent == "" || child == "" || parent == child {
		return false
	}
	parent = strings.TrimRight(parent, string(filepath.Separator)) + string(filepath.Separator)
	return strings.HasPrefix(child, parent)
}
