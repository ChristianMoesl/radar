package state

import (
	"io"
	"log/slog"
	"path/filepath"
	"radar/internal/protocol"
	"testing"
	"time"
)

func ignoredNote(ref protocol.SourceRef) protocol.SourceRef {
	binding := ref.Binding()
	return protocol.SourceRef{ID: "notes:task:one", Source: "notes", Kind: "task", Role: protocol.SourceRefRoleAuthoritative, Authored: true, Ignored: true, Bindings: []protocol.SourceBinding{binding}, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "low_priority", CanonicalKey: "notes:task:one", LinkingKeys: []string{"notes:task:one", binding.LinkingKey()}}
}

func TestIgnoredProjectionIsNoteOwnedAndSourceIndependent(t *testing.T) {
	for _, source := range []string{"github", "jira", "datadog", "git", "tmux", "sbx"} {
		t.Run(source, func(t *testing.T) {
			remote := protocol.SourceRef{ID: source + ":one", Source: source, Kind: "item", Role: protocol.SourceRefRoleAuthoritative, Signal: "immediate", CanonicalKey: source + ":one"}
			note := ignoredNote(remote)
			now := time.Now().UTC()
			state := reconcileState(persistedState{}, []protocol.Task{makeTask("low_priority", "authored", note), makeTask("immediate", "source alert", remote)}, now)
			tasks := projectTasks(state)
			if len(tasks) != 1 || !tasks[0].Ignored || tasks[0].Attention != "immediate" || tasks[0].DisplayGroup() != "ignored" {
				t.Fatalf("projection = %+v", tasks)
			}
			note.Ignored = false
			state = reconcileStateForSources(state, []protocol.Task{makeTask("low_priority", "authored", note)}, now.Add(time.Minute), map[string]bool{"notes": true})
			tasks = projectTasks(state)
			if len(tasks) != 1 || tasks[0].Ignored || tasks[0].Attention != "immediate" {
				t.Fatalf("unignore = %+v", tasks)
			}
		})
	}
}

func TestIgnoredResourceBindingDoesNotClaimNewLifetime(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{true: "cold cache", false: "existing cache"}[reset], func(t *testing.T) {
			resource := protocol.SourceRef{ID: "runtime:session:name", Source: "runtime", Kind: "session", Role: protocol.SourceRefRoleAuthoritative, BindingKey: "instance-one", CanonicalKey: "runtime:session:name", Signal: "in_progress"}
			note := ignoredNote(resource)
			now := time.Now().UTC()
			state := reconcileState(persistedState{}, []protocol.Task{makeTask("low_priority", "authored", note), makeTask("in_progress", "runtime", resource)}, now)
			if len(projectTasks(state)) != 1 {
				t.Fatal("same lifetime did not bind")
			}
			if reset {
				state = persistedState{}
			}
			resource.BindingKey = "instance-two"
			state = reconcileState(state, []protocol.Task{makeTask("low_priority", "authored", note), makeTask("in_progress", "runtime", resource)}, now.Add(time.Minute))
			tasks := projectTasks(state)
			if len(tasks) != 2 {
				t.Fatalf("replacement inherited old task: %+v", tasks)
			}
			for _, task := range tasks {
				for _, ref := range task.SourceRefs {
					if ref.ID == resource.ID && task.Ignored {
						t.Fatal("new lifetime inherited ignore")
					}
				}
			}
		})
	}
}

func TestIgnoredPreferenceCannotComeFromInformationalRef(t *testing.T) {
	remote := protocol.SourceRef{ID: "remote:one", Source: "remote", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Signal: "attention", CanonicalKey: "remote:one"}
	info := ignoredNote(remote)
	info.Role = protocol.SourceRefRoleInformational
	state := reconcileState(persistedState{}, []protocol.Task{makeTask("attention", "source", remote)}, time.Now().UTC())
	state = reconcileStateForSources(state, []protocol.Task{{TargetTaskID: state.Records[0].NumericID, SourceRefs: []protocol.SourceRef{info}}}, time.Now().UTC(), map[string]bool{"notes": true})
	if tasks := projectTasks(state); len(tasks) != 1 || tasks[0].Ignored {
		t.Fatalf("informational preference promoted: %+v", tasks)
	}
}

func TestCollectionTasksRetainsHiddenBoundCompletionFacts(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	remote := protocol.SourceRef{ID: "remote:one", Source: "remote", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Signal: "done", Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, RetainInactive: true}
	note := ignoredNote(remote)
	note.Signal = "done"
	note.Metadata = map[string]string{"completed_at": time.Now().Add(-5 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	st, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.SetTasks([]protocol.Task{makeTask("done", "authored", note), makeTask("done", "remote", remote)})
	if len(st.Tasks()) != 0 {
		t.Fatal("historical task should not be displayed")
	}
	tracked := st.CollectionTasks()
	if len(tracked) != 1 || len(tracked[0].SourceRefs) != 2 || tracked[0].Attention != "done" || !tracked[0].Ignored {
		t.Fatalf("lost terminal lookup facts: %+v", tracked)
	}
	tracked[0].SourceRefs[0].Bindings = nil
	if len(st.CollectionTasks()[0].SourceRefs) != 2 {
		t.Fatal("collection mutated store")
	}
}
