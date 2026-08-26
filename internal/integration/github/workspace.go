package github

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/github/identity"
	"radar/internal/protocol"
	"radar/internal/integration/workspace"
)

func (Source) CanSeedWorkspace(ref protocol.SourceRef) bool {
	return ref.Source == "github" && ref.Kind == "pull_request"
}

func (Source) PrepareWorkspaceSeed(ctx context.Context, ref protocol.SourceRef) (integration.WorkspaceSeed, error) {
	repo, err := localRepoForPullRequest(ctx, workspace.ExecRunner{}, ref)
	if err != nil {
		return integration.WorkspaceSeed{}, err
	}
	name := strings.TrimSpace(ref.Presentation.WorkspaceName)
	if name == "" {
		name, err = fetchPullRequestHeadBranch(ctx, workspace.ExecRunner{}, ref)
		if err != nil {
			return integration.WorkspaceSeed{}, err
		}
	}
	if name == "" {
		return integration.WorkspaceSeed{}, fmt.Errorf("github pull request has no origin branch")
	}
	warning := ""
	if err := workspace.FetchBranches(ctx, workspace.ExecRunner{}, repo); err != nil {
		warning = fmt.Sprintf("origin refresh failed; used cached refs: %v", err)
	}
	_, number, ok := parsePullRequestRef(ref)
	if !ok {
		return integration.WorkspaceSeed{}, fmt.Errorf("github pull request has no number")
	}
	recoveryWarning, err := ensurePullRequestHeadBranch(ctx, workspace.ExecRunner{}, repo, name, fmt.Sprint(number))
	if err != nil {
		return integration.WorkspaceSeed{}, err
	}
	return integration.WorkspaceSeed{
		Repo: repo, Name: name, Branch: name, BranchMode: integration.WorkspaceBranchExisting,
		Warning: appendWarning(warning, recoveryWarning),
	}, nil
}

func ensurePullRequestHeadBranch(ctx context.Context, runner workspace.Runner, repo string, branch string, number string) (string, error) {
	branch = strings.TrimSpace(branch)
	number = strings.TrimSpace(number)
	if branch == "" || number == "" {
		return "", fmt.Errorf("github pull request has no origin branch or number")
	}
	if gitRefExists(ctx, runner, repo, "refs/heads/"+branch) {
		return "", nil
	}

	originRef := "refs/remotes/origin/" + branch
	originExists := gitRefExists(ctx, runner, repo, originRef)
	if _, err := runner.Run(ctx, repo, "git", "fetch", "--no-tags", "origin", "refs/pull/"+number+"/head"); err != nil {
		if originExists {
			return fmt.Sprintf("pull request head refresh failed; used origin branch: %v", err), nil
		}
		return "", fmt.Errorf("branch %q is unavailable on origin and pull request #%s could not be fetched: %w", branch, number, err)
	}
	pullCommit, err := runner.Run(ctx, repo, "git", "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve pull request #%s head: %w", number, err)
	}
	pullCommit = strings.TrimSpace(pullCommit)
	if originExists {
		originCommit, originErr := runner.Run(ctx, repo, "git", "rev-parse", "--verify", originRef+"^{commit}")
		if originErr == nil && strings.TrimSpace(originCommit) == pullCommit {
			return "", nil
		}
	}
	if _, err := runner.Run(ctx, repo, "git", "branch", "--", branch, pullCommit); err != nil {
		return "", fmt.Errorf("create local branch %q from pull request #%s: %w", branch, number, err)
	}
	return "", nil
}

func gitRefExists(ctx context.Context, runner workspace.Runner, repo string, ref string) bool {
	_, err := runner.Run(ctx, repo, "git", "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func localRepoForPullRequest(ctx context.Context, runner workspace.Runner, ref protocol.SourceRef) (string, error) {
	repos, err := workspace.DiscoverRepos(ctx, runner, "")
	if err != nil {
		return "", err
	}
	wantRepo, _, ok := parsePullRequestRef(ref)
	if !ok {
		return "", fmt.Errorf("github pull request has no repository")
	}
	matches := make([]string, 0)
	for _, repo := range repos {
		if localRepoMatchesGitHubRepo(ctx, runner, repo, wantRepo) {
			matches = append(matches, repo)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no local repository found for %s", wantRepo)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple local repositories found for %s", wantRepo)
	}
	return matches[0], nil
}

func localRepoMatchesGitHubRepo(ctx context.Context, runner workspace.Runner, repo string, wantRepo string) bool {
	remote, err := runner.Run(ctx, repo, "git", "remote", "get-url", "origin")
	if err == nil {
		return identity.Repository(remote) == identity.Repository(wantRepo)
	}
	return filepath.Base(repo) == filepath.Base(wantRepo)
}

func fetchPullRequestHeadBranch(ctx context.Context, runner workspace.Runner, ref protocol.SourceRef) (string, error) {
	if err := runner.LookPath("gh"); err != nil {
		return "", fmt.Errorf("github pull request branch lookup requires %q: %w", "gh", err)
	}
	repo, number, ok := parsePullRequestRef(ref)
	if !ok {
		return "", fmt.Errorf("github pull request has no repository or number")
	}
	branch, err := runner.Run(ctx, "", "gh", "pr", "view", fmt.Sprint(number), "--repo", repo, "--json", "headRefName", "--jq", ".headRefName")
	if err != nil {
		return "", err
	}
	return cleanBranch(branch), nil
}

func parsePullRequestRef(ref protocol.SourceRef) (string, int, bool) {
	if ref.Repo != "" {
		_, number, ok := parsePullRequestSourceRefID(ref.ID)
		return identity.Repository(ref.Repo), number, ok
	}
	return parsePullRequestSourceRefID(ref.ID)
}

func cleanBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "origin/")
	return strings.TrimPrefix(branch, "refs/heads/")
}

func appendWarning(current string, warning string) string {
	if strings.TrimSpace(current) == "" {
		return warning
	}
	if strings.TrimSpace(warning) == "" {
		return current
	}
	return current + "; " + warning
}

var _ integration.WorkspaceSeedProvider = Source{}
