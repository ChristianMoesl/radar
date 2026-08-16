package state

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/linking"
	"radar/internal/protocol"
)

func TestReconcileStateUsesLinkingMarkRecordForMultiplePullRequests(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{
		{Kind: "github_own_pr", Title: "CAP-7 first", Attention: "in_progress", SourceRefs: []protocol.SourceRef{testGitHubPRRef("github:pr:acme/app:1", "acme/app", "CAP-7-a")}},
		{Kind: "github_own_pr", Title: "CAP-7 second", Attention: "in_progress", SourceRefs: []protocol.SourceRef{testGitHubPRRef("github:pr:acme/app:2", "acme/app", "CAP-7-b")}},
	}, now)

	if len(state.Records) != 1 {
		t.Fatalf("records = %d, want one linking-mark record: %+v", len(state.Records), state.Records)
	}
	if state.Records[0].CanonicalKey != "mark:CAP-7" {
		t.Fatalf("canonical key = %q, want mark:CAP-7", state.Records[0].CanonicalKey)
	}
	if len(state.Records[0].SourceRefIDs) != 2 {
		t.Fatalf("source refs = %+v, want both PR refs", state.Records[0].SourceRefIDs)
	}
}

func TestReconcileStateDurablyLinksPullRequestAndWorktreeByOriginAndBranch(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{{
		Kind:       "github_own_pr",
		Title:      "Feature without ticket",
		Attention:  "in_progress",
		SourceRefs: []protocol.SourceRef{testGitHubPRRef("github:pr:acme/app:7", "acme/app", "feature/no-ticket")},
	}}, now)

	state = reconcileStateForSources(state, []protocol.Task{{
		Kind:       "git_worktree",
		Title:      "feature-no-ticket",
		Attention:  "in_progress",
		SourceRefs: []protocol.SourceRef{testGitWorktreeRef("git:worktree:/workspaces/app/feature-no-ticket", "/workspaces/app/feature-no-ticket", "acme/app", "feature-no-ticket")},
	}}, now.Add(time.Hour), map[string]bool{"git": true})

	if len(state.Records) != 1 {
		t.Fatalf("records = %d, want durable merge: %+v", len(state.Records), state.Records)
	}
	if len(state.Records[0].SourceRefIDs) != 2 {
		t.Fatalf("source refs = %+v, want PR and worktree", state.Records[0].SourceRefIDs)
	}
	for _, ref := range state.SourceRefs {
		if ref.TaskRecordID != state.Records[0].ID {
			t.Fatalf("source ref %s linked to %s, want %s", ref.ID, ref.TaskRecordID, state.Records[0].ID)
		}
	}
}

func TestReconcileStateLinksPullRequestThroughRemovedWorkspaceMember(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	primary := withWorkspaceGroup(testGitWorktreeRef("git:worktree:/workspaces/primary/work", "/workspaces/primary/work", "acme/primary", "work"), "group-a")
	member := withWorkspaceGroup(testGitWorktreeRef("git:worktree:/workspaces/app/feature", "/workspaces/app/feature", "acme/app", "feature/no-ticket"), "group-a")
	state := reconcileStateForSources(persistedState{Version: stateVersion}, []protocol.Task{
		makeTask("in_progress", "git worktree", primary),
		makeTask("in_progress", "git worktree", member),
	}, now, map[string]bool{"git": true})

	state = reconcileStateForSources(state, []protocol.Task{
		makeTask("in_progress", "git worktree", primary),
	}, now.Add(time.Hour), map[string]bool{"git": true})
	state = reconcileStateForSources(state, []protocol.Task{
		makeTask("in_progress", "draft PR", testGitHubPRRef("github:pr:acme/app:7", "acme/app", "feature/no-ticket")),
	}, now.Add(2*time.Hour), map[string]bool{"github": true})

	if len(state.Records) != 1 {
		t.Fatalf("records = %d, want PR linked through removed workspace member: %+v", len(state.Records), state.Records)
	}
	tasks := projectTasks(state)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want one linked task: %+v", len(tasks), tasks)
	}
	if len(tasks[0].SourceRefs) != 2 {
		t.Fatalf("active source refs = %+v, want primary worktree and PR only", tasks[0].SourceRefs)
	}
	for _, ref := range tasks[0].SourceRefs {
		if ref.ID == member.ID {
			t.Fatalf("removed workspace member is still projected: %+v", tasks[0].SourceRefs)
		}
	}
}

