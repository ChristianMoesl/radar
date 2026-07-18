package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponseIncludesCleanupPayloads(t *testing.T) {
	preview := CleanupPreview{TaskID: 7, TaskTitle: "ship", Targets: []CleanupTarget{{SourceRefID: "tmux:session:$1", Source: "tmux", Kind: "session", SessionName: "$1"}}}
	result := CleanupResult{TaskID: 7, Targets: preview.Targets}
	data, err := json.Marshal(Response{OK: true, CleanupPreview: &preview, CleanupResult: &result})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`"cleanup_preview"`, `"cleanup_result"`, `"targets"`, `"session_name":"$1"`} {
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
