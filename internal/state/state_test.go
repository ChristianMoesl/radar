package state

import (
	"context"
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

func TestReconcileStateUsesTicketRecordForMultiplePullRequests(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{
		{Kind: "github_own_pr", Title: "CAP-7 first", Attention: "in_progress", SourceRefs: []protocol.SourceRef{testGitHubPRRef("github:pr:acme/app:1", "acme/app", "CAP-7-a")}},
		{Kind: "github_own_pr", Title: "CAP-7 second", Attention: "in_progress", SourceRefs: []protocol.SourceRef{testGitHubPRRef("github:pr:acme/app:2", "acme/app", "CAP-7-b")}},
	}, now)

	if len(state.Records) != 1 {
		t.Fatalf("records = %d, want one ticket record: %+v", len(state.Records), state.Records)
	}
	if state.Records[0].CanonicalKey != "ticket:CAP-7" {
		t.Fatalf("canonical key = %q, want ticket:CAP-7", state.Records[0].CanonicalKey)
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
		ID:           id,
		Source:       "github",
		Kind:         "pull_request",
		Repo:         repo,
		Branch:       branch,
		CanonicalKey: id,
		LinkingKeys:  linking.Keys(append(linking.TicketKeys(id, repo, branch), id, linking.BranchKey(repo, testBranchKey(branch)))...),
	}
}

func testGitWorktreeRef(id string, path string, repo string, branch string) protocol.SourceRef {
	canonicalKey := linking.WorkspaceKey(path)
	return protocol.SourceRef{
		ID:           id,
		Source:       "git",
		Kind:         "worktree",
		Repo:         repo,
		Path:         path,
		Branch:       branch,
		CanonicalKey: canonicalKey,
		LinkingKeys:  linking.Keys(append(linking.TicketKeys(id, path, repo, branch), canonicalKey, linking.BranchKey(repo, testBranchKey(branch)))...),
	}
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
		Path:        path,
		LinkingKeys: linking.Keys(append(linking.TicketKeys(id, path), linking.WorkspaceKey(path))...),
	}
}

func withMetadata(ref protocol.SourceRef, metadata map[string]string) protocol.SourceRef {
	ref.Metadata = metadata
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
	store.SetLocalTasks([]protocol.Task{{Title: "local", Attention: "in_progress", SourceRefs: []protocol.SourceRef{{ID: "git:worktree:/tmp/a", Source: "git", Kind: "worktree"}}}})
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