func TestReconcileStateDoesNotLinkThroughRemovedMemberOfClosedWorkspace(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	primary := withWorkspaceGroup(testGitWorktreeRef("git:worktree:/workspaces/primary/work", "/workspaces/primary/work", "acme/primary", "work"), "group-a")
	member := withWorkspaceGroup(testGitWorktreeRef("git:worktree:/workspaces/app/feature", "/workspaces/app/feature", "acme/app", "feature/no-ticket"), "group-a")
	state := reconcileStateForSources(persistedState{Version: stateVersion}, []protocol.Task{
		makeTask("in_progress", "git worktree", primary),
		makeTask("in_progress", "git worktree", member),
	}, now, map[string]bool{"git": true})
	state = reconcileStateForSources(state, nil, now.Add(time.Hour), map[string]bool{"git": true})
	state = reconcileStateForSources(state, []protocol.Task{
		makeTask("in_progress", "draft PR", testGitHubPRRef("github:pr:acme/app:7", "acme/app", "feature/no-ticket")),
	}, now.Add(2*time.Hour), map[string]bool{"github": true})

	if len(state.Records) != 2 {
		t.Fatalf("records = %d, want closed workspace and PR to remain separate: %+v", len(state.Records), state.Records)
	}
}

func TestReconcileStateDoesNotLinkAmbiguousHistoricalWorkspaceBranch(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	primaryA := withWorkspaceGroup(testGitWorktreeRef("git:worktree:/workspaces/primary-a/work", "/workspaces/primary-a/work", "acme/primary-a", "work-a"), "group-a")
	memberA := withWorkspaceGroup(testGitWorktreeRef("git:worktree:/workspaces/app-a/feature", "/workspaces/app-a/feature", "acme/app", "feature/shared"), "group-a")
	primaryB := withWorkspaceGroup(testGitWorktreeRef("git:worktree:/workspaces/primary-b/work", "/workspaces/primary-b/work", "acme/primary-b", "work-b"), "group-b")
	memberB := withWorkspaceGroup(testGitWorktreeRef("git:worktree:/workspaces/app-b/feature", "/workspaces/app-b/feature", "acme/app", "feature/shared"), "group-b")

	state := reconcileStateForSources(persistedState{Version: stateVersion}, []protocol.Task{
		makeTask("in_progress", "git worktree", primaryA),
		makeTask("in_progress", "git worktree", memberA),
	}, now, map[string]bool{"git": true})
	state = reconcileStateForSources(state, []protocol.Task{
		makeTask("in_progress", "git worktree", primaryA),
		makeTask("in_progress", "git worktree", primaryB),
		makeTask("in_progress", "git worktree", memberB),
	}, now.Add(time.Hour), map[string]bool{"git": true})
	state = reconcileStateForSources(state, []protocol.Task{
		makeTask("in_progress", "git worktree", primaryA),
		makeTask("in_progress", "git worktree", primaryB),
	}, now.Add(2*time.Hour), map[string]bool{"git": true})
	state = reconcileStateForSources(state, []protocol.Task{
		makeTask("in_progress", "draft PR", testGitHubPRRef("github:pr:acme/app:7", "acme/app", "feature/shared")),
	}, now.Add(3*time.Hour), map[string]bool{"github": true})

	if len(state.Records) != 3 {
		t.Fatalf("records = %d, want two workspaces and ambiguous PR to remain separate: %+v", len(state.Records), state.Records)
	}
}

