package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/github"
	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestDevelopmentHeadBranchIdentityInCollectionAndBindings(t *testing.T) {
	for _, test := range []struct {
		name, branch, status string
		link                 bool
	}{
		{"matching", "ABC-7-feature", "OPEN", true},
		{"case insensitive", "feature/abc-7", "OPEN", true},
		{"repeated same mark", "ABC-7-feature-ABC-7", "OPEN", true},
		{"ticketless", "feature/work", "OPEN", true},
		{"unconfigured prefix", "XYZ-8-feature", "OPEN", true},
		{"different ticket", "ABC-8-feature", "OPEN", false},
		{"whole ticket boundary", "ABC-70-feature", "OPEN", false},
		{"ambiguous including issue", "ABC-7-ABC-8-feature", "OPEN", false},
		{"ambiguous other issues", "ABC-8-ABC-9-feature", "OPEN", false},
		{"closed matching", "ABC-7-feature", "DECLINED", true},
		{"merged matching", "ABC-7-feature", "MERGED", true},
		{"closed different ticket", "ABC-8-feature", "DECLINED", false},
	} {
		for _, mode := range []string{"collection", "bindings"} {
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				value := boundJiraFixture("In Progress", "indeterminate")
				value.Fields.IssueType.Name = "Task"
				value.ID = "10007"
				server := developmentIdentityServer(t, []issue{value}, test.branch, test.status)
				defer server.Close()
				configureJiraSource(t, server.URL, `{}`)
				t.Setenv("RADAR_JIRA_BASE_URL", server.URL)

				// A successful lookup must replace previously cached relationships,
				// including a conflicting PR that used to join this issue.
				previous := sourceRefFromIssue(Config{BaseURL: server.URL}, value)
				previous.LinkingKeys = []string{"mark:ABC-7", "github:pr:acme/app:42", linking.BranchKey("acme/app", linking.BranchName(test.branch))}
				req := boundJiraRequest()
				req.Previous = []protocol.Task{{SourceRefs: []protocol.SourceRef{previous}}}
				source := NewSource(github.NewSource())
				var result integration.CollectResult
				if mode == "bindings" {
					result = source.ResolveBindings(context.Background(), req)
				} else {
					result = source.Collect(context.Background(), integration.CollectRequest{Previous: req.Previous, Logger: req.Logger, LinkingMarks: req.LinkingMarks})
				}
				if !result.Complete || len(result.Observations) != 1 {
					t.Fatalf("result = %+v", result)
				}
				if result.SourceStatus != nil && result.SourceStatus.Status != "ok" {
					t.Fatalf("non-owning association must not fail collection: %+v", result.SourceStatus)
				}
				keys := result.Observations[0].Ref.LinkingKeys
				for _, key := range previous.LinkingKeys[1:] {
					if slices.Contains(keys, key) != test.link {
						t.Fatalf("keys = %v, want %q present = %v", keys, key, test.link)
					}
				}
				if !slices.Contains(keys, "mark:ABC-7") {
					t.Fatalf("issue lost its own identity: %v", keys)
				}
			})
		}
	}
}

