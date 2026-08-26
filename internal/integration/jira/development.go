package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

const developmentLookupConcurrency = 6

const developmentPullRequestDataType = "pullrequest"

type developmentInstance struct {
	Count int    `json:"count"`
	Name  string `json:"name"`
}

type developmentSummaryResponse struct {
	Errors  []json.RawMessage `json:"errors"`
	Summary struct {
		PullRequests struct {
			ByInstanceType map[string]developmentInstance `json:"byInstanceType"`
		} `json:"pullrequest"`
	} `json:"summary"`
}

type developmentPullRequest struct {
	ID             string `json:"id"`
	URL            string `json:"url"`
	RepositoryName string `json:"repositoryName"`
	Source         struct {
		Branch string `json:"branch"`
	} `json:"source"`
}

type developmentDetailResponse struct {
	Errors []json.RawMessage `json:"errors"`
	Detail []struct {
		PullRequests []developmentPullRequest `json:"pullRequests"`
	} `json:"detail"`
}

type developmentHTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e developmentHTTPError) Error() string {
	return fmt.Sprintf("jira development fetch failed: %s: %s", e.Status, e.Body)
}

type developmentIssueResult struct {
	IssueKey        string
	LinkingKeys     []string
	PullRequestKeys []string
	Invalid         int
	Unavailable     bool
	Err             error
}

type developmentCollection struct {
	LinkingKeysByIssue map[string][]string
	PullRequests       int
	Invalid            int
	Failed             int
	Unavailable        int
}

func collectDevelopmentLinks(ctx context.Context, cfg Config, issues []issue, previous []protocol.Task, logger *slog.Logger, resolvers []integration.DevelopmentLinkResolver) developmentCollection {
	collection := developmentCollection{LinkingKeysByIssue: map[string][]string{}}
	if len(issues) == 0 {
		return collection
	}

	previousKeys := previousDevelopmentLinkKeys(previous)
	results := make(chan developmentIssueResult, len(issues))
	semaphore := make(chan struct{}, developmentLookupConcurrency)
	var wg sync.WaitGroup
	for _, value := range issues {
		value := value
		key := normalizeIssueKey(value.Key)
		if key == "" || strings.TrimSpace(value.ID) == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- developmentIssueResult{IssueKey: key, Err: ctx.Err()}
				return
			}
			result := fetchIssueDevelopmentLinks(ctx, cfg, value.ID, resolvers)
			result.IssueKey = key
			results <- result
		}()
	}
	wg.Wait()
	close(results)

	seenPullRequests := map[string]bool{}
	for result := range results {
		keys := result.LinkingKeys
		if result.Err != nil || result.Unavailable {
			keys = linking.Keys(append(keys, previousKeys[result.IssueKey]...)...)
		}
		collection.LinkingKeysByIssue[result.IssueKey] = keys
		collection.Invalid += result.Invalid
		if result.Unavailable {
			collection.Unavailable++
			logger.Debug("jira development links unavailable", "issue", result.IssueKey, "error", result.Err)
			continue
		}
		if result.Err != nil {
			collection.Failed++
			logger.Warn("jira development link collection failed", "issue", result.IssueKey, "error", result.Err)
		}
		for _, key := range result.PullRequestKeys {
			seenPullRequests[key] = true
		}
	}
	collection.PullRequests = len(seenPullRequests)
	return collection
}

func fetchIssueDevelopmentLinks(ctx context.Context, cfg Config, issueID string, resolvers []integration.DevelopmentLinkResolver) developmentIssueResult {
	var summary developmentSummaryResponse
	err := getDevelopmentJSON(ctx, cfg, "/rest/dev-status/1.0/issue/summary", url.Values{"issueId": []string{issueID}}, &summary)
	if err != nil {
		return developmentIssueResult{Unavailable: developmentUnavailable(err), Err: err}
	}
	if len(summary.Errors) > 0 {
		return developmentIssueResult{Err: fmt.Errorf("jira development summary returned %d error(s)", len(summary.Errors))}
	}

	applicationTypes := make([]string, 0)
	for applicationType, instance := range summary.Summary.PullRequests.ByInstanceType {
		if instance.Count > 0 && strings.EqualFold(strings.TrimSpace(instance.Name), "GitHub") {
			applicationTypes = append(applicationTypes, applicationType)
		}
	}
	sort.Strings(applicationTypes)

	result := developmentIssueResult{}
	for _, applicationType := range applicationTypes {
		var detail developmentDetailResponse
		err := getDevelopmentJSON(ctx, cfg, "/rest/dev-status/1.0/issue/detail", url.Values{
			"issueId":         []string{issueID},
			"applicationType": []string{applicationType},
			"dataType":        []string{developmentPullRequestDataType},
		}, &detail)
		if err != nil {
			result.Unavailable = developmentUnavailable(err)
			result.Err = err
			continue
		}
		if len(detail.Errors) > 0 {
			result.Err = fmt.Errorf("jira development detail returned %d error(s)", len(detail.Errors))
			continue
		}
		for _, group := range detail.Detail {
			for _, pullRequest := range group.PullRequests {
				resolution, err := resolveDevelopmentPullRequest(pullRequest, resolvers)
				if err != nil {
					result.Invalid++
					continue
				}
				result.PullRequestKeys = linking.Keys(append(result.PullRequestKeys, resolution.EntityKey)...)
				result.LinkingKeys = linking.Keys(append(result.LinkingKeys, resolution.LinkingKeys...)...)
			}
		}
	}
	return result
}

func resolveDevelopmentPullRequest(pullRequest developmentPullRequest, resolvers []integration.DevelopmentLinkResolver) (integration.DevelopmentLinkResolution, error) {
	link := integration.DevelopmentLink{
		URL: pullRequest.URL, Repository: pullRequest.RepositoryName,
		ExternalID: pullRequest.ID, Branch: pullRequest.Source.Branch,
	}
	for _, resolver := range resolvers {
		resolution, handled, err := resolver.ResolveDevelopmentLink(link)
		if handled {
			return resolution, err
		}
	}
	return integration.DevelopmentLinkResolution{}, fmt.Errorf("development pull request provider is not registered")
}

func getDevelopmentJSON(ctx context.Context, cfg Config, path string, query url.Values, target any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	requestURL := strings.TrimRight(cfg.BaseURL, "/") + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(cfg.Email, cfg.APIToken)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return developmentHTTPError{StatusCode: response.StatusCode, Status: response.Status, Body: previewResponse(data)}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("jira development response decode failed: %w: %s", err, previewResponse(data))
	}
	return nil
}

func developmentUnavailable(err error) bool {
	value, ok := err.(developmentHTTPError)
	return ok && (value.StatusCode == http.StatusForbidden || value.StatusCode == http.StatusNotFound)
}

func previousDevelopmentLinkKeys(tasks []protocol.Task) map[string][]string {
	keysByIssue := map[string][]string{}
	for _, task := range tasks {
		for _, ref := range task.SourceRefs {
			if ref.Source != "jira" || ref.Kind != "issue" || ref.Role != protocol.SourceRefRoleAuthoritative {
				continue
			}
			issueKey := normalizeIssueKey(ref.Metadata["key"])
			if issueKey == "" {
				issueKey, _ = strings.CutPrefix(ref.ID, "jira:issue:")
				issueKey = normalizeIssueKey(issueKey)
			}
			for _, key := range ref.LinkingKeys {
				if strings.HasPrefix(key, "github:pr:") || strings.HasPrefix(key, "branch:") {
					keysByIssue[issueKey] = linking.Keys(append(keysByIssue[issueKey], key)...)
				}
			}
		}
	}
	return keysByIssue
}