func TestReconcileStateReopensDoneRecord(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{{Title: "CAP-7 ship", Attention: "done", DoneAt: now.Format(time.RFC3339), SourceRefs: []protocol.SourceRef{testGitHubPRRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship")}}}, now)
	state = reconcileState(state, []protocol.Task{{Title: "CAP-7 ship", Attention: "in_progress", SourceRefs: []protocol.SourceRef{testGitHubPRRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship")}}}, now.Add(time.Hour))

	if len(state.Records) != 1 {
		t.Fatalf("records = %d, want one reused record", len(state.Records))
	}
	if state.Records[0].State != "active" || state.Records[0].DoneAt != "" {
		t.Fatalf("record state = %s done_at=%q, want active with no done_at", state.Records[0].State, state.Records[0].DoneAt)
	}
}

func TestReconcileStatePreservesDoneAtAcrossRepeatedDoneObservations(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	task := protocol.Task{Title: "CAP-7 ship", Attention: "done", SourceRefs: []protocol.SourceRef{testGitHubPRRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship")}}
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{task}, now)
	state = reconcileState(state, []protocol.Task{task}, now.Add(time.Hour))

	if len(state.Records) != 1 {
		t.Fatalf("records = %d, want one reused record", len(state.Records))
	}
	if state.Records[0].DoneAt != now.Format(time.RFC3339) {
		t.Fatalf("done_at = %q, want original completion time %q", state.Records[0].DoneAt, now.Format(time.RFC3339))
	}
}

func TestProjectTasksKeepsActiveWorkWhenLinkedRemoteIsDone(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{makeTask("in_progress", "jira issue", testJiraIssueRef("jira:issue:CAP-7", "CAP-7 Ship"))}, now)
	state = reconcileState(state, []protocol.Task{
		makeTask("in_progress", "jira issue", testJiraIssueRef("jira:issue:CAP-7", "CAP-7 Ship")),
		makeTask("done", "merged today", withSignal(withStatus(testGitHubPRRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship"), "merged today"), "done")),
	}, now.Add(time.Hour))

	tasks := projectTasks(state)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want one linked task: %+v", len(tasks), tasks)
	}
	if tasks[0].Attention != "in_progress" {
		t.Fatalf("task attention = %q, want active Jira work to stay in_progress: %+v", tasks[0].Attention, tasks[0])
	}
}

func TestProjectTasksPromotesLowPriorityJiraWithLinkedSignals(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	jira := withSignal(withStatus(testJiraIssueRef("jira:issue:CAP-7", "CAP-7 Ship"), "Selected for Development"), "low_priority")
	worktree := withSignal(testGitWorktreeRef("git:worktree:/work/CAP-7-ship", "/work/CAP-7-ship", "acme/app", "feature/CAP-7-ship"), "in_progress")
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{
		makeTask("low_priority", "Selected for Development", jira),
		makeTask("in_progress", "git worktree", worktree),
	}, now)
	if got := projectTasks(state); len(got) != 1 || got[0].Attention != "in_progress" {
		t.Fatalf("Jira + worktree tasks = %+v, want one in-progress task", got)
	}

	github := withSignal(withStatus(testGitHubPRRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship"), "review requested"), "attention")
	state = reconcileState(state, []protocol.Task{
		makeTask("low_priority", "Selected for Development", jira),
		makeTask("in_progress", "git worktree", worktree),
		makeTask("attention", "review requested", github),
	}, now.Add(time.Hour))
	if got := projectTasks(state); len(got) != 1 || got[0].Attention != "attention" {
		t.Fatalf("Jira + worktree + GitHub tasks = %+v, want one attention task", got)
	}
}

func TestProjectTasksProjectsBusyFromActiveSourceRefs(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	jira := testJiraIssueRef("jira:issue:CAP-7", "CAP-7 Ship")
	worktree := testGitWorktreeRef("git:worktree:/work/CAP-7-ship", "/work/CAP-7-ship", "acme/app", "CAP-7-ship")
	worktree.Busy = true
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{
		makeTask("in_progress", "jira issue", jira),
		makeTask("in_progress", "git worktree", worktree),
	}, now)

	if tasks := projectTasks(state); len(tasks) != 1 || !tasks[0].Busy {
		t.Fatalf("projected tasks = %+v, want one busy task", tasks)
	}

	worktree.Busy = false
	state = reconcileState(state, []protocol.Task{
		makeTask("in_progress", "jira issue", jira),
		makeTask("in_progress", "git worktree", worktree),
	}, now.Add(time.Hour))
	if tasks := projectTasks(state); len(tasks) != 1 || tasks[0].Busy {
		t.Fatalf("projected tasks = %+v, want busy cleared", tasks)
	}
}

func TestSourceScopedRefreshKeepsExistingRefActiveWhenNewRefComesFirst(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	existing := testGitWorktreeRef("git:worktree:/work/existing", "/work/existing", "acme/app", "existing")
	added := testGitWorktreeRef("git:worktree:/work/added", "/work/added", "acme/app", "added")
	state := reconcileStateForSources(persistedState{Version: stateVersion}, []protocol.Task{
		makeTask("in_progress", "existing", existing),
	}, now, map[string]bool{"git": true})

	state = reconcileStateForSources(state, []protocol.Task{
		makeTask("in_progress", "added", added),
		makeTask("in_progress", "existing", existing),
	}, now.Add(time.Hour), map[string]bool{"git": true})

	active := map[string]bool{}
	for _, ref := range state.SourceRefs {
		active[ref.ID] = ref.Active
	}
	if !active[existing.ID] || !active[added.ID] {
		t.Fatalf("source refs = %+v", state.SourceRefs)
	}
}

func TestProjectTasksMarksDoneWhenRemoteDoneAndOnlyLocalRemains(t *testing.T) {
	now := time.Now().UTC()
	worktree := testGitWorktreeRef("git:worktree:/work/CAP-7-ship", "/work/CAP-7-ship", "acme/app", "CAP-7-ship")
	worktree.Busy = true
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{
		makeTask("done", "merged today", withSignal(withStatus(testGitHubPRRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship"), "merged today"), "done")),
		makeTask("in_progress", "git worktree", worktree),
	}, now)

	tasks := projectTasks(state)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want one done task: %+v", len(tasks), tasks)
	}
	if tasks[0].Attention != "done" || tasks[0].Reason != "merged today" || tasks[0].Busy {
		t.Fatalf("task = %+v, want non-busy done remote completion", tasks[0])
	}
}

func TestProjectTasksHidesInactiveLocalRefsFromDoneTasks(t *testing.T) {
	now := time.Now().UTC()
	remote := testGitHubPRRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship")
	worktree := testGitWorktreeRef("git:worktree:/work/CAP-7-ship", "/work/CAP-7-ship", "acme/app", "CAP-7-ship")
	session := testTmuxSessionRef("tmux:session:$7", "/work/CAP-7-ship")
	sandbox := protocol.SourceRef{ID: "sbx:sandbox:CAP-7-ship", EntityID: "sbx:sandbox:CAP-7-ship", Source: "sbx", Kind: "sandbox", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, Path: "/work/CAP-7-ship"}
	state := persistedState{
		Version: stateVersion,
		Records: []TaskRecord{{
			ID:        "task:7",
			NumericID: 7,
			State:     "done",
			DoneAt:    now.Format(time.RFC3339),
			Snapshot:  protocol.Task{Title: "CAP-7 ship", Attention: "done", SourceRefs: []protocol.SourceRef{remote, worktree, session, sandbox}},
		}},
		SourceRefs: []SourceRefRecord{
			{ID: remote.ID, Source: remote.Source, Kind: remote.Kind, TaskRecordID: "task:7", Active: false, Snapshot: remote},
			{ID: worktree.ID, Source: worktree.Source, Kind: worktree.Kind, TaskRecordID: "task:7", Active: true, Snapshot: worktree},
			{ID: session.ID, Source: session.Source, Kind: session.Kind, TaskRecordID: "task:7", Active: false, Snapshot: session},
			{ID: sandbox.ID, Source: sandbox.Source, Kind: sandbox.Kind, TaskRecordID: "task:7", Active: false, Snapshot: sandbox},
		},
	}

	tasks := projectTasks(state)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v, want one done task", tasks)
	}
	if len(tasks[0].SourceRefs) != 2 || tasks[0].SourceRefs[0].ID != remote.ID || tasks[0].SourceRefs[1].ID != worktree.ID {
		t.Fatalf("source refs = %+v, want historical remote and active worktree only", tasks[0].SourceRefs)
	}
}

func TestProjectTasksClearsSnapshotRefsWhenDoneLocalResourcesAreGone(t *testing.T) {
	now := time.Now().UTC()
	worktree := testGitWorktreeRef("git:worktree:/work/local", "/work/local", "", "local")
	state := persistedState{
		Version: stateVersion,
		Records: []TaskRecord{{
			ID:        "task:1",
			NumericID: 1,
			State:     "done",
			DoneAt:    now.Format(time.RFC3339),
			Snapshot:  protocol.Task{Title: "local", Attention: "done", SourceRefs: []protocol.SourceRef{worktree}},
		}},
		SourceRefs: []SourceRefRecord{{ID: worktree.ID, Source: worktree.Source, Kind: worktree.Kind, TaskRecordID: "task:1", Active: false, Snapshot: worktree}},
	}

	tasks := projectTasks(state)
	if len(tasks) != 0 {
		t.Fatalf("tasks = %+v, want source-less local task to disappear", tasks)
	}
}

func TestProjectTasksAppliesAcknowledgementOutsideSourceMetadata(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{{
		Kind:      "github_own_pr",
		Title:     "CAP-7 ship",
		Attention: "attention",
		Reason:    "1 new PR comment(s)",
		SourceRefs: []protocol.SourceRef{withMetadata(testGitHubPRRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship"), map[string]string{
			"base_reason":               "open PR",
			"new_general_comments":      "1",
			"latest_general_comment_at": "2026-06-15T11:00:00Z",
		})},
	}}, now)
	state.Records[0].Ack.GeneralCommentsAckAt = "2026-06-15T11:00:00Z"

	tasks := projectTasks(state)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want acknowledged own PR to remain", len(tasks))
	}
	if tasks[0].Attention != "in_progress" || tasks[0].Reason != "open PR" {
		t.Fatalf("task = %s/%s, want in_progress/open PR", tasks[0].Attention, tasks[0].Reason)
	}
	if tasks[0].SourceRefs[0].Metadata["general_comments_ack_at"] != "" {
		t.Fatalf("ack leaked into source metadata: %+v", tasks[0].SourceRefs[0].Metadata)
	}
	if tasks[0].Metadata["general_comments_ack_at"] == "" {
		t.Fatalf("ack missing from task metadata: %+v", tasks[0].Metadata)
	}
}

func TestProjectTasksKeepsLinkedActiveWorkAfterAcknowledgingPRActivity(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	githubRef := withSignal(withStatus(withMetadata(testGitHubPRRef("github:pr:acme/app:7", "acme/app", "CAP-7-ship"), map[string]string{
		"base_reason":               "draft PR",
		"new_general_comments":      "1",
		"latest_general_comment_at": "2026-06-15T11:00:00Z",
	}), "1 new PR comment(s)"), "attention")
	jiraRef := withSignal(withStatus(testJiraIssueRef("jira:issue:CAP-7", "CAP-7 Ship"), "In Progress"), "in_progress")
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{{
		Kind:       "github_pr_activity",
		Title:      "CAP-7 ship",
		Attention:  "attention",
		Reason:     "1 new PR comment(s)",
		SourceRefs: []protocol.SourceRef{githubRef, jiraRef},
	}}, now)
	state.Records[0].Ack.GeneralCommentsAckAt = "2026-06-15T11:00:00Z"

	tasks := projectTasks(state)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want linked active task to remain: %+v", len(tasks), tasks)
	}
	if tasks[0].Attention != "in_progress" || tasks[0].Reason != "In Progress" {
		t.Fatalf("task = %s/%s, want in_progress/In Progress", tasks[0].Attention, tasks[0].Reason)
	}
}

func TestProjectTasksHidesStandaloneAcknowledgedPRActivity(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{{
		Kind:      "github_pr_activity",
		Title:     "Unlinked activity",
		Attention: "attention",
		Reason:    "1 new PR comment(s)",
		SourceRefs: []protocol.SourceRef{withMetadata(testGitHubPRRef("github:pr:acme/app:7", "acme/app", "unlinked"), map[string]string{
			"new_general_comments":      "1",
			"latest_general_comment_at": "2026-06-15T11:00:00Z",
		})},
	}}, now)
	state.Records[0].Ack.GeneralCommentsAckAt = "2026-06-15T11:00:00Z"

	if tasks := projectTasks(state); len(tasks) != 0 {
		t.Fatalf("tasks = %+v, want standalone acknowledged activity hidden", tasks)
	}
}

func TestLocalReconcilePreservesRemoteRefsAndUpdatesLocalRefs(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{{
		Title:     "CAP-7 ship",
		Attention: "in_progress",
		SourceRefs: []protocol.SourceRef{
			testGitHubPRRef("github:pr:acme/app:7", "acme/app", "feature/CAP-7-ship"),
			testGitWorktreeRef("git:worktree:/old", "/old", "acme/app", "feature/CAP-7-ship"),
		},
	}}, now)

	state = reconcileStateForSources(state, []protocol.Task{{
		Title:      "feature/CAP-7-ship",
		Attention:  "in_progress",
		SourceRefs: []protocol.SourceRef{testGitWorktreeRef("git:worktree:/new", "/new", "acme/app", "feature/CAP-7-ship")},
	}}, now.Add(time.Hour), map[string]bool{"git": true, "tmux": true})

	var githubActive, oldGitActive, newGitActive bool
	for _, ref := range state.SourceRefs {
		switch ref.ID {
		case "github:pr:acme/app:7":
			githubActive = ref.Active
		case "git:worktree:/old":
			oldGitActive = ref.Active
		case "git:worktree:/new":
			newGitActive = ref.Active
		}
	}
	if !githubActive {
		t.Fatal("local reconcile deactivated remote github ref")
	}
	if oldGitActive {
		t.Fatal("local reconcile kept old git ref active")
	}
	if !newGitActive {
		t.Fatal("local reconcile did not activate new git ref")
	}
}

func TestReconcileStateMarksRemovedWorktreeDoneButNotTmuxOnly(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{
		{Title: "local", Attention: "in_progress", SourceRefs: []protocol.SourceRef{testGitWorktreeRef("git:worktree:/repo/local", "/repo/local", "", "")}},
		{Title: "session", Attention: "in_progress", SourceRefs: []protocol.SourceRef{testTmuxSessionRef("tmux:session:$1", "")}},
	}, now)
	state = reconcileState(state, nil, now.Add(time.Hour))

	var worktreeState, tmuxState string
	for _, record := range state.Records {
		switch record.CanonicalKey {
		case "workspace:/repo/local":
			worktreeState = record.State
		case "tmux:session:$1":
			tmuxState = record.State
		}
	}
	if worktreeState != "done" {
		t.Fatalf("worktree state = %q, want done", worktreeState)
	}
	if tmuxState != "active" {
		t.Fatalf("tmux state = %q, want active cleanup without done transition", tmuxState)
	}
}

func testGitHubPRRef(id string, repo string, branch string) protocol.SourceRef {
	return protocol.SourceRef{
		ID:             id,
		Source:         "github",
		Kind:           "pull_request",
		Role:           protocol.SourceRefRoleAuthoritative,
		EntityID:       id,
		Lifecycle:      protocol.SourceRefLifecycleWorkItem,
		Authority:      protocol.SourceRefAuthorityContributing,
		RetainInactive: true,
		Repo:           repo,
		Branch:         branch,
		CanonicalKey:   id,
		LinkingKeys:    linking.Keys(append(testLinkingMarks().Keys(id, repo, branch), id, linking.BranchKey(repo, testBranchKey(branch)))...),
	}
}

func testGitWorktreeRef(id string, path string, repo string, branch string) protocol.SourceRef {
	canonicalKey := linking.WorkspaceKey(path)
	return protocol.SourceRef{
		ID:                id,
		Source:            "git",
		Kind:              "worktree",
		Role:              protocol.SourceRefRoleAuthoritative,
		EntityID:          id,
		Lifecycle:         protocol.SourceRefLifecycleWorkspace,
		Authority:         protocol.SourceRefAuthorityNone,
		Repo:              repo,
		Path:              path,
		ProvidesWorkspace: true,
		Branch:            branch,
		CanonicalKey:      canonicalKey,
		LinkingKeys:       linking.Keys(append(testLinkingMarks().Keys(id, path, repo, branch), canonicalKey, linking.BranchKey(repo, testBranchKey(branch)))...),
	}
}

func testLinkingMarks() linking.MarkMatcher {
	return linking.NewMarkMatcher([]string{"CAP", "DPSCAP", "RAD"})
}

func testBranchKey(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "origin/")
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return strings.ReplaceAll(branch, "/", "-")
}

