package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const testHead = "1111111111111111111111111111111111111111"
const testMerge = "2222222222222222222222222222222222222222"

type mergeProofRunner struct {
	published      bool
	proof          string
	origin         string
	mergeReachable bool
	fetchFailures  int
	apiFailures    int
	fetchCalls     int
	apiCalls       int
}

func (*mergeProofRunner) LookPath(string) error { return nil }
func (r *mergeProofRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	command := name + " " + strings.Join(args, " ")
	switch {
	case strings.HasPrefix(command, "git fetch "):
		r.fetchCalls++
		if r.fetchCalls <= r.fetchFailures {
			return "", errors.New("temporary fetch error")
		}
		return "", nil
	case strings.HasPrefix(command, "git show-ref "):
		return "", nil
	case strings.HasPrefix(command, "git for-each-ref "):
		if strings.Contains(command, "--contains="+testMerge) {
			if r.mergeReachable {
				return "refs/remotes/origin/main", nil
			}
			return "", nil
		}
		if r.published {
			return "refs/remotes/origin/feature", nil
		}
		return "", nil
	case command == "git remote get-url origin":
		if r.origin != "" {
			return r.origin, nil
		}
		return "git@github.com:acme/app.git", nil
	case strings.HasPrefix(command, "git rev-parse "):
		return testHead, nil
	case command == "gh api --hostname github.com --paginate repos/acme/app/commits/"+testHead+"/pulls":
		r.apiCalls++
		if r.apiCalls <= r.apiFailures {
			return "", errors.New("temporary API error")
		}
		return r.proof, nil
	default:
		return "", fmt.Errorf("unexpected command: %s", command)
	}
}

func mergedProof(head, repo, mergedAt string) string {
	return fmt.Sprintf(`[{"merged_at":%q,"merge_commit_sha":%q,"head":{"sha":%q},"base":{"repo":{"full_name":%q}}}]`, mergedAt, testMerge, head, repo)
}

func TestMergedBranchRequiresExactHeadAndReachableMerge(t *testing.T) {
	for _, tc := range []struct {
		name, proof              string
		reachable, want, wantErr bool
	}{
		{"squash or rebase merge", mergedProof(testHead, "acme/app", "2026-01-01"), true, true, false},
		{"commits after merge or reused branch name", mergedProof(strings.Repeat("3", 40), "acme/app", "2026-01-01"), true, false, false},
		{"closed unmerged", mergedProof(testHead, "acme/app", ""), true, false, false},
		{"different destination repository", mergedProof(testHead, "other/app", "2026-01-01"), true, false, false},
		{"merge no longer on origin", mergedProof(testHead, "acme/app", "2026-01-01"), false, false, false},
		{"no associated PR", "[]", true, false, false},
		{"later page", "[]\n" + mergedProof(testHead, "acme/app", "2026-01-01"), true, true, false},
		{"malformed response", "invalid", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &mergeProofRunner{proof: tc.proof, mergeReachable: tc.reachable}
			got, err := BranchPublishedOrMerged(context.Background(), r, "/repo", "feature")
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("safe=%v, err=%v", got, err)
			}
		})
	}
}

func TestPublishedBranchDoesNotNeedGitHub(t *testing.T) {
	r := &mergeProofRunner{published: true}
	got, err := BranchPublishedOrMerged(context.Background(), r, "/repo", "feature")
	if err != nil || !got || r.apiCalls != 0 {
		t.Fatalf("safe=%v err=%v calls=%d", got, err, r.apiCalls)
	}
}

func TestRemoteVerificationRetriesBeforeRequiringAttention(t *testing.T) {
	r := &mergeProofRunner{fetchFailures: 1, apiFailures: 1, proof: mergedProof(testHead, "acme/app", "2026-01-01"), mergeReachable: true}
	got, err := BranchPublishedOrMerged(context.Background(), r, "/repo", "feature")
	if err != nil || !got || r.fetchCalls != 2 || r.apiCalls != 2 {
		t.Fatalf("safe=%v err=%v calls=%d/%d", got, err, r.fetchCalls, r.apiCalls)
	}
	r = &mergeProofRunner{fetchFailures: 2}
	got, err = BranchPublishedOrMerged(context.Background(), r, "/repo", "feature")
	if err == nil || got || r.fetchCalls != 2 || r.apiCalls != 0 {
		t.Fatalf("persistent failure accepted: %v %v %+v", got, err, r)
	}
	r = &mergeProofRunner{apiFailures: 2}
	got, err = BranchPublishedOrMerged(context.Background(), r, "/repo", "feature")
	if err == nil || got || r.apiCalls != 2 {
		t.Fatalf("unverified merge accepted: %v %v %+v", got, err, r)
	}
}

func TestGitHubOriginParsingDoesNotQueryOtherHosts(t *testing.T) {
	for _, origin := range []string{"git@github.com:acme/app.git", "https://github.com/acme/app.git", "ssh://git@github.com/acme/app.git"} {
		if got := githubOriginRepository(origin); got != "acme/app" {
			t.Fatalf("%s => %s", origin, got)
		}
	}
	for _, origin := range []string{"https://other.example/acme/app.git", "git@other.example:acme/app.git", "/repo/remote.git", "https://github.com/invalid"} {
		r := &mergeProofRunner{origin: origin}
		got, err := BranchPublishedOrMerged(context.Background(), r, "/repo", "feature")
		if err != nil || got || r.apiCalls != 0 {
			t.Fatalf("unexpected GitHub request for %s: %v %v", origin, got, err)
		}
	}
}
