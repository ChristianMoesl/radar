package linking

import (
	"path/filepath"
	"regexp"
	"strings"
)

const MarkKeyPrefix = "mark:"

type MarkMatcher struct {
	pattern *regexp.Regexp
}

func NewMarkMatcher(prefixes []string) MarkMatcher {
	parts := make([]string, 0, len(prefixes))
	seen := map[string]bool{}
	for _, prefix := range prefixes {
		prefix = strings.ToUpper(strings.TrimSpace(prefix))
		if prefix == "" || seen[prefix] {
			continue
		}
		seen[prefix] = true
		parts = append(parts, regexp.QuoteMeta(prefix))
	}
	if len(parts) == 0 {
		return MarkMatcher{}
	}
	return MarkMatcher{pattern: regexp.MustCompile(`(?i)\b(?:` + strings.Join(parts, "|") + `)-[0-9]+\b`)}
}

func (m MarkMatcher) Values(values ...string) []string {
	if m.pattern == nil {
		return nil
	}
	marks := make([]string, 0)
	seen := map[string]bool{}
	for _, value := range values {
		for _, match := range m.pattern.FindAllString(value, -1) {
			mark := strings.ToUpper(match)
			if seen[mark] {
				continue
			}
			seen[mark] = true
			marks = append(marks, mark)
		}
	}
	return marks
}

func (m MarkMatcher) Keys(values ...string) []string {
	marks := m.Values(values...)
	keys := make([]string, 0, len(marks))
	for _, mark := range marks {
		keys = append(keys, MarkKey(mark))
	}
	return keys
}

func (m MarkMatcher) FirstValue(values ...string) string {
	marks := m.Values(values...)
	if len(marks) == 0 {
		return ""
	}
	return marks[0]
}

func MarkKey(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	return MarkKeyPrefix + value
}

func MarkValue(key string) (string, bool) {
	value, ok := strings.CutPrefix(strings.TrimSpace(key), MarkKeyPrefix)
	return value, ok && value != ""
}

func IsMarkKey(key string) bool {
	_, ok := MarkValue(key)
	return ok
}

func Keys(values ...string) []string {
	seen := map[string]bool{}
	keys := make([]string, 0)
	for _, value := range values {
		key := strings.TrimSpace(value)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys
}

func WorkspaceKey(path string) string {
	path = CleanPath(path)
	if path == "" {
		return ""
	}
	return "workspace:" + path
}

func WorkspaceGroupKey(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	return "workspace-group:" + id
}

func BranchKey(repoKey string, branchKey string) string {
	repoKey = strings.TrimSpace(repoKey)
	branchKey = strings.TrimSpace(branchKey)
	if repoKey == "" || branchKey == "" {
		return ""
	}
	return "branch:" + repoKey + ":" + branchKey
}

func CleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." {
		return path
	}
	return cleaned
}
