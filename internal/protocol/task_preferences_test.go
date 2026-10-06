package protocol

import "testing"

func TestIgnoredDisplayGroupsAndSummaryPreserveLifecycle(t *testing.T) {
	tasks := []Task{{Attention: "immediate", Ignored: true}, {Attention: "attention", Ignored: true}, {Attention: "in_progress", Ignored: true}, {Attention: "low_priority", Ignored: true}, {Attention: "done", Ignored: true}, {Attention: "attention"}}
	want := []string{"ignored", "ignored", "ignored", "ignored", "done", "attention"}
	for i, task := range tasks {
		if task.DisplayGroup() != want[i] {
			t.Fatalf("group %d = %s", i, task.DisplayGroup())
		}
	}
	summary := SummarizeTasks(tasks)
	if summary != (Summary{Ignored: 4, Done: 1, Attention: 1}) {
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