func testTmuxSessionRef(id string, path string) protocol.SourceRef {
	return protocol.SourceRef{
		ID:          id,
		Source:      "tmux",
		Kind:        "session",
		Role:        protocol.SourceRefRoleAuthoritative,
		EntityID:    id,
		Lifecycle:   protocol.SourceRefLifecycleResource,
		Authority:   protocol.SourceRefAuthorityNone,
		Path:        path,
		LinkingKeys: linking.Keys(append(testLinkingMarks().Keys(id, path), linking.WorkspaceKey(path))...),
	}
}

func testJiraIssueRef(id string, title string) protocol.SourceRef {
	key := strings.TrimPrefix(id, "jira:issue:")
	return protocol.SourceRef{
		ID:             id,
		Source:         "jira",
		Kind:           "issue",
		Role:           protocol.SourceRefRoleAuthoritative,
		EntityID:       id,
		Lifecycle:      protocol.SourceRefLifecycleWorkItem,
		Authority:      protocol.SourceRefAuthorityContributing,
		RetainInactive: true,
		Presentation:   protocol.SourceRefPresentation{PreferTitle: true},
		Title:          title,
		CanonicalKey:   id,
		LinkingKeys:    linking.Keys("mark:" + key),
	}
}

func makeTask(attention string, reason string, ref protocol.SourceRef) protocol.Task {
	if ref.Signal == "" {
		ref.Signal = attention
	}
	return protocol.Task{Title: ref.Title, Attention: attention, Reason: reason, SourceRefs: []protocol.SourceRef{ref}}
}

