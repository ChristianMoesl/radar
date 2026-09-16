package cleanup

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestUnresolvedIncludesAllResourceIssuesWithoutLifecycleGating(t *testing.T) {
	refs := []protocol.SourceRef{
		{ID: "workspace:one", Source: "workspace", Path: "/workspaces/one", ProvidesWorkspace: true, CleanupIssues: []string{"unknown files"}},
		{ID: "git:one", Source: "git", Path: "/workspaces/one/repo", CleanupIssues: []string{"uncommitted changes", "branch commits were not found remotely", "uncommitted changes"}},
		{ID: "git:two", Source: "git", Path: "/workspaces/one/other", CleanupIssues: []string{"branch publication could not be verified"}},
		{ID: "tmux:one", Source: "tmux", Path: "/workspaces/one", InUse: true},
		{ID: "tmux:unrelated", Source: "tmux", Path: "/unrelated", InUse: true},
	}
	for _, attention := range []string{"in_progress", "done"} {
		issues := Unresolved(protocol.Task{Attention: attention, SourceRefs: refs})
		if len(issues) != 5 {
			t.Fatalf("%s: issues = %+v", attention, issues)
		}
		if issues[4].Ref.ID != "git:two" {
			// In-use issues are attached when visiting the anchor, before member checks.
			t.Fatalf("resource order lost: %+v", issues)
		}
		if issues[1].Ref.ID != "tmux:one" || issues[1].Message != InUseIssues(refs, refs[0].Path)[0].Message {
			t.Fatalf("in-use check differs from GC: %+v", issues)
		}
	}
	for i := range refs {
		refs[i].CleanupIssues = nil
		refs[i].InUse = false
	}
	if got := Unresolved(protocol.Task{SourceRefs: refs}); len(got) != 0 {
		t.Fatalf("resolved checks retained issues: %+v", got)
	}
}

func TestLocationIssuesMatchGCWorkspaceBoundary(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/workspaces/one", false}, {"/workspaces", true}, {"/workspaces-other/one", true}, {"/workspaces/../outside", true},
	} {
		if got := LocationIssue(tc.path, "/workspaces") != ""; got != tc.want {
			t.Fatalf("%s: issue = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestBlockingMessagesOnlyReturnsAutomaticCleanupRisks(t *testing.T) {
	targets := []protocol.CleanupTarget{{Safety: []protocol.CleanupSafety{
		{Message: "deletes local branch"},
		{Message: "local changes", BlocksAutomatic: true},
		{Message: "publication unknown", BlocksAutomatic: true},
	}}}
	want := []string{"local changes", "publication unknown"}
	if got := BlockingMessages(targets); !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %v, want %v", got, want)
	}
}

type issueProvider struct{ fakeProvider }

func (issueProvider) PreviewCleanup(_ context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if req.Task.SourceRefs[0].ID == "unreadable" {
		return nil, errors.New("cannot inspect resource")
	}
	return []protocol.CleanupTarget{{Safety: []protocol.CleanupSafety{{Message: "local changes", BlocksAutomatic: true}, {Message: "deletes branch"}}}}, nil
}

func TestObserveIssuesContinuesAfterErrorsAndNeverExecutesCleanup(t *testing.T) {
	var calls []string
	provider := issueProvider{fakeProvider{calls: &calls}}
	refs := []protocol.SourceRef{{ID: "unreadable"}, {ID: "readable"}}
	ObserveIssues(context.Background(), provider, refs)
	if !reflect.DeepEqual(refs[0].CleanupIssues, []string{"cannot inspect resource"}) || !reflect.DeepEqual(refs[1].CleanupIssues, []string{"local changes"}) || len(calls) != 0 {
		t.Fatalf("refs = %+v; cleanup calls = %v", refs, calls)
	}
}
