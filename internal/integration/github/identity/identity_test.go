package identity

import "testing"

func TestRepositoryNormalizesGitHubRemote(t *testing.T) {
	for _, value := range []string{
		"https://github.com/acme/app.git",
		"http://github.com/acme/app.git",
		"git@github.com:acme/app.git",
		"acme/app",
	} {
		if got := Repository(value); got != "acme/app" {
			t.Fatalf("Repository(%q) = %q, want acme/app", value, got)
		}
	}
}

func TestBranchNormalizesGitReferences(t *testing.T) {
	for _, value := range []string{"feature/work", "origin/feature/work", "refs/heads/feature/work", "refs/remotes/origin/feature/work"} {
		if got := Branch(value); got != "feature-work" {
			t.Fatalf("Branch(%q) = %q, want feature-work", value, got)
		}
	}
}

func TestParsePullRequestURL(t *testing.T) {
	repository, number, ok := ParsePullRequestURL("https://github.com/acme/app/pull/42")
	if !ok || repository != "acme/app" || number != 42 {
		t.Fatalf("ParsePullRequestURL() = %q, %d, %v", repository, number, ok)
	}
}

func TestParsePullRequestURLRejectsNonCanonicalURL(t *testing.T) {
	for _, value := range []string{
		"http://github.com/acme/app/pull/42",
		"https://example.test/acme/app/pull/42",
		"https://github.com/acme/app/issues/42",
		"https://github.com/acme/app/pull/42/files",
		"https://github.com/acme/app/pull/42?diff=split",
	} {
		if _, _, ok := ParsePullRequestURL(value); ok {
			t.Fatalf("ParsePullRequestURL(%q) succeeded", value)
		}
	}
}
