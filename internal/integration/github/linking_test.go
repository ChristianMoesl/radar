package github

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func referenceTask(branch string) protocol.Task {
	ref := githubPullRequestRef("github:pr:acme/app:7", "acme/app", 7, "Fix ABC-1", "https://github.com/acme/app/pull/7", "open PR", branch)
	ref.Metadata = map[string]string{"head_oid": "head7", "body": "https://jira.example.test/browse/ABC-2", "commit_marks": "ABC-3\nABC-1"}
	return protocol.Task{Attention: "in_progress", SourceRefs: []protocol.SourceRef{ref}}
}

func TestPullRequestReferenceOwnership(t *testing.T) {
	for _, test := range []struct {
		branch string
		want   []string
	}{
		{"feature", []string{"mark:ABC-1", "mark:ABC-2", "mark:ABC-3"}},
		{"abc-4-feature", []string{"mark:ABC-4"}},
		{"ABC-4-ABC-4", []string{"mark:ABC-4"}},
		{"ABC-4-ABC-5", nil},
		{"ABC-40-work", []string{"mark:ABC-40"}},
		{"XYZ-4-work", []string{"mark:ABC-1", "mark:ABC-2", "mark:ABC-3"}},
	} {
		t.Run(test.branch, func(t *testing.T) {
			tasks := []protocol.Task{referenceTask(test.branch)}
			tasks[0].SourceRefs[0].LinkingKeys = append(tasks[0].SourceRefs[0].LinkingKeys, "mark:ABC-99")
			applyLinkingMarks(tasks, linking.NewMarkMatcher([]string{"ABC"}))
			ref := tasks[0].SourceRefs[0]
			var got []string
			for _, key := range ref.LinkingKeys {
				if linking.IsMarkKey(key) {
					got = append(got, key)
				}
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("keys = %v, want %v", got, test.want)
			}
			if !slices.Contains(ref.LinkingKeys, ref.ID) {
				t.Fatal("lost PR identity")
			}
			if test.want == nil && slices.ContainsFunc(ref.LinkingKeys, func(key string) bool { return strings.HasPrefix(key, "branch:") }) {
				t.Fatal("ambiguous PR retained automatic branch join")
			}
		})
	}
}

func commitPage(total int, next bool, cursor string, commits ...string) map[string]any {
	nodes := make([]any, 0, len(commits))
	for i, message := range commits {
		nodes = append(nodes, map[string]any{"commit": map[string]any{"oid": fmt.Sprint(i, message), "message": message}})
	}
	return map[string]any{"number": 7, "url": "https://github.com/acme/app/pull/7", "headRefName": "feature", "headRefOid": "head7", "commits": map[string]any{"totalCount": total, "nodes": nodes, "pageInfo": map[string]any{"hasNextPage": next, "endCursor": cursor}}}
}

func fakeCommitPages(t *testing.T, first, second map[string]any) {
	t.Helper()
	resetRateStateForTest(t)
	encode := func(pr map[string]any) string {
		data, err := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pr}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	installFakeGH(t, `#!/bin/sh
case "$*" in
  "api rate_limit") echo '{"resources":{"graphql":{"limit":5000,"remaining":5000,"reset":4102444800}}}' ;;
  *"commits(first: 100"*"cursor=next"*) cat <<'JSON'
`+encode(second)+`
JSON
    ;;
  *"commits(first: 100"*) cat <<'JSON'
`+encode(first)+`
JSON
    ;;
  *) echo 'unexpected request' >&2; exit 1 ;;
esac
`)
}

func TestCommitReferencesUseEveryPageAndDeduplicate(t *testing.T) {
	fakeCommitPages(t, commitPage(3, true, "next", "Fix ABC-3", "ABC-3 again"), commitPage(3, false, "", "Also https://jira.example.test/browse/ABC-4"))
	tasks := []protocol.Task{referenceTask("feature")}
	if err := collectLinkingMarks(context.Background(), tasks, nil, linking.NewMarkMatcher([]string{"ABC"}), testLogger()); err != nil {
		t.Fatal(err)
	}
	ref := tasks[0].SourceRefs[0]
	for _, key := range []string{"mark:ABC-1", "mark:ABC-2", "mark:ABC-3", "mark:ABC-4"} {
		if !slices.Contains(ref.LinkingKeys, key) {
			t.Fatalf("missing %s in %v", key, ref.LinkingKeys)
		}
	}
}

