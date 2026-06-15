package linker

import (
	"path/filepath"
	"strings"

	"radar.nvim/internal/protocol"
)

func mergeTasksByWorkKey(items []protocol.Task) []protocol.Task {
	merged := make([]protocol.Task, 0, len(items))
	byKey := map[string]int{}
	for _, item := range items {
		key := taskWorkKey(item)
		if key != "" {
			if existing, ok := byKey[key]; ok {
				merged[existing] = mergeTasks(merged[existing], item)
				continue
			}
			byKey[key] = len(merged)
		}
		merged = append(merged, item)
	}
	return merged
}

func taskWorkKey(item protocol.Task) string {
	if keys := ticketKeysForTask(item); len(keys) > 0 {
		return "ticket:" + keys[0]
	}
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Source == "git" && sourceRef.Kind == "worktree" && sourceRef.Path != "" {
			return "workspace:" + filepath.Clean(sourceRef.Path)
		}
	}
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Source == "github" && sourceRef.Kind == "pull_request" && sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Source == "jira" && sourceRef.Kind == "issue" && sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	if item.URL != "" {
		return "url:" + item.URL
	}
	return ""
}

func mergeTasks(left, right protocol.Task) protocol.Task {
	if attentionRank(right.Attention) > attentionRank(left.Attention) || left.Title == "" {
		left.Kind = right.Kind
		left.Title = right.Title
		left.Repo = right.Repo
		left.URL = right.URL
		left.Attention = right.Attention
		left.Reason = right.Reason
		left.DoneAt = right.DoneAt
		left.Metadata = right.Metadata
	}
	left.SourceRefs = mergeSourceRefs(left.SourceRefs, right.SourceRefs)
	return left
}

func mergeSourceRefs(left []protocol.SourceRef, right []protocol.SourceRef) []protocol.SourceRef {
	seen := map[string]bool{}
	merged := make([]protocol.SourceRef, 0, len(left)+len(right))
	for _, sourceRef := range append(left, right...) {
		if sourceRef.ID != "" && seen[sourceRef.ID] {
			continue
		}
		merged = append(merged, sourceRef)
		if sourceRef.ID != "" {
			seen[sourceRef.ID] = true
		}
	}
	return merged
}

func attentionRank(attention string) int {
	switch strings.ToLower(attention) {
	case "immediate":
		return 5
	case "attention":
		return 4
	case "in_progress":
		return 3
	case "done":
		return 2
	case "low_priority":
		return 1
	default:
		return 0
	}
}
