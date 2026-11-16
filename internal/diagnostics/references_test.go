package diagnostics

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"radar/internal/protocol"
	"radar/internal/state"
)

func TestCompleteRetainedReferenceAndBindingDrillDown(t *testing.T) {
	store, _, path := fixture(t)
	input := task(path, "test:ABC-123")
	for i := 0; i < 25; i++ {
		input.SourceRefs = append(input.SourceRefs, protocol.SourceRef{ID: "test:resource-" + strconv.Itoa(i), Source: "test", Kind: "resource", Role: protocol.SourceRefRoleInformational, Title: "private-title"})
	}
	for i := 0; i < 21; i++ {
		input.SourceRefs[0].Bindings = append(input.SourceRefs[0].Bindings, protocol.SourceBinding{Source: "test", Kind: "issue", ID: "ABC-" + strconv.Itoa(i), Key: "lifetime-" + strconv.Itoa(i), WorkItem: true})
	}
	input.SourceRefs[0].Bindings = append(input.SourceRefs[0].Bindings, input.SourceRefs[0].Bindings[0])
	input.SourceRefs[0].BindingError = "private-provider-body"
	store.SetTasks([]protocol.Task{input})
	tasks, err := read(store, protocol.DiagnosticsRequest{Section: "tasks"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 || !tasks.Items[0].Task.Truncated {
		t.Fatalf("summary fixture wrong: %+v", tasks)
	}
	req := protocol.DiagnosticsRequest{Section: "source_refs", ParentID: strconv.Itoa(tasks.Items[0].Task.TaskID), Workspace: path, Limit: 5}
	seen := map[string]bool{}
	providerID := ""
	for {
		page, err := read(store, req, false)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 27 {
			t.Fatalf("lost retained refs: total=%d", page.Total)
		}
		encoded, _ := json.Marshal(page)
		if strings.Contains(string(encoded), "private-") {
			t.Fatalf("private source payload exposed: %s", encoded)
		}
		for _, item := range page.Items {
			if seen[item.ID] || item.Ref == nil || item.Ref.RecordID != tasks.Items[0].ID {
				t.Fatalf("invalid source page: %+v", item)
			}
			seen[item.ID] = true
			if item.Ref.ProviderID == "test:ABC-123" {
				providerID = item.ID
				if !item.Ref.LifecycleInput || item.Ref.BindingCount != 22 || !item.Ref.BindingProblem {
					t.Fatalf("missing lifecycle/binding evidence: %+v", item.Ref)
				}
			}
		}
		if page.Complete {
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 27 || providerID == "" {
		t.Fatal("nested inventory incomplete")
	}
	req = protocol.DiagnosticsRequest{Section: "bindings", ParentID: providerID, Workspace: path, Limit: 7}
	bindings := map[string]protocol.DiagnosticBinding{}
	occurrences := 0
	for {
		page, err := read(store, req, false)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 21 {
			t.Fatalf("bad semantic binding total: %+v", page)
		}
		for _, item := range page.Items {
			if item.Binding == nil || item.Binding.SourceRefID != providerID || item.Binding.RecordID != tasks.Items[0].ID || !item.Binding.WorkItem {
				t.Fatalf("invalid binding: %+v", item)
			}
			if _, exists := bindings[item.ID]; exists {
				t.Fatal("duplicate binding pagination identity")
			}
			bindings[item.ID] = *item.Binding
			occurrences += item.Binding.Occurrences
		}
		if page.Complete {
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(bindings) != 21 || occurrences != 22 {
		t.Fatal("binding entries lost")
	}
	// Stable semantic IDs survive authored ordering changes; continuation cursors
	// still cannot span revisions or changed selectors.
	for i, j := 0, len(input.SourceRefs[0].Bindings)-1; i < j; i, j = i+1, j-1 {
		input.SourceRefs[0].Bindings[i], input.SourceRefs[0].Bindings[j] = input.SourceRefs[0].Bindings[j], input.SourceRefs[0].Bindings[i]
	}
	store.SetTasks([]protocol.Task{input})
	req.Cursor = ""
	req.Limit = 50
	page, err := read(store, req, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if _, exists := bindings[item.ID]; !exists {
			t.Fatal("binding order changed stable identity")
		}
	}
	req.ParentID = "not-this-ref"
	page, err = read(store, req, false)
	if err != nil || page.Total != 0 {
		t.Fatal("parent selector ignored")
	}
	if _, err := read(store, protocol.DiagnosticsRequest{Section: "tasks", ParentID: "ignored?"}, false); err == nil {
		t.Fatal("irrelevant parent selector accepted")
	}
}

func TestUnownedReferenceScopeDoesNotLeakOtherUnownedObservations(t *testing.T) {
	refs := []state.SourceRefRecord{
		{ID: "retained-one", Snapshot: protocol.SourceRef{ID: "provider-one", Path: "/work/origin", Bindings: []protocol.SourceBinding{{Source: "test", ID: "ABC-1"}}}},
		{ID: "retained-two", Snapshot: protocol.SourceRef{ID: "provider-two", Path: "/work/other", Bindings: []protocol.SourceBinding{{Source: "test", ID: "ABC-2"}}}},
	}
	byRecord := map[string][]state.SourceRefRecord{"": refs}
	for _, section := range []string{"source_refs", "bindings"} {
		items := referenceItems(protocol.DiagnosticsRequest{Section: section, Workspace: "/work/origin"}, nil, refs, byRecord)
		if len(items) != 1 {
			t.Fatalf("unowned scope escaped: %+v", items)
		}
		if section == "source_refs" && (items[0].ID != referenceID("retained-one") || items[0].Ref.ProviderID != "provider-one" || items[0].Ref.RecordID != "" || items[0].Ref.LifecycleInput) {
			t.Fatalf("retained/provider identity confused: %+v", items[0])
		}
		if section == "bindings" && items[0].Binding.TargetID != "ABC-1" {
			t.Fatal("unrelated binding exposed")
		}
	}
}

func TestLongProviderIdentitiesRemainSelectableWithoutClippingDiagnosticIDs(t *testing.T) {
	raw := "test:" + strings.Repeat("long-provider-identity", 70)
	ref := state.SourceRefRecord{ID: raw, Snapshot: protocol.SourceRef{ID: raw, Bindings: []protocol.SourceBinding{{Source: "test", ID: "ABC-123"}}}}
	refs := []state.SourceRefRecord{ref}
	byRecord := map[string][]state.SourceRefRecord{"": refs}
	view := sourceView(ref, "", 0, false)
	if len(view.ID) > 512 || view.ID == view.ProviderID {
		t.Fatal("diagnostic identity is not independently bounded")
	}
	items := referenceItems(protocol.DiagnosticsRequest{Section: "source_refs", ID: view.ID}, nil, refs, byRecord)
	if len(items) != 1 || items[0].ID != view.ID {
		t.Fatal("bounded reference selector cannot resolve the full retained identity")
	}
	items = referenceItems(protocol.DiagnosticsRequest{Section: "bindings", ParentID: view.ID}, nil, refs, byRecord)
	if len(items) != 1 || items[0].Binding.SourceRefID != view.ID {
		t.Fatal("long retained identity cannot be drilled into")
	}
}

func TestScopedTaskViewExplainsSharedIdentityHoldWithoutMergingOrPrivatePayloads(t *testing.T) {
	store, _, path := fixture(t)
	a, b := task(path, "test:ABC-1"), task(path+"-other", "test:ABC-2")
	for _, input := range []*protocol.Task{&a, &b} {
		input.SourceRefs[0].Authored = true
		input.SourceRefs[0].LinkingKeys = []string{"mark:ABC-123"}
	}
	store.SetTasks([]protocol.Task{a, b})
	page, err := read(store, protocol.DiagnosticsRequest{Section: "tasks", Workspace: path}, false)
	if err != nil || page.Total != 1 {
		t.Fatalf("scope or identity separation lost: %+v %v", page, err)
	}
	view := page.Items[0].Task
	if !view.LinkingBlocked || view.AuthoredIdentityCount != 1 || view.LinkingComponentRecords != 2 {
		t.Fatalf("production blocker missing: %+v", view)
	}
	encoded, _ := json.Marshal(page)
	for _, private := range []string{"private-title", "private-token", "private-provider-body", path + "-other", "test:ABC-2"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private or unrelated identity exposed: %s", encoded)
		}
	}
	records, _, _ := store.InspectionSnapshot()
	if len(records) != 2 {
		t.Fatal("inspection merged separate authored tasks")
	}
}
