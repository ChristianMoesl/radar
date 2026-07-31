package state

import (
	"strconv"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestInformationalJiraReferenceDoesNotAffectManualTask(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	state := persistedState{Version: stateVersion, NextTaskID: 1, Records: []TaskRecord{{
		ID: "task:1", NumericID: 1, Kind: "manual", State: "done", DoneAt: now.Format(time.RFC3339),
		Intent:   &ManualIntent{Title: "Review RAD-7 rollout", ManuallyComplete: true},
		Snapshot: protocol.Task{Kind: "manual", Title: "Review RAD-7 rollout", Attention: "low_priority", Reason: "manual task"},
	}}}

	state = reconcileState(state, []protocol.Task{informationalJiraTask(1, "RAD-7", "RAD-7 Remote title", "Done")}, now.Add(time.Hour))
	tasks := projectTasks(state)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v, want manual task", tasks)
	}
	got := tasks[0]
	if got.ID != 1 || got.Title != "Review RAD-7 rollout" || got.Attention != "done" {
		t.Fatalf("informational reference changed task: %+v", got)
	}
	if got.Metadata["manual_lifecycle_available"] != "true" || len(got.SourceRefs) != 1 {
		t.Fatalf("manual lifecycle or refs = %+v", got)
	}
	if state.Records[0].CanonicalKey != "" {
		t.Fatalf("canonical key = %q, want empty", state.Records[0].CanonicalKey)
	}
}

func TestInformationalJiraReferenceUsesPerTaskIdentity(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	state := persistedState{Version: stateVersion, NextTaskID: 2, Records: []TaskRecord{
		{ID: "task:1", NumericID: 1, Kind: "manual", State: "active", Intent: &ManualIntent{Title: "First RAD-7 mention"}, Snapshot: protocol.Task{Title: "First RAD-7 mention", Attention: "low_priority"}},
		{ID: "task:2", NumericID: 2, Kind: "manual", State: "active", Intent: &ManualIntent{Title: "Second RAD-7 mention"}, Snapshot: protocol.Task{Title: "Second RAD-7 mention", Attention: "low_priority"}},
	}}

	state = reconcileState(state, []protocol.Task{
		informationalJiraTask(1, "RAD-7", "RAD-7 Remote", "Open"),
		informationalJiraTask(2, "RAD-7", "RAD-7 Remote", "Open"),
	}, now)
	if len(state.Records) != 2 {
		t.Fatalf("records = %+v, informational refs merged tasks", state.Records)
	}
	tasks := projectTasks(state)
	if len(tasks) != 2 || tasks[0].SourceRefs[0].ID == tasks[1].SourceRefs[0].ID {
		t.Fatalf("tasks = %+v, want independent mention refs", tasks)
	}
}

func TestInformationalReferenceSurvivesDaemonStateReload(t *testing.T) {
	store := newManualTestStore(t)
	manual, err := store.CreateManualTask("Review RAD-7 rollout")
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{informationalJiraTask(manual.ID, "RAD-7", "RAD-7 Remote", "Open")})

	reloaded, err := NewStore(store.logger)
	if err != nil {
		t.Fatal(err)
	}
	tasks := reloaded.Tasks()
	if len(tasks) != 1 || tasks[0].ID != manual.ID || len(tasks[0].SourceRefs) != 1 || tasks[0].SourceRefs[0].Role != protocol.SourceRefRoleInformational {
		t.Fatalf("reloaded tasks = %+v", tasks)
	}
}

func TestRemovingTitleReferenceRetiresInformationalRef(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	state := persistedState{Version: stateVersion, NextTaskID: 1, Records: []TaskRecord{{
		ID: "task:1", NumericID: 1, Kind: "manual", State: "active", Intent: &ManualIntent{Title: "Review rollout"},
		Snapshot: protocol.Task{Kind: "manual", Title: "Review rollout", Attention: "low_priority"},
	}}}
	state = reconcileState(state, []protocol.Task{informationalJiraTask(1, "RAD-7", "RAD-7 Remote", "Open")}, now)
	state = reconcileState(state, nil, now.Add(time.Hour))

	tasks := projectTasks(state)
	if len(tasks) != 1 || len(tasks[0].SourceRefs) != 0 || tasks[0].ID != 1 {
		t.Fatalf("tasks = %+v, want stable manual task without removed reference", tasks)
	}
}

func TestAuthoritativeTitleDiscoveryTargetsAndMergesManualTask(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	state := persistedState{Version: stateVersion, NextTaskID: 1, Records: []TaskRecord{{
		ID: "task:1", NumericID: 1, Kind: "manual", State: "active",
		Intent:   &ManualIntent{Title: "Investigate RAD-7 rollout"},
		Snapshot: protocol.Task{Kind: "manual", Title: "Investigate RAD-7 rollout", Attention: "low_priority"},
	}}}

	state = reconcileState(state, []protocol.Task{authoritativeJiraTask(1, "RAD-7", "RAD-7 Remote title", "In Progress", "in_progress")}, now)
	tasks := projectTasks(state)
	if len(tasks) != 1 || tasks[0].ID != 1 || tasks[0].Title != "RAD-7 Remote title" || tasks[0].Attention != "in_progress" {
		t.Fatalf("tasks = %+v, want authoritative projection on manual ID", tasks)
	}
	if state.Records[0].CanonicalKey != "ticket:RAD-7" || tasks[0].Metadata["manual_lifecycle_available"] != "false" {
		t.Fatalf("record/task authority = %+v / %+v", state.Records[0], tasks[0])
	}
}

