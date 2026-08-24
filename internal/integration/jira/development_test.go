package jira

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"radar/internal/protocol"
)

func TestFetchIssueDevelopmentLinksDiscoversGitHubApplicationType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != basicAuth("me@example.com", "token") {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/dev-status/1.0/issue/summary":
			if r.URL.Query().Get("issueId") != "10001" {
				t.Fatalf("summary query = %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"errors":[],"summary":{"pullrequest":{"byInstanceType":{"opaque-github-type":{"count":1,"name":"GitHub"},"gitlab-type":{"count":1,"name":"GitLab"}}}}}`))
		case "/rest/dev-status/1.0/issue/detail":
			if r.URL.Query().Get("applicationType") != "opaque-github-type" || r.URL.Query().Get("dataType") != "pullrequest" {
				t.Fatalf("detail query = %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"errors":[],"detail":[{"pullRequests":[{"id":"#42","url":"https://github.com/acme/app/pull/42","repositoryName":"acme/app","source":{"branch":"feature/work"}}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := fetchIssueDevelopmentLinks(context.Background(), Config{BaseURL: server.URL, Email: "me@example.com", APIToken: "token"}, "10001")
	if result.Err != nil || result.Invalid != 0 {
		t.Fatalf("result = %+v", result)
	}
	for _, key := range []string{"github:pr:acme/app:42", "branch:acme/app:feature-work"} {
		if !slices.Contains(result.LinkingKeys, key) {
			t.Fatalf("linking keys = %+v, want %q", result.LinkingKeys, key)
		}
	}
}

func TestDevelopmentPullRequestLinkingKeysRejectsContradictoryIdentity(t *testing.T) {
	pullRequest := developmentPullRequest{ID: "#42", URL: "https://github.com/acme/app/pull/42", RepositoryName: "acme/other"}
	pullRequest.Source.Branch = "feature/work"
	if _, _, err := developmentPullRequestLinkingKeys(pullRequest); err == nil {
		t.Fatal("developmentPullRequestLinkingKeys() succeeded")
	}
}

func TestCollectDevelopmentLinksPreservesPreviousKeysOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary failure", http.StatusBadGateway)
	}))
	defer server.Close()

	value := jiraIssueWithType("XYZ-7", "Task", "In Progress")
	value.ID = "10007"
	previous := []protocol.Task{{SourceRefs: []protocol.SourceRef{{
		ID: "jira:issue:XYZ-7", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative,
		LinkingKeys: []string{"mark:XYZ-7", "github:pr:acme/app:7", "branch:acme/app:feature-work"}, Metadata: map[string]string{"key": "XYZ-7"},
	}}}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	result := collectDevelopmentLinks(context.Background(), Config{BaseURL: server.URL}, []issue{value}, previous, logger)
	if result.Failed != 1 {
		t.Fatalf("result = %+v", result)
	}
	for _, key := range []string{"github:pr:acme/app:7", "branch:acme/app:feature-work"} {
		if !slices.Contains(result.LinkingKeysByIssue["XYZ-7"], key) {
			t.Fatalf("linking keys = %+v, want %q", result.LinkingKeysByIssue["XYZ-7"], key)
		}
	}
}
