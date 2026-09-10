package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestResponseIncludesCleanupPayloads(t *testing.T) {
	preview := CleanupPreview{TaskID: 7, TaskTitle: "ship", Targets: []CleanupTarget{{SourceRefID: "tmux:session:$1", Source: "tmux", Kind: "session", ResourceID: "$1"}}}
	result := CleanupResult{TaskID: 7, Targets: preview.Targets}
	data, err := json.Marshal(Response{OK: true, CleanupPreview: &preview, CleanupResult: &result})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`"cleanup_preview"`, `"cleanup_result"`, `"targets"`, `"resource_id":"$1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response should include %s, got %s", want, body)
		}
	}
}

func TestResponseIncludesGarbageCollectionResult(t *testing.T) {
	result := GarbageCollectionResult{
		Deleted: []GarbageCollectionItem{{TaskID: 7, Path: "/workspaces/ship"}},
		Skipped: []GarbageCollectionItem{{TaskID: 8, Path: "/workspaces/dirty", Reason: "workspace has local changes"}},
	}
	data, err := json.Marshal(Response{OK: true, GarbageCollectionResult: &result})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`"garbage_collection_result"`, `"deleted"`, `"skipped"`, `"workspace has local changes"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response should include %s, got %s", want, body)
		}
	}
}

func TestResponseIncludesProjectedBusyState(t *testing.T) {
	data, err := json.Marshal(Response{OK: true, Tasks: []Task{{
		ID: 7, Title: "ship", Busy: true, SourceRefs: []SourceRef{{ID: "tmux:session:$1", Busy: true}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Count(body, `"busy":true`) != 2 {
		t.Fatalf("response should include task and source-ref busy state, got %s", body)
	}
}

func TestResponseIncludesEmptyTasksAndSources(t *testing.T) {
	data, err := json.Marshal(Response{OK: true, Revision: 7, Summary: &Summary{}, Tasks: []Task{}, Sources: []SourceStatus{}})
	if err != nil {
		t.Fatal(err)
	}

	body := string(data)
	for _, want := range []string{`"revision":7`, `"tasks":[]`, `"sources":[]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response should include %s for GUI clearing, got %s", want, body)
		}
	}
}

func TestCleanupPresentationRoundTrip(t *testing.T) {
	preview := CleanupPreview{TaskID: 7, TaskTitle: "ABC-123 · Small fix", Targets: []CleanupTarget{{
		Source: "git", Kind: "worktree", Path: "/work/repo--fix", Branch: "fix",
		Presentation: CleanupPresentation{Singular: "worktree", Plural: "worktrees", Label: "repo", Detail: "fix"},
		Safety:       []CleanupSafety{{Kind: "deletes_local_data", Summary: "deletes local branch", Message: "deletes local branch fix"}},
		Operation:    map[string]string{"delete_branch": "fix"},
	}}}
	data, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	var got CleanupPreview
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, preview) {
		t.Fatalf("presentation or execution plan changed during round trip: %+v", got)
	}
}
