package linking

import (
	"path/filepath"
	"regexp"
	"strings"
)

var ticketPattern = regexp.MustCompile(`(?i)[A-Z][A-Z0-9]+-[0-9]+`)

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

func TicketKeys(values ...string) []string {
	keys := make([]string, 0)
	seen := map[string]bool{}
	for _, value := range values {
		for _, match := range ticketPattern.FindAllString(value, -1) {
			key := "ticket:" + strings.ToUpper(match)
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
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
