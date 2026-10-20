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
	repo, number, ok := identity.ParsePullRequestURL(link.URL)
	if !ok {
		return integration.DevelopmentLinkResolution{}, false, nil
	}
	n, err := strconv.Atoi(link.ExternalID)
	branch := identity.Branch(link.Branch)
	if err != nil || n != number || !strings.EqualFold(repo, identity.Repository(link.Repository)) || branch == "" {
		return integration.DevelopmentLinkResolution{}, true, fmt.Errorf("pull request relationship has conflicting identity")
	}
	key := identity.PullRequestKey(repo, number)
	return integration.DevelopmentLinkResolution{EntityKey: key, LinkingKeys: linking.Keys(key, linking.BranchKey(repo, branch))}, true, nil
}
