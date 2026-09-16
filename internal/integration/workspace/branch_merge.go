package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// Retry remote verification once before requiring user attention. Never turn a
// failed check into proof of safety, and never retry destructive operations.
type verificationRetryRunner struct{ Runner }

func (r verificationRetryRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	output, err := r.Runner.Run(ctx, cwd, name, args...)
	remote := (name == "git" && len(args) > 0 && args[0] == "fetch") || (name == "gh" && len(args) > 0 && args[0] == "api")
	if err == nil || !remote || ctx.Err() != nil {
		return output, err
	}
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return r.Runner.Run(ctx, cwd, name, args...)
	}
}

func githubOriginRepository(origin string) string {
	origin = strings.TrimSpace(origin)
	if strings.HasPrefix(origin, "git@github.com:") {
		origin = "https://github.com/" + strings.TrimPrefix(origin, "git@github.com:")
	}
	u, err := url.Parse(origin)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return ""
	}
	repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return repo
}

// Squash/rebase merges do not preserve original commit ancestry. Accept them
// only when GitHub confirms a merged PR whose exact head is the current local
// tip AND whose merge commit is still reachable on origin. This rejects reused
// branch names, closed-but-unmerged PRs, and commits added after the merge.
func branchMergedOnGitHub(ctx context.Context, runner Runner, repository, ref string) (bool, error) {
	origin, err := runner.Run(ctx, repository, "git", "remote", "get-url", "origin")
	if err != nil {
		return false, err
	}
	repo := githubOriginRepository(origin)
	if repo == "" {
		return false, nil
	}
	head, err := runner.Run(ctx, repository, "git", "rev-parse", "--verify", ref)
	if err != nil {
		return false, err
	}
	head = strings.TrimSpace(head)
	if !gitObjectID(head) {
		return false, fmt.Errorf("invalid local branch tip")
	}
	endpoint := "repos/" + repo + "/commits/" + head + "/pulls"
	output, err := runner.Run(ctx, repository, "gh", "api", "--hostname", "github.com", "--paginate", endpoint)
	if err != nil {
		return false, fmt.Errorf("verify merged pull request: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	for {
		var pulls []struct {
			MergedAt       string `json:"merged_at"`
			MergeCommitSHA string `json:"merge_commit_sha"`
			Head           struct {
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"base"`
		}
		if err := decoder.Decode(&pulls); err == io.EOF {
			break
		} else if err != nil {
			return false, fmt.Errorf("decode merged pull request proof: %w", err)
		}
		for _, pr := range pulls {
			if pr.MergedAt == "" || pr.Head.SHA != head || pr.MergeCommitSHA == "" || !strings.EqualFold(pr.Base.Repo.FullName, repo) {
				continue
			}
			// Never use a remote response as a command option or arbitrary revision.
			if !gitObjectID(pr.MergeCommitSHA) {
				return false, fmt.Errorf("invalid merged commit ID")
			}
			refs, err := runner.Run(ctx, repository, "git", "for-each-ref", "--format=%(refname)", "--contains="+pr.MergeCommitSHA, "refs/remotes/origin")
			if err != nil {
				return false, err
			}
			if strings.TrimSpace(refs) != "" {
				return true, nil
			}
		}
	}
	return false, nil
}

func gitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
