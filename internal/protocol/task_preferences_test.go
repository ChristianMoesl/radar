package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMutedDisplayGroupsAndSummaryPreserveLifecycle(t *testing.T) {
	tasks := []Task{{Attention: "immediate", Muted: true}, {Attention: "attention", Muted: true}, {Attention: "in_progress", Muted: true}, {Attention: "low_priority", Muted: true}, {Attention: "done", Muted: true}, {Attention: "attention"}}
	want := []string{"muted", "muted", "muted", "muted", "done", "attention"}
	for i, task := range tasks {
		if task.DisplayGroup() != want[i] {
			t.Fatalf("group %d = %s", i, task.DisplayGroup())
		}
	}
	summary := SummarizeTasks(tasks)
	if summary != (Summary{Muted: 4, Done: 1, Attention: 1}) {
		t.Fatalf("summary = %+v", summary)
	}
	if tasks[0].Attention != "immediate" {
		t.Fatal("display classification mutated underlying attention")
	}
}

func TestBindingKeysDistinguishProvidersKindsAndResourceLifetimes(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range []SourceBinding{{Source: "first", Kind: "issue", ID: "one"}, {Source: "second", Kind: "issue", ID: "one"}, {Source: "first", Kind: "session", ID: "one"}, {Source: "first", Kind: "session", ID: "one", Key: "instance-a"}, {Source: "first", Kind: "session", ID: "one", Key: "instance-b"}} {
		if seen[b.LinkingKey()] {
			t.Fatalf("duplicate binding key %+v", b)
		}
		seen[b.LinkingKey()] = true
	}
}

func TestMutingWireSchemaUsesOnlyMuted(t *testing.T) {
	task := Task{ID: 1, Attention: "attention", Muted: true, SourceRefs: []SourceRef{{ID: "note:one", Muted: true}}}
	for name, value := range map[string]any{"task": task, "summary": SummarizeTasks([]Task{task})} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"muted":`) || strings.Contains(string(data), `"ignored":`) {
			t.Fatalf("%s schema = %s", name, data)
		}
	}
	var current Task
	if err := json.Unmarshal([]byte(`{"ignored":true,"source_refs":[{"ignored":true}]}`), &current); err != nil {
		t.Fatal(err)
	}
	if current.Muted || current.SourceRefs[0].Muted {
		t.Fatal("legacy wire fields must not act as aliases")
	}
}
