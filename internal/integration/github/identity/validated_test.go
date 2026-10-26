package identity

import "testing"

func TestCloneRemoteBoundary(t *testing.T) {
	for _, value := range []string{"https://github.com/Acme/App", "git@github.com:Acme/App.git"} {
		remote, repo, err := CloneRemote(value)
		if err != nil || repo != "acme/app" || remote == "" {
			t.Fatalf("%q: %q %q %v", value, remote, repo, err)
		}
	}
	for _, value := range []string{
		"acme/app", "http://github.com/acme/app", "https://evil.test/acme/app", "https://github.com:443/acme/app",
		"https://user:secret@github.com/acme/app", "https://github.com/acme/app?token=x", "https://github.com/acme/app#x",
		"https://github.com/acme/app/../other", "https://github.com/acme/app%2fother", "https://github.com/acme/app%3fx",
		"https://github.com/acme/..", "https://github.com/acme/.", "https://github.com/acme/app\n",
		"file:///tmp/app", "/tmp/app", "ext::sh -c evil", "--upload-pack=evil", "ssh://git@github.com/acme/app",
		"git@evil.test:acme/app.git", "git@github.com:acme/app.git;evil", "https://github.com/acme--org/app",
	} {
		t.Run(value, func(t *testing.T) {
			if remote, _, err := CloneRemote(value); err == nil {
				t.Fatalf("accepted %q as %q", value, remote)
			}
		})
	}
}

func TestValidatedPRIdentity(t *testing.T) {
	repo, n, err := ValidatedPullRequestID("github:pr:Acme/App:123")
	if err != nil || repo != "acme/app" || n != 123 {
		t.Fatalf("%s %d %v", repo, n, err)
	}
	for _, id := range []string{"github:pr:acme/app:0", "github:pr:acme/app:-1", "github:pr:acme/app:01", "github:pr:acme/app:1:changes", "github:pr:acme/app%2fother:1", "github:pr:acme/app?q=x:1", "github:pr:acme/../app:1", "other:pr:acme/app:1"} {
		if _, _, err := ValidatedPullRequestID(id); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
	if got := ResourceURL(repo, n, true); got != "https://github.com/acme/app/pull/123/files" {
		t.Fatal(got)
	}
}
