package github

import (
	"context"
	"fmt"
	"radar/internal/integration"
	"radar/internal/integration/github/identity"
	"radar/internal/protocol"
	"strings"
)

func (Source) OpenResources(_ context.Context, ref protocol.SourceRef) ([]integration.OpenResource, error) {
	if ref.Source != "github" || ref.Kind != "pull_request" {
		return nil, nil
	}
	repo, number, err := identity.ValidatedPullRequestID(ref.ID)
	if err != nil {
		return nil, err
	}
	if ref.Repo != "" {
		actual, err := identity.ValidatedRepository(ref.Repo)
		if err != nil || actual != repo {
			return nil, fmt.Errorf("inconsistent GitHub PR repository identity")
		}
	}
	if ref.CanonicalKey != "" && !strings.EqualFold(ref.CanonicalKey, identity.PullRequestKey(repo, number)) {
		return nil, fmt.Errorf("inconsistent GitHub PR canonical identity")
	}
	base := fmt.Sprintf("github:open:pr:%s:%d:", repo, number)
	return []integration.OpenResource{
		{ID: base + "overview", Source: "github", SourceRefID: ref.ID, Label: fmt.Sprintf("%s PR #%d — Overview", repo, number), URL: identity.ResourceURL(repo, number, false)},
		{ID: base + "changes", Source: "github", SourceRefID: ref.ID, Label: fmt.Sprintf("%s PR #%d — Changes", repo, number), URL: identity.ResourceURL(repo, number, true)},
		{ID: "github:open:repository:" + repo, Source: "github", SourceRefID: ref.ID, Label: repo + " — Repository", URL: identity.ResourceURL(repo, 0, false)},
	}, nil
}

var _ integration.ResourceOpenProvider = Source{}
