package state

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"radar/internal/protocol"
)

func TestLegacyBindingsOnlyPromotesUnambiguousOwnership(t *testing.T) {
	const first = "12345678-1234-4234-8234-123456789abc"
	const second = "22345678-1234-4234-8234-123456789abc"
	noteRef := func(id string) protocol.SourceRef {
		return protocol.SourceRef{ID: "obsidian:task:" + id, Source: "obsidian", Kind: "task", Authored: true, Role: protocol.SourceRefRoleAuthoritative}
	}
	remote := protocol.SourceRef{ID: "github:pr:acme/app:7", Source: "github", Kind: "pr", Role: protocol.SourceRefRoleAuthoritative}
	for _, tt := range []struct {
		name      string
		canonical string
		stored    []protocol.SourceRef
		snapshot  []protocol.SourceRef
		grouped   bool
	}{
		{name: "single canonical note", canonical: noteRef(first).ID},
		{name: "same note repeated", canonical: noteRef(first).ID, stored: []protocol.SourceRef{noteRef(first)}, snapshot: []protocol.SourceRef{noteRef(first)}},
		{name: "grouped stored notes", stored: []protocol.SourceRef{noteRef(first), noteRef(second)}, grouped: true},
		{name: "grouped snapshot notes", snapshot: []protocol.SourceRef{noteRef(first), noteRef(second)}, grouped: true},
		{name: "canonical and stored notes differ", canonical: noteRef(first).ID, stored: []protocol.SourceRef{noteRef(second)}, grouped: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			old := persistedState{Version: stateVersion, NextTaskID: 7, Records: []TaskRecord{{
				ID: "task:7", NumericID: 7, CanonicalKey: tt.canonical, Snapshot: protocol.Task{SourceRefs: tt.snapshot},
			}}}
			for _, ref := range append(tt.stored, remote) {
				old.SourceRefs = append(old.SourceRefs, SourceRefRecord{ID: ref.ID, TaskRecordID: "task:7", Snapshot: ref})
			}
			data, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			bindings, warnings, err := LegacyBindings(data)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string][]protocol.SourceBinding{first: {remote.Binding()}}
			if tt.grouped {
				want = map[string][]protocol.SourceBinding{first: nil, second: nil}
				if len(warnings) != 1 || !strings.Contains(warnings[0], "task:7") {
					t.Fatalf("grouped ownership was not reported: %v", warnings)
				}
			} else if len(warnings) != 0 {
				t.Fatalf("unexpected warnings: %v", warnings)
			}
			if !reflect.DeepEqual(bindings, want) {
				t.Fatalf("bindings = %+v, want %+v", bindings, want)
			}
		})
	}
}