func TestIncompleteCommitReferencesRetainPreviousLinks(t *testing.T) {
	for _, name := range []string{"truncated", "missing cursor", "repeated cursor", "duplicate commit", "changed head", "changed count", "wrong PR", "wrong repository", "missing page info", "missing total", "missing nodes", "missing message", "permission denied"} {
		t.Run(name, func(t *testing.T) {
			first, second := commitPage(2, true, "next", "ABC-3"), commitPage(2, false, "", "ABC-4")
			connection := first["commits"].(map[string]any)
			switch name {
			case "truncated":
				connection["pageInfo"] = map[string]any{"hasNextPage": false}
			case "missing cursor":
				connection["pageInfo"] = map[string]any{"hasNextPage": true}
			case "repeated cursor":
				second = commitPage(3, true, "next", "ABC-4")
				connection["totalCount"] = 3
			case "duplicate commit":
				second = commitPage(2, false, "", "ABC-3")
			case "changed head":
				second["headRefOid"] = "changed"
			case "changed count":
				second["commits"].(map[string]any)["totalCount"] = 3
			case "wrong PR":
				second["number"] = 8
			case "wrong repository":
				second["url"] = "https://github.com/other/app/pull/7"
			case "missing page info":
				delete(connection, "pageInfo")
			case "missing total":
				delete(connection, "totalCount")
			case "missing nodes":
				delete(connection, "nodes")
			case "missing message":
				connection["nodes"] = []any{map[string]any{"commit": map[string]any{"oid": "one"}}}
			case "permission denied":
				first = nil
			}
			fakeCommitPages(t, first, second)
			tasks, previous := []protocol.Task{referenceTask("feature")}, []protocol.Task{referenceTask("feature")}
			previous[0].SourceRefs[0].LinkingKeys = append(previous[0].SourceRefs[0].LinkingKeys, "mark:ABC-9")
			err := collectLinkingMarks(context.Background(), tasks, previous, linking.NewMarkMatcher([]string{"ABC"}), testLogger())
			if err == nil {
				t.Fatal("accepted incomplete commit facts")
			}
			keys := tasks[0].SourceRefs[0].LinkingKeys
			if !slices.Contains(keys, "mark:ABC-9") || slices.Contains(keys, "mark:ABC-1") || slices.Contains(keys, "mark:ABC-3") {
				t.Fatalf("partial references replaced previous links: %v", keys)
			}
		})
	}
}

func TestBranchOwnedAndDisabledReferencesNeedNoCommitRequests(t *testing.T) {
	installFakeGH(t, "#!/bin/sh\nexit 1\n")
	for _, branch := range []string{"ABC-4-work", "ABC-4-ABC-5"} {
		if err := collectLinkingMarks(context.Background(), []protocol.Task{referenceTask(branch)}, nil, linking.NewMarkMatcher([]string{"ABC"}), testLogger()); err != nil {
			t.Fatal(err)
		}
	}
	if err := collectLinkingMarks(context.Background(), []protocol.Task{referenceTask("feature")}, nil, linking.MarkMatcher{}, testLogger()); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionReportsIncompleteLinkingWithoutDroppingPriorFacts(t *testing.T) {
	trackingBudget(t)
	configureBoundGitHub(t, `{}`)
	pr := trackedFixture(7)
	pr.HeadRefName, pr.HeadRefOID, pr.Title, pr.Body = "feature", "head7", "ABC-1 new title", "ABC-2 new description"
	data, err := json.Marshal(map[string]any{"data": map[string]any{
		"viewer":          map[string]string{"login": "me"},
		"reviewRequested": map[string]any{"nodes": []any{}, "pageInfo": map[string]bool{"hasNextPage": false}},
		"authored":        map[string]any{"nodes": []searchPullRequest{pr}},
		"participated":    map[string]any{"nodes": []any{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	installFakeGH(t, `#!/bin/sh
case "$*" in
  *"commits(first: 100"*) echo 'Forbidden' >&2; exit 1 ;;
  *reviewQuery*) cat <<'JSON'
`+string(data)+`
JSON
    ;;
  *) exit 2 ;;
esac
`)
	previous := referenceTask("feature")
	previous.SourceRefs[0].LinkingKeys = []string{previous.SourceRefs[0].ID, "mark:ABC-9"}
	result := NewSource().Collect(context.Background(), integration.CollectRequest{
		Previous: []protocol.Task{previous}, LinkingMarks: linking.NewMarkMatcher([]string{"ABC"}), Logger: testLogger(),
	})
	if result.Complete || result.SourceStatus == nil || !strings.Contains(result.SourceStatus.Detail, "linking incomplete") || len(result.Observations) != 1 {
		t.Fatalf("link failure was not reported: %+v", result)
	}
	keys := result.Observations[0].Ref.LinkingKeys
	if !slices.Contains(keys, "mark:ABC-9") || slices.Contains(keys, "mark:ABC-1") {
		t.Fatalf("failed refresh replaced prior links: %v", keys)
	}
}
