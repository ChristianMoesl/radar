package state

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar.nvim/internal/protocol"
)

func TestReconcileStateUsesTicketRecordForMultiplePullRequests(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{
		{Kind: "github_own_pr", Title: "CAP-7 first", Attention: "in_progress", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:1", Source: "github", Kind: "pull_request", Branch: "CAP-7-a"}}},
		{Kind: "github_own_pr", Title: "CAP-7 second", Attention: "in_progress", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:2", Source: "github", Kind: "pull_request", Branch: "CAP-7-b"}}},
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

func TestReconcileStateReopensDoneRecord(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{{Title: "CAP-7 ship", Attention: "done", DoneAt: now.Format(time.RFC3339), SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request", Branch: "CAP-7-ship"}}}}, now)
	state = reconcileState(state, []protocol.Task{{Title: "CAP-7 ship", Attention: "in_progress", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request", Branch: "CAP-7-ship"}}}}, now.Add(time.Hour))

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
		SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request", Metadata: map[string]string{
			"base_reason":               "open PR",
			"new_general_comments":      "1",
			"latest_general_comment_at": "2026-06-15T11:00:00Z",
		}}},
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
			{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request", Branch: "feature/CAP-7-ship"},
			{ID: "git:worktree:/old", Source: "git", Kind: "worktree", Path: "/old", Branch: "feature/CAP-7-ship"},
		},
	}}, now)

	state = reconcileStateForSources(state, []protocol.Task{{
		Title:      "feature/CAP-7-ship",
		Attention:  "in_progress",
		SourceRefs: []protocol.SourceRef{{ID: "git:worktree:/new", Source: "git", Kind: "worktree", Path: "/new", Branch: "feature/CAP-7-ship"}},
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
		{Title: "local", Attention: "in_progress", SourceRefs: []protocol.SourceRef{{ID: "git:worktree:/repo/local", Source: "git", Kind: "worktree", Path: "/repo/local"}}},
		{Title: "session", Attention: "in_progress", SourceRefs: []protocol.SourceRef{{ID: "tmux:session:$1", Source: "tmux", Kind: "session"}}},
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