func withWorkspaceGroup(ref protocol.SourceRef, workspaceID string) protocol.SourceRef {
	ref.LinkingKeys = linking.Keys(append(ref.LinkingKeys, linking.WorkspaceGroupKey(workspaceID))...)
	return ref
}

func withMetadata(ref protocol.SourceRef, metadata map[string]string) protocol.SourceRef {
	ref.Metadata = metadata
	return ref
}

func withStatus(ref protocol.SourceRef, status string) protocol.SourceRef {
	ref.Status = status
	return ref
}

func withSignal(ref protocol.SourceRef, signal string) protocol.SourceRef {
	ref.Signal = signal
	return ref
}

func TestStoreRevisionIncrementsOnMutations(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	store.SetTasks([]protocol.Task{{Title: "one", Attention: "attention"}})
	if got := store.Revision(); got != 1 {
		t.Fatalf("revision after SetTasks = %d, want 1", got)
	}
	store.SetTasksForSources([]protocol.Task{{Title: "local", Attention: "in_progress", SourceRefs: []protocol.SourceRef{{ID: "git:worktree:/tmp/a", Source: "git", Kind: "worktree", Role: protocol.SourceRefRoleAuthoritative}}}}, []string{"git"})
	store.SetSources([]protocol.SourceStatus{{Name: "git", Status: "ok"}})
	store.Acknowledge("1")
	if got := store.Revision(); got != 4 {
		t.Fatalf("revision after mutations = %d, want 4", got)
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := store.Revision(); got != 5 {
		t.Fatalf("revision after Reset = %d, want 5", got)
	}
}

func TestWaitForRevisionReturnsImmediatelyWhenNewer(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Title: "one", Attention: "attention"}})

	if got := store.WaitForRevision(context.Background(), 0); got != 1 {
		t.Fatalf("WaitForRevision = %d, want 1", got)
	}
}

