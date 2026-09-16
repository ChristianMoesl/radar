package collector

import (
	"reflect"
	"testing"

	"radar/internal/protocol"
)

func TestReplaceSourcePreservesUnrelatedResults(t *testing.T) {
	for _, local := range []bool{false, true} {
		old := protocol.SourceRef{ID: "author:old", Source: "author", Signal: "in_progress"}
		other := protocol.SourceRef{ID: "other:work", Source: "other", Signal: "in_progress"}
		result := Result{
			Tasks:       []protocol.Task{{SourceRefs: []protocol.SourceRef{old}}, {SourceRefs: []protocol.SourceRef{other}}},
			SourceNames: []string{"author", "other"},
			Sources:     []protocol.SourceStatus{{Name: "author", Status: "ok"}, {Name: "other", Status: "error", Detail: "keep"}},
			Complete:    map[string]bool{"author": true, "other": false},
		}
		if local {
			result.Complete = nil
		}
		fresh := Result{
			Tasks:       []protocol.Task{{SourceRefs: []protocol.SourceRef{{ID: "author:new", Source: "author", Signal: "done"}}}},
			SourceNames: []string{"author"},
			Sources:     []protocol.SourceStatus{{Name: "author", Status: "partial", Detail: "new error"}},
			Complete:    map[string]bool{"author": false},
		}
		result.ReplaceSource("author", fresh)
		if len(result.Tasks) != 2 || !reflect.DeepEqual(result.Tasks[0].SourceRefs, []protocol.SourceRef{other}) || !reflect.DeepEqual(result.Tasks[1], fresh.Tasks[0]) {
			t.Fatalf("tasks = %+v", result.Tasks)
		}
		if !reflect.DeepEqual(result.SourceNames, []string{"author", "other"}) || result.Sources[0] != fresh.Sources[0] || result.Sources[1].Detail != "keep" {
			t.Fatalf("source statuses = %+v", result)
		}
		if local && result.Complete != nil {
			t.Fatal("local refresh acquired full-collection evidence")
		}
		if !local && (result.Complete["author"] || result.Complete["other"]) {
			t.Fatalf("completeness = %+v", result.Complete)
		}
		// A complete empty snapshot must remove old author observations too.
		result.ReplaceSource("author", Result{SourceNames: []string{"author"}, Sources: []protocol.SourceStatus{{Name: "author", Status: "ok"}}, Complete: map[string]bool{"author": true}})
		if len(result.Tasks) != 1 || result.Tasks[0].SourceRefs[0].ID != other.ID {
			t.Fatal("empty source snapshot kept stale authored refs")
		}
	}
}
