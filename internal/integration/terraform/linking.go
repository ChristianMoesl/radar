package terraform

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func (s Source) link(ref *protocol.SourceRef, message string, git ingressAttributes, marks linking.MarkMatcher) error {
	branchMarks := marks.Keys(git.Branch)
	switch len(branchMarks) {
	case 0:
		ref.LinkingKeys = marks.Keys(message)
	case 1:
		ref.LinkingKeys = branchMarks
	default:
		return nil // Conflicting branches require an explicit authored binding.
	}
	locator := repositoryLocator(git.CloneURL)
	if locator != "" {
		u, _ := url.Parse(locator)
		if git.IsPR {
			prURL, err := url.Parse(git.PRURL)
			if err != nil || !strings.EqualFold(u.Host, prURL.Host) || strings.Trim(u.Path, "/") != git.Repository {
				return fmt.Errorf("Terraform repository and PR relationship conflict")
			}
		}
		if strings.Trim(u.Path, "/") == git.Repository {
			ref.Repo, ref.Branch = git.Repository, git.Branch
			ref.Metadata["repository_url"] = locator
			if git.DefaultBranch != nil && !*git.DefaultBranch && git.Branch != "" {
				// Existing GitHub branch keys use owner/repository. Other VCS
				// hosts must not collide with that provider's repository namespace.
				if strings.EqualFold(u.Host, "github.com") {
					ref.LinkingKeys = linking.Keys(append(ref.LinkingKeys, linking.BranchKey(git.Repository, linking.BranchName(git.Branch)))...)
				}
				ref.Presentation.WorkspaceName = git.Branch
			}
		}
	}
	if !git.IsPR || git.PRNumber <= 0 || git.Branch == "" {
		return nil
	}
	for _, resolver := range s.resolvers {
		resolved, handled, err := resolver.ResolveDevelopmentLink(integration.DevelopmentLink{URL: git.PRURL, Repository: git.Repository, ExternalID: strconv.Itoa(git.PRNumber), Branch: git.Branch})
		if err != nil {
			return fmt.Errorf("Terraform PR relationship is inconsistent")
		}
		if !handled {
			continue
		}
		ref.LinkingKeys = linking.Keys(append(ref.LinkingKeys, resolved.EntityKey)...)
		// Shared/default branches do not supply generic association keys.
		if git.DefaultBranch != nil && !*git.DefaultBranch {
			ref.LinkingKeys = linking.Keys(append(ref.LinkingKeys, resolved.LinkingKeys...)...)
		}
		break
	}
	return nil
}

func repositoryLocator(value string) string {
	if strings.HasPrefix(value, "git@") {
		parts := strings.SplitN(strings.TrimPrefix(value, "git@"), ":", 2)
		if len(parts) == 2 {
			value = "https://" + parts[0] + "/" + parts[1]
		}
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "ssh") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || strings.Contains(u.Path, "..") {
		return ""
	}
	if u.User != nil && (u.Scheme != "ssh" || u.User.Username() != "git") {
		return ""
	}
	u.Scheme, u.User, u.Host = "https", nil, strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git")
	return u.String()
}
