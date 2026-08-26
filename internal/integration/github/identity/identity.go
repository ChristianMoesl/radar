package identity

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func Repository(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".git")
	value = strings.TrimPrefix(value, "https://github.com/")
	value = strings.TrimPrefix(value, "http://github.com/")
	value = strings.TrimPrefix(value, "git@github.com:")
	if strings.Contains(value, "://") || strings.Contains(value, "@") {
		return ""
	}
	return value
}

func Branch(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "refs/remotes/")
	value = strings.TrimPrefix(value, "origin/")
	value = strings.TrimPrefix(value, "refs/heads/")
	return strings.ReplaceAll(value, "/", "-")
}

func PullRequestKey(repository string, number int) string {
	repository = Repository(repository)
	if repository == "" || number <= 0 {
		return ""
	}
	return fmt.Sprintf("github:pr:%s:%d", repository, number)
}

func ParsePullRequestURL(value string) (string, int, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", 0, false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return "", 0, false
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", 0, false
	}
	repositoryName, err := url.PathUnescape(parts[1])
	if err != nil {
		return "", 0, false
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", 0, false
	}
	repository := Repository(owner + "/" + repositoryName)
	return repository, number, repository != ""
}
