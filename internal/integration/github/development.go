package github

import (
	"fmt"
	"strconv"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/github/identity"
	"radar/internal/linking"
)

func (Source) ResolveDevelopmentLink(link integration.DevelopmentLink) (integration.DevelopmentLinkResolution, bool, error) {
	repositoryFromURL, numberFromURL, ok := identity.ParsePullRequestURL(link.URL)
	if !ok {
		return integration.DevelopmentLinkResolution{}, false, nil
	}
	repository := identity.Repository(link.Repository)
	if repository == "" || !strings.EqualFold(repository, repositoryFromURL) {
		return integration.DevelopmentLinkResolution{}, true, fmt.Errorf("GitHub pull request repository does not match URL")
	}
	number, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(link.ExternalID), "#"))
	if err != nil || number <= 0 || number != numberFromURL {
		return integration.DevelopmentLinkResolution{}, true, fmt.Errorf("GitHub pull request number does not match URL")
	}
	branch := identity.Branch(link.Branch)
	if branch == "" {
		return integration.DevelopmentLinkResolution{}, true, fmt.Errorf("GitHub pull request source branch is empty")
	}
	entityKey := identity.PullRequestKey(repository, number)
	return integration.DevelopmentLinkResolution{
		EntityKey:   entityKey,
		LinkingKeys: linking.Keys(entityKey, linking.BranchKey(repository, branch)),
	}, true, nil
}
