package state

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"radar/internal/integration/github/filters"
	"radar/internal/protocol"
)

func TestHiddenParticipationSurvivesCollectionRestartAndLinking(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	ref := testGitHubPRRef("github:pr:acme/app:7", "acme/app", "work")
	ref.Presentation.Hidden = true
	ref.Signal = "in_progress"
	store.SetTasks([]protocol.Task{makeTask("in_progress", "participated PR", ref)})
	if len(store.Tasks()) != 0 {
		t.Fatal("dormant participation independently visible")
	}
	for i := 0; i < 2; i++ {
		tracked := store.CollectionTasks()
		if len(tracked) != 1 || len(tracked[0].SourceRefs) != 1 || tracked[0].TrackingOnly || tracked[0].SourceRefs[0].Signal != "in_progress" {
			t.Fatalf("hidden active work lost to collection: %+v", tracked)
		}
		if i == 0 {
			store, err = NewStore(logger)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	local := testGitWorktreeRef("git:worktree:/work/app", "/work/app", "acme/app", "work")
	store.SetTasksForSources([]protocol.Task{makeTask("in_progress", "local work", local)}, []string{"git"})
	if tasks := store.Tasks(); len(tasks) != 1 || len(tasks[0].SourceRefs) != 2 || tasks[0].Attention != "in_progress" {
		t.Fatalf("hidden PR failed to link to local work: %+v", tasks)
	}
}

func TestPolicyDoesNotChangeLifecycleOrPersistFilteredSignal(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	pr := testGitHubPRRef("github:pr:acme/app:7", "acme/app", "work")
	pr.Metadata = map[string]string{"author": "bot"}
	jira := testJiraIssueRef("jira:issue:ABC-7", "ABC-7")
	pr.LinkingKeys, jira.LinkingKeys = []string{"shared"}, []string{"shared"}
	store.SetTasks([]protocol.Task{makeTask("attention", "PR feedback", pr), makeTask("done", "Jira done", jira)})
	rules := []filters.PullRequestRule{{Authors: []string{"bot"}, Action: "mute"}}
	view := filters.Apply(store.Tasks(), rules)
	if len(view) != 1 || view[0].Attention == "done" {
		t.Fatalf("muting open PR completed linked work: %+v", view)
	}
	if store.state.Records[0].State == "done" {
		t.Fatal("raw lifecycle changed")
	}
	for _, ref := range store.CollectionTasks()[0].SourceRefs {
		if ref.ID == pr.ID && ref.Signal != "attention" {
			t.Fatal("source signal mutated")
		}
	}
	reloaded, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	if tasks := reloaded.Tasks(); len(tasks) != 1 || tasks[0].Attention != "attention" {
		t.Fatalf("persisted filtered view: %+v", tasks)
	}
}