func TestMultipleAuthoritativeJiraReferencesUseTitleOrderAndAllControlCompletion(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	first := authoritativeJiraTask(1, "RAD-2", "RAD-2 First title", "Done", "done")
	firstOrder := 0
	first.SourceRefs[0].Presentation.TitleOrder = &firstOrder
	second := authoritativeJiraTask(1, "RAD-1", "RAD-1 Second title", "In Progress", "in_progress")
	secondOrder := 1
	second.SourceRefs[0].Presentation.TitleOrder = &secondOrder
	state := persistedState{Version: stateVersion, NextTaskID: 1, Records: []TaskRecord{{
		ID: "task:1", NumericID: 1, Kind: "manual", State: "active", Intent: &ManualIntent{Title: "RAD-2 then RAD-1"},
		Snapshot: protocol.Task{Kind: "manual", Title: "RAD-2 then RAD-1", Attention: "low_priority"},
	}}}

	state = reconcileState(state, []protocol.Task{first, second}, now)
	tasks := projectTasks(state)
	if len(tasks) != 1 || tasks[0].Title != "RAD-2 First title" || tasks[0].Attention != "in_progress" {
		t.Fatalf("tasks = %+v, want first Jira title and incomplete lifecycle", tasks)
	}
	second.SourceRefs[0].Status = "Done"
	second.SourceRefs[0].Signal = "done"
	second.Attention = "done"
	state = reconcileState(state, []protocol.Task{first, second}, now.Add(time.Hour))
	if tasks = projectTasks(state); len(tasks) != 1 || tasks[0].Attention != "done" {
		t.Fatalf("tasks = %+v, want done after every authoritative ref completes", tasks)
	}
}

func TestJiraReferenceDemotionRemovesDerivedAuthority(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	state := persistedState{Version: stateVersion, NextTaskID: 1, Records: []TaskRecord{{
		ID: "task:1", NumericID: 1, Kind: "manual", State: "active",
		Intent:   &ManualIntent{Title: "Investigate RAD-7 rollout"},
		Snapshot: protocol.Task{Kind: "manual", Title: "Investigate RAD-7 rollout", Attention: "low_priority"},
	}}}
	state = reconcileState(state, []protocol.Task{authoritativeJiraTask(1, "RAD-7", "RAD-7 Remote title", "Done", "done")}, now)
	if tasks := projectTasks(state); len(tasks) != 1 || tasks[0].Attention != "done" {
		t.Fatalf("authoritative Jira did not complete task: %+v", tasks)
	}
	state = reconcileState(state, []protocol.Task{informationalJiraTask(1, "RAD-7", "RAD-7 Remote title", "Done")}, now.Add(time.Hour))

	tasks := projectTasks(state)
	if len(tasks) != 1 || tasks[0].Title != "Investigate RAD-7 rollout" || tasks[0].Attention != "low_priority" {
		t.Fatalf("tasks after demotion = %+v", tasks)
	}
	if state.Records[0].CanonicalKey != "" || tasks[0].Metadata["manual_lifecycle_available"] != "true" {
		t.Fatalf("stale authority after demotion: record=%+v task=%+v", state.Records[0], tasks[0])
	}
}

func informationalJiraTask(target int, key, title, status string) protocol.Task {
	return protocol.Task{TargetTaskID: target, SourceRefs: []protocol.SourceRef{{
		ID: "jira:mention:" + strconv.Itoa(target) + ":" + key, EntityID: "jira:issue:" + key, Source: "jira", Kind: "issue",
		Role: protocol.SourceRefRoleInformational, Title: title, URL: "https://jira.example.test/browse/" + key,
		Status: status, Metadata: map[string]string{"key": key, "issue_type": "Epic", "status_category": "done"},
	}}}
}

func authoritativeJiraTask(target int, key, title, status, signal string) protocol.Task {
	return protocol.Task{TargetTaskID: target, Kind: "jira_issue", Title: title, Attention: signal, Reason: status, SourceRefs: []protocol.SourceRef{{
		ID: "jira:issue:" + key, EntityID: "jira:issue:" + key, Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative,
		Lifecycle: protocol.SourceRefLifecycleWorkItem, Presentation: protocol.SourceRefPresentation{PreferTitle: true},
		Title: title, URL: "https://jira.example.test/browse/" + key, Status: status, Signal: signal,
		CanonicalKey: "jira:issue:" + key, LinkingKeys: []string{"ticket:" + key}, Metadata: map[string]string{"key": key, "issue_type": "Task"},
	}}}
}
