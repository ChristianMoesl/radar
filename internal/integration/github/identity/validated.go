package identity

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

// ValidatedRepository is deliberately stricter than Repository, which is a
// historical display normalizer and must not authorize privileged operations.
func ValidatedRepository(value string) (string, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !ownerPattern.MatchString(parts[0]) || !repoPattern.MatchString(parts[1]) ||
		strings.Contains(parts[0], "--") || parts[1] == "." || parts[1] == ".." || strings.HasSuffix(parts[1], ".git") {
		return "", fmt.Errorf("invalid GitHub repository identity")
	}
	return strings.ToLower(value), nil
}

// CloneRemote accepts exactly two GitHub.com transports; no remote helpers,
// credentials, local paths, URL parameters, ports, or caller-supplied Git flags.
func CloneRemote(value string) (remote, repository string, err error) {
	const https = "https://github.com/"
	const ssh = "git@github.com:"
	var raw, prefix string
	switch {
	case strings.HasPrefix(value, https):
		raw, prefix = strings.TrimPrefix(value, https), https
	case strings.HasPrefix(value, ssh):
		raw, prefix = strings.TrimPrefix(value, ssh), ssh
	default:
		return "", "", fmt.Errorf("use a GitHub HTTPS URL or git@github.com:owner/repo.git")
	}
	raw = strings.TrimSuffix(raw, ".git")
	repository, err = ValidatedRepository(raw)
	if err != nil {
		return "", "", err
	}
	return prefix + repository + ".git", repository, nil
}

func ValidatedPullRequestID(value string) (string, int, error) {
	if !strings.HasPrefix(value, "github:pr:") {
		return "", 0, fmt.Errorf("invalid GitHub PR identity")
	}
	parts := strings.Split(strings.TrimPrefix(value, "github:pr:"), ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid GitHub PR identity")
	}
	repo, err := ValidatedRepository(parts[0])
	if err != nil {
		return "", 0, err
	}
	number, err := strconv.Atoi(parts[1])
	if err != nil || number <= 0 || strconv.Itoa(number) != parts[1] {
		return "", 0, fmt.Errorf("invalid GitHub PR number")
	}
	return repo, number, nil
}

func ResourceURL(repository string, number int, changes bool) string {
	path := "/" + repository
	if number > 0 {
		path += "/pull/" + strconv.Itoa(number)
		if changes {
			path += "/files"
		}
	}
	return (&url.URL{Scheme: "https", Host: "github.com", Path: path}).String()
}
