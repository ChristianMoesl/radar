package terraform

import (
	"context"
	"fmt"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
)

func (Source) CanSeedWorkspace(ref protocol.SourceRef) bool {
	return ref.Source == "terraform" && ref.Kind == "run" && ref.Presentation.WorkspaceName != "" && ref.Branch != "" && repositoryLocator(ref.Metadata["repository_url"]) != ""
}

func (s Source) PrepareWorkspaceSeed(ctx context.Context, ref protocol.SourceRef) (integration.WorkspaceSeed, error) {
	if !s.CanSeedWorkspace(ref) {
		return integration.WorkspaceSeed{}, fmt.Errorf("Terraform run has no verified source branch")
	}
	runner := workspace.ExecRunner{}
	repos, err := workspace.DiscoverRepos(ctx, runner, "")
	if err != nil {
		return integration.WorkspaceSeed{}, err
	}
	var matches []string
	for _, repo := range repos {
		remote, err := runner.Run(ctx, repo, "git", "remote", "get-url", "origin")
		if err == nil && repositoryLocator(strings.TrimSpace(remote)) == repositoryLocator(ref.Metadata["repository_url"]) {
			matches = append(matches, repo)
		}
	}
	if len(matches) != 1 {
		return integration.WorkspaceSeed{}, fmt.Errorf("expected one local repository for Terraform source; found %d", len(matches))
	}
	if strings.HasPrefix(ref.Branch, "-") || strings.ContainsAny(ref.Branch, "\r\n") {
		return integration.WorkspaceSeed{}, fmt.Errorf("invalid source branch")
	}
	if _, err := runner.Run(ctx, matches[0], "git", "check-ref-format", "--branch", ref.Branch); err != nil {
		return integration.WorkspaceSeed{}, fmt.Errorf("invalid source branch")
	}
	if err := workspace.FetchBranches(ctx, runner, matches[0]); err != nil {
		return integration.WorkspaceSeed{}, fmt.Errorf("refresh Terraform source branch: %w", err)
	}
	return integration.WorkspaceSeed{Repo: matches[0], Name: ref.Branch, Branch: ref.Branch, BranchMode: integration.WorkspaceBranchExisting}, nil
}
