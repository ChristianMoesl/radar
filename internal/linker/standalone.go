package linker

import "radar.nvim/internal/protocol"

func standaloneSourceRefTasks(items []protocol.Task, sourceRefs []protocol.SourceRef) []protocol.Task {
	attached := map[string]bool{}
	for _, item := range items {
		for _, sourceRef := range item.SourceRefs {
			attached[sourceRef.ID] = true
		}
	}

	unattached := make([]protocol.SourceRef, 0)
	for _, sourceRef := range sourceRefs {
		if attached[sourceRef.ID] || ignoredSourceRef(sourceRef) {
			continue
		}
		unattached = append(unattached, sourceRef)
	}

	groups := sourceRefGroups(unattached)
	standalone := make([]protocol.Task, 0, len(groups))
	for _, group := range groups {
		standalone = append(standalone, taskFromSourceRefs(group))
	}
	return standalone
}

func sourceRefGroups(sourceRefs []protocol.SourceRef) [][]protocol.SourceRef {
	groups := make([][]protocol.SourceRef, 0)
	used := make([]bool, len(sourceRefs))
	for i := range sourceRefs {
		if used[i] {
			continue
		}
		group := make([]protocol.SourceRef, 0)
		queue := []int{i}
		used[i] = true
		for len(queue) > 0 {
			idx := queue[0]
			queue = queue[1:]
			group = append(group, sourceRefs[idx])
			for j := range sourceRefs {
				if used[j] || !sourceRefsRelated(sourceRefs[idx], sourceRefs[j]) {
					continue
				}
				used[j] = true
				queue = append(queue, j)
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func sourceRefsRelated(left, right protocol.SourceRef) bool {
	return ticketKeysMatch(left, right) || sourceRefPathsMatch(left, right)
}

func taskFromSourceRefs(sourceRefs []protocol.SourceRef) protocol.Task {
	primary := sourceRefs[0]
	reason := primary.Source + " " + primary.Kind
	return protocol.Task{
		Kind:       primary.Source + "_" + primary.Kind,
		Title:      primary.Title,
		Repo:       primary.Repo,
		URL:        primary.URL,
		Attention:  "in_progress",
		Reason:     reason,
		SourceRefs: sourceRefs,
	}
}

func ignoredSourceRef(sourceRef protocol.SourceRef) bool {
	return sourceRef.Source == "git" && sourceRef.Kind == "worktree" && ignoredBranch(sourceRef.Branch)
}

func ignoredBranch(branch string) bool {
	switch branch {
	case "", "main", "master", "develop", "dev":
		return true
	default:
		return false
	}
}

func matchesAnyString(left []string, right []string) bool {
	for _, l := range left {
		for _, r := range right {
			if l == r {
				return true
			}
		}
	}
	return false
}
