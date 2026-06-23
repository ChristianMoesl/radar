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

func BranchKey(repo string, branch string) string {
	repo = NormalizeRepo(repo)
	branch = NormalizeBranch(branch)
	if repo == "" || branch == "" {
		return ""
	}
	return "branch:" + repo + ":" + branch
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

func NormalizeBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "origin/")
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return strings.ReplaceAll(branch, "/", "-")
}

func NormalizeRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "http://github.com/")
	repo = strings.TrimPrefix(repo, "git@github.com:")
	if strings.Contains(repo, "://") || strings.Contains(repo, "@") {
		return ""
	}
	return repo
}