func TestStackedDevelopmentPRKeepsWorkspaceSandboxBundlesSeparate(t *testing.T) {
	issues := []issue{jiraIssueWithType("ABC-123", "Task", "In Progress"), jiraIssueWithType("ABC-124", "Task", "In Progress")}
	issues[0].ID, issues[1].ID = "10123", "10124"
	server := developmentIdentityServer(t, issues, "ABC-124-feature", "OPEN")
	defer server.Close()
	configureJiraSource(t, server.URL, `{}`)
	t.Setenv("RADAR_JIRA_BASE_URL", server.URL)
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	req := integration.CollectRequest{Logger: jiraCollectRequest(nil).Logger, LinkingMarks: linking.NewMarkMatcher([]string{"ABC"})}
	store, err := state.NewStore(req.Logger)
	if err != nil {
		t.Fatal(err)
	}

	var localTasks []protocol.Task
	for _, value := range issues {
		path := "/workspaces/" + value.Key
		groupKey := linking.WorkspaceGroupKey(value.Key)
		workspaceKey := linking.WorkspaceKey(path)
		refs := []protocol.SourceRef{
			{ID: "workspace:" + value.Key, Source: "workspace", Kind: "workspace", Title: value.Key, Path: path, ProvidesWorkspace: true, WorkspaceID: value.Key, WorkspaceEntry: true, Lifecycle: protocol.SourceRefLifecycleWorkspace, CanonicalKey: workspaceKey, LinkingKeys: []string{workspaceKey, groupKey}},
			{ID: "git:worktree:" + path + "/app", Source: "git", Kind: "worktree", Path: path + "/app", WorkspaceID: value.Key, Lifecycle: protocol.SourceRefLifecycleWorkspace, LinkingKeys: []string{groupKey, linking.MarkKey(value.Key), linking.BranchKey("acme/app", value.Key+"-feature")}},
			{ID: "sbx:sandbox:" + value.Key, Source: "sbx", Kind: "sandbox", Path: path, Lifecycle: protocol.SourceRefLifecycleResource, CanonicalKey: workspaceKey, LinkingKeys: []string{workspaceKey, linking.MarkKey(value.Key)}},
		}
		for _, ref := range refs {
			ref.Role = protocol.SourceRefRoleAuthoritative
			ref.Authority = protocol.SourceRefAuthorityNone
			ref.EntityID = ref.ID
			ref.Signal = "in_progress"
			localTasks = append(localTasks, protocol.Task{Title: value.Key, Attention: "in_progress", SourceRefs: []protocol.SourceRef{ref}})
		}
	}
	store.SetTasks(localTasks)

	// Exercise repeated Jira-only refreshes against the durable local bundles.
	for range 2 {
		req.Previous = store.Tasks()
		result := NewSource(github.NewSource()).Collect(context.Background(), req)
		if !result.Complete || len(result.Observations) != 2 {
			t.Fatalf("collection = %+v", result)
		}
		var tasks []protocol.Task
		for _, observation := range result.Observations {
			tasks = append(tasks, protocol.Task{Title: observation.Ref.Title, Attention: string(observation.Signal), SourceRefs: []protocol.SourceRef{observation.Ref}})
		}
		store.SetTasksForSources(tasks, []string{"jira"})
		if got := store.Tasks(); len(got) != 2 {
			t.Fatalf("tasks = %+v, want two independent workspace bundles", got)
		}
		for _, task := range store.Tasks() {
			var workspace, sandbox, issueKey string
			for _, ref := range task.SourceRefs {
				switch ref.Source {
				case "workspace":
					workspace = ref.WorkspaceID
				case "sbx":
					sandbox = strings.TrimPrefix(ref.ID, "sbx:sandbox:")
				case "jira":
					issueKey = ref.Metadata["key"]
					if slices.Contains(ref.LinkingKeys, "github:pr:acme/app:42") != (issueKey == "ABC-124") {
						t.Fatalf("stacked PR joined the wrong issue: %+v", ref)
					}
				}
			}
			if workspace == "" || workspace != sandbox || workspace != issueKey {
				t.Fatalf("mixed workspace bundle: %+v", task)
			}
		}
	}
}

// Jira may return the same stacked PR in both issues' Development panels.
// The destination branch is deliberately unrelated to the owning head branch.
func developmentIdentityServer(t *testing.T, issues []issue, branch, status string) *httptest.Server {
	t.Helper()
	return jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/jql":
			_ = json.NewEncoder(w).Encode(searchResponse{Issues: issues})
		case "/rest/dev-status/1.0/issue/summary":
			_, _ = w.Write([]byte(`{"summary":{"pullrequest":{"byInstanceType":{"github-instance":{"count":1,"name":"GitHub"}}}}}`))
		case "/rest/dev-status/1.0/issue/detail":
			_ = json.NewEncoder(w).Encode(map[string]any{"detail": []any{map[string]any{"pullRequests": []any{map[string]any{
				"id": "#42", "url": "https://github.com/acme/app/pull/42", "repositoryName": "acme/app", "status": status,
				"source": map[string]string{"branch": branch}, "destination": map[string]string{"branch": "ABC-123-base"},
			}}}}})
		default:
			for _, value := range issues {
				if r.URL.Path == "/issue/"+value.Key {
					_ = json.NewEncoder(w).Encode(value)
					return
				}
			}
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
}
