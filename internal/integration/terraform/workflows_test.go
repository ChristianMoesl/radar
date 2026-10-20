package terraform

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/collector"
	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/notification"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestJiraTitleIndependentAttentionAndContributingCompletion(t *testing.T) {
	s, api, _ := fixtureSource(t, runFixture("run-one", "policy_override", "user-me"))
	result := s.Collect(context.Background(), integration.CollectRequest{})
	terraform := result.Observations[0].Ref
	terraform.LinkingKeys = []string{"mark:ABC-123"}
	terraform.DisplayOrder = 1
	jira := protocol.SourceRef{ID: "jira:issue:ABC-123", EntityID: "jira:issue:ABC-123", CanonicalKey: "jira:issue:ABC-123", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, Title: "ABC-123 Infrastructure change", Signal: "in_progress", Status: "In progress", Presentation: protocol.SourceRefPresentation{PreferTitle: true}, LinkingKeys: []string{"mark:ABC-123"}}
	pr := jira
	pr.ID = "github:pr:acme/app:7"
	pr.EntityID = pr.ID
	pr.CanonicalKey = pr.ID
	pr.Source = "github"
	pr.Kind = "pull_request"
	pr.DisplayOrder = 1
	pr.Presentation.PreferTitle = false
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.json"))
	store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	items := func() []protocol.Task {
		return []protocol.Task{{Title: jira.Title, Attention: jira.Signal, SourceRefs: []protocol.SourceRef{jira}}, {Title: "PR title", Attention: pr.Signal, SourceRefs: []protocol.SourceRef{pr}}, {Title: terraform.Title, Attention: terraform.Signal, SourceRefs: []protocol.SourceRef{terraform}}}
	}
	store.SetTasks(items())
	tasks := store.Tasks()
	if len(tasks) != 1 || tasks[0].Title != jira.Title || tasks[0].Attention != "attention" || tasks[0].AttentionSourceRefID != terraform.ID || tasks[0].SourceRefs[0].Source != "jira" {
		t.Fatalf("incorrect source precedence: %+v", tasks)
	}
	sender := &captureNotifications{}
	notifier := notification.NewWithSender(slog.New(slog.NewTextHandler(io.Discard, nil)), sender)
	notifier.NotifyTransitions(context.Background(), nil, tasks)
	notifier.NotifyTransitions(context.Background(), tasks, tasks)
	if len(sender.sent) != 1 || sender.sent[0].URL != terraform.URL {
		t.Fatal("Terraform notification destination or deduplication incorrect", sender.sent)
	}
	muted := append([]protocol.Task(nil), tasks...)
	muted[0].Muted = true
	notifier.NotifyTransitions(context.Background(), nil, muted)
	if len(sender.sent) != 1 {
		t.Fatal("muted Terraform work notified")
	}
	api.runs[0] = runFixture("run-one", "applied", "user-me")
	terraform = s.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	terraform.LinkingKeys = []string{"mark:ABC-123"}
	terraform.DisplayOrder = 1
	store.SetTasks(items())
	if store.Tasks()[0].Attention == "done" {
		t.Fatal("Terraform completed an active Jira/PR task")
	}
	jira.Signal = "done"
	pr.Signal = "done"
	store.SetTasks(items())
	if store.Tasks()[0].Attention != "done" {
		t.Fatal("all completed contributors did not complete")
	}
}

type captureNotifications struct{ sent []notification.Notification }

func (c *captureNotifications) Send(_ context.Context, n notification.Notification) error {
	c.sent = append(c.sent, n)
	return nil
}

func TestMutedRunBindingSurvivesColdCollectionWithoutWorkspace(t *testing.T) {
	s, api, _ := fixtureSource(t, runFixture("run-one", "planning", "user-me"))
	first := s.Collect(context.Background(), integration.CollectRequest{})
	notes := obsidian.NewSourceAt(t.TempDir())
	if _, err := notes.SetMuted(context.Background(), tasksFrom(first)[0], true); err != nil {
		t.Fatal(err)
	}
	api.runs = nil
	api.direct["run-one"] = runFixture("run-one", "applied", "user-me")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cold := collector.Collect(context.Background(), nil, logger, []integration.Source{notes, s})
	if !cold.Complete["terraform"] {
		t.Fatalf("cold binding unresolved: %+v", cold.Sources)
	}
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "cold.json"))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks(cold.Tasks)
	tasks := store.Tasks()
	if len(tasks) != 1 || !tasks[0].Muted || len(tasks[0].SourceRefs) != 2 {
		t.Fatalf("muted binding lost: %+v", tasks)
	}
	var found bool
	for _, ref := range tasks[0].SourceRefs {
		if ref.Source == "terraform" {
			found = ref.Signal == "done"
		}
	}
	if !found {
		t.Fatal("terminal remote fact missing")
	}
	cfg, _ := config.Load()
	if _, err := os.Stat(cfg.Workspace.RootDir); !os.IsNotExist(err) {
		// The note lock may create the registry directory, but no workspace.
		entries, _ := os.ReadDir(cfg.Workspace.RootDir)
		for _, entry := range entries {
			if entry.IsDir() {
				t.Fatal("mute provisioned a workspace")
			}
		}
	}
	if _, err := notes.SetMuted(context.Background(), tasks[0], false); err != nil {
		t.Fatal(err)
	}
	unmuted := notes.Collect(context.Background(), integration.CollectRequest{})
	if len(unmuted.Observations) != 1 || unmuted.Observations[0].Ref.Muted || len(unmuted.Observations[0].Ref.Bindings) != 1 {
		t.Fatal("unmute lost bindings")
	}
}
