package linker

import (
	"regexp"
	"strings"

	"radar.nvim/internal/protocol"
)

var ticketPattern = regexp.MustCompile(`(?i)[A-Z][A-Z0-9]+-[0-9]+`)

type Input struct {
	Tasks      []protocol.Task
	SourceRefs []protocol.SourceRef
}

func Link(input Input) []protocol.Task {
	items := cloneTasks(input.Tasks)
	items = attachSourceRefs(items, input.SourceRefs)
	items = append(items, standaloneSourceRefs(items, input.SourceRefs)...)
	return items
}

func cloneTasks(items []protocol.Task) []protocol.Task {
	cloned := make([]protocol.Task, len(items))
	copy(cloned, items)
	return cloned
}

func attachSourceRefs(items []protocol.Task, source_refs []protocol.SourceRef) []protocol.Task {
	for i := range items {
		itemKeys := keysForTask(items[i])
		for _, sourceRef := range source_refs {
			if matchesAny(itemKeys, keysForSourceRef(sourceRef)) {
				items[i].SourceRefs = appendSourceRef(items[i].SourceRefs, sourceRef)
			}
		}
	}
	return items
}

func standaloneSourceRefs(items []protocol.Task, source_refs []protocol.SourceRef) []protocol.Task {
	attached := map[string]bool{}
	for _, item := range items {
		for _, sourceRef := range item.SourceRefs {
			attached[sourceRef.ID] = true
		}
	}

	standalone := make([]protocol.Task, 0)
	for _, sourceRef := range source_refs {
		if attached[sourceRef.ID] || ignoredSourceRef(sourceRef) {
			continue
		}
		standalone = append(standalone, taskFromSourceRef(sourceRef))
	}
	return standalone
}

func taskFromSourceRef(sourceRef protocol.SourceRef) protocol.Task {
	reason := sourceRef.Source + " " + sourceRef.Kind
	return protocol.Task{
		Kind:       sourceRef.Source + "_" + sourceRef.Kind,
		Title:      sourceRef.Title,
		Repo:       sourceRef.Repo,
		URL:        sourceRef.URL,
		Attention:  "in_progress",
		Reason:     reason,
		SourceRefs: []protocol.SourceRef{sourceRef},
	}
}

func appendSourceRef(source_refs []protocol.SourceRef, sourceRef protocol.SourceRef) []protocol.SourceRef {
	for _, existing := range source_refs {
		if existing.ID == sourceRef.ID {
			return source_refs
		}
	}
	return append(source_refs, sourceRef)
}

func keysForTask(task protocol.Task) []string {
	values := []string{task.Title, task.Repo, task.URL}
	for _, sourceRef := range task.SourceRefs {
		values = append(values, valuesForSourceRef(sourceRef)...)
	}
	return extractKeys(values...)
}

func keysForSourceRef(sourceRef protocol.SourceRef) []string {
	return extractKeys(valuesForSourceRef(sourceRef)...)
}

func valuesForSourceRef(sourceRef protocol.SourceRef) []string {
	values := []string{sourceRef.ID, sourceRef.Title, sourceRef.Branch, sourceRef.Path, sourceRef.Repo, sourceRef.URL}
	for _, value := range sourceRef.Metadata {
		values = append(values, value)
	}
	return values
}

func extractKeys(values ...string) []string {
	keys := make([]string, 0)
	seen := map[string]bool{}
	for _, value := range values {
		for _, match := range ticketPattern.FindAllString(value, -1) {
			key := strings.ToUpper(match)
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func matchesAny(left []string, right []string) bool {
	for _, l := range left {
		for _, r := range right {
			if l == r {
				return true
			}
		}
	}
	return false
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