func TestWaitForRevisionUnblocksOnMutation(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan int64, 1)
	go func() {
		done <- store.WaitForRevision(context.Background(), 0)
	}()
	store.SetSources([]protocol.SourceStatus{{Name: "git", Status: "ok"}})

	select {
	case got := <-done:
		if got != 1 {
			t.Fatalf("WaitForRevision = %d, want 1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForRevision did not unblock")
	}
}

func TestLoadRejectsHugeStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxStateFileSize + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	store := &Store{path: path, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := store.Load(); err == nil {
		t.Fatal("Load() succeeded for huge state file, want error")
	}
}

func TestNewStoreRejectsMalformedStateAndDiscardsIncompatibleCache(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	malformedPath := filepath.Join(t.TempDir(), "malformed.json")
	t.Setenv("RADAR_STATE", malformedPath)
	if err := os.WriteFile(malformedPath, []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(logger); err == nil {
		t.Fatal("NewStore() error = nil for malformed state")
	}

	incompatiblePath := filepath.Join(t.TempDir(), "incompatible.json")
	t.Setenv("RADAR_STATE", incompatiblePath)
	if err := os.WriteFile(incompatiblePath, []byte(fmt.Sprintf(`{"version":%d,"task_records":[],"source_refs":[]}`, stateVersion-1)), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Tasks()) != 0 {
		t.Fatalf("tasks = %+v, want clean rebuildable cache", store.Tasks())
	}
}

func TestPrimaryLifecycleAuthorityOverridesContributingWorkItems(t *testing.T) {
	now := time.Now().UTC()
	primary := protocol.SourceRef{ID: "obsidian:task:1", Source: "obsidian", Kind: "task", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, CanonicalKey: "obsidian:task:1", LinkingKeys: []string{"shared"}, Signal: "low_priority", Title: "Authored task", Presentation: protocol.SourceRefPresentation{PreferTitle: true}}
	jira := protocol.SourceRef{ID: "jira:issue:RAD-1", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, CanonicalKey: "jira:issue:RAD-1", LinkingKeys: []string{"shared"}, Signal: "done", Title: "RAD-1"}
	tmux := protocol.SourceRef{ID: "tmux:session:1", Source: "tmux", Kind: "session", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, Authority: protocol.SourceRefAuthorityNone, LinkingKeys: []string{"shared"}, Signal: "in_progress"}

	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{
		makeTask("low_priority", "open", primary), makeTask("done", "Done", jira), makeTask("in_progress", "session", tmux),
	}, now)
	tasks := projectTasks(state)
	if len(tasks) != 1 || tasks[0].Attention != "in_progress" {
		t.Fatalf("open primary task = %+v", tasks)
	}

	primary.Signal = "done"
	primary.Status = "done"
	primary.Metadata = map[string]string{"completed_at": now.Add(-time.Hour).Format(time.RFC3339)}
	jira.Signal = "attention"
	state = reconcileState(state, []protocol.Task{
		makeTask("done", "done", primary), makeTask("attention", "Jira changed", jira), makeTask("in_progress", "session", tmux),
	}, now)
	tasks = projectTasks(state)
	if len(tasks) != 1 || tasks[0].Attention != "done" || tasks[0].DoneAt != primary.Metadata["completed_at"] {
		t.Fatalf("done primary task = %+v", tasks)
	}

	primary.Signal = "low_priority"
	primary.Status = "open"
	primary.Metadata = nil
	state = reconcileState(state, []protocol.Task{makeTask("low_priority", "open", primary), makeTask("done", "Done", jira)}, now.Add(time.Hour))
	if tasks = projectTasks(state); len(tasks) != 1 || tasks[0].Attention != "attention" {
		t.Fatalf("reopened primary task with actionable contributor = %+v", tasks)
	}
}
