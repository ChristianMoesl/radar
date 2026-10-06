package workspacegc

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestExpiryAt(t *testing.T) {
	done := time.Date(2026, 1, 2, 10, 20, 30, 0, time.FixedZone("offset", 3600))
	for _, tc := range []struct {
		name, attention, doneAt, workspaceID, path string
		provides, want                             bool
	}{
		{"registered", "done", done.Format(time.RFC3339), "workspace", "/workspaces/one", true, true},
		{"active", "in_progress", done.Format(time.RFC3339), "workspace", "/workspaces/one", true, false},
		{"reopened", "attention", "", "workspace", "/workspaces/one", true, false},
		{"missing date", "done", "", "workspace", "/workspaces/one", true, false},
		{"malformed date", "done", "yesterday", "workspace", "/workspaces/one", true, false},
		{"zero date", "done", time.Time{}.Format(time.RFC3339), "workspace", "/workspaces/one", true, false},
		{"standalone", "done", done.Format(time.RFC3339), "", "/workspaces/one", true, false},
		{"runtime only", "done", done.Format(time.RFC3339), "workspace", "/workspaces/one", false, false},
		{"missing path", "done", done.Format(time.RFC3339), "workspace", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := protocol.Task{Attention: tc.attention, DoneAt: tc.doneAt, SourceRefs: []protocol.SourceRef{{WorkspaceID: tc.workspaceID, Path: tc.path, ProvidesWorkspace: tc.provides}}}
			got, ok := ExpiryAt(task)
			if ok != tc.want || (ok && !got.Equal(done.Add(8*24*time.Hour))) {
				t.Fatalf("ExpiryAt = %s, %v", got, ok)
			}
		})
	}
}

type expiryProvider struct {
	gcProvider
	previewHook func()
	previewErr  error
	hardBlock   bool
	modes       *[]integration.CleanupMode
}

func (p expiryProvider) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if p.previewHook != nil {
		p.previewHook()
	}
	if p.previewErr != nil {
		return nil, p.previewErr
	}
	targets, err := p.gcProvider.PreviewCleanup(ctx, req)
	for i := range targets {
		if p.hardBlock {
			targets[i].Safety = append(targets[i].Safety, protocol.CleanupSafety{Kind: "unknown_structural_check", Message: "unsafe target", BlocksAutomatic: true})
		}
	}
	return targets, err
}

func (p expiryProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	if p.modes != nil {
		*p.modes = append(*p.modes, req.Mode)
	}
	return p.gcProvider.Cleanup(ctx, req)
}

func expiryFixture(t *testing.T) (*state.Store, string, time.Time, []protocol.Task) {
	t.Helper()
	store := testStore(t)
	root := t.TempDir()
	anchor := filepath.Join(root, "completed")
	member := worktreeRef(filepath.Join(anchor, "repo--work"), "acme/app", "work")
	member.WorkspaceID, member.ProvidesWorkspace = "registered", false
	session := attachedSessionRef(anchor, "session")
	tasks := []protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "work")),
		makeTask("in_progress", "workspace", workspaceAnchorRef(anchor, "registered")),
		makeTask("in_progress", "worktree", member),
		makeTask("in_progress", "session", session),
	}
	tasks[0].SourceRefs[0].Authority = protocol.SourceRefAuthorityPrimary
	tasks[0].SourceRefs[0].Metadata = map[string]string{"completed_at": "2026-01-02T10:20:30Z"}
	store.SetTasks(tasks)
	records := store.Records()
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	done, err := time.Parse(time.RFC3339, records[0].DoneAt)
	if err != nil {
		t.Fatal(err)
	}
	return store, root, done, tasks
}

func TestExpiryHasIndependentEightDayBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		age    time.Duration
		manual bool
		want   bool
	}{
		{"automatic before day one", 23 * time.Hour, false, false},
		{"automatic day one", 24 * time.Hour, false, false},
		{"manual early", time.Hour, true, false},
		{"automatic before expiry", ExpiryRetention - time.Nanosecond, false, false},
		{"manual before expiry", ExpiryRetention - time.Nanosecond, true, false},
		{"automatic exact expiry", ExpiryRetention, false, true},
		{"manual exact expiry", ExpiryRetention, true, true},
		{"automatic long expired", 40 * 24 * time.Hour, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, root, done, _ := expiryFixture(t)
			var calls []cleanupCall
			var modes []integration.CleanupMode
			providers := []integration.CleanupProvider{
				expiryProvider{gcProvider: gcProvider{name: "tmux", calls: &calls}, modes: &modes},
				expiryProvider{gcProvider: gcProvider{name: "git", calls: &calls, dirty: true, unpublished: true, publicationUnknown: true}, modes: &modes},
				expiryProvider{gcProvider: gcProvider{name: "workspace", calls: &calls}, modes: &modes},
			}
			result, err := Run(context.Background(), store, cleanup.New(providers), nil, done.Add(tc.age), Options{WorkspaceRoot: root, IgnoreRetention: tc.manual})
			if err != nil || (len(result.Deleted) == 1) != tc.want {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if !tc.want && len(calls) != 0 {
				t.Fatalf("blocked bundle partially removed: %+v", calls)
			}
			if tc.want {
				if len(calls) != 3 || calls[0].source != "tmux" || calls[1].source != "git" || calls[2].source != "workspace" {
					t.Fatalf("cleanup order = %+v", calls)
				}
				for _, mode := range modes {
					if mode != integration.CleanupExpired {
						t.Fatalf("cleanup mode = %v", mode)
					}
				}
			}
		})
	}
}

func TestExpiryNeverOverridesStructuralFailures(t *testing.T) {
	for _, failure := range []string{"hard safety", "inspection error", "missing workspace target"} {
		t.Run(failure, func(t *testing.T) {
			store, root, done, _ := expiryFixture(t)
			var calls []cleanupCall
			provider := expiryProvider{gcProvider: gcProvider{name: "git", calls: &calls, dirty: true}}
			switch failure {
			case "hard safety":
				provider.hardBlock = true
			case "inspection error":
				provider.previewErr = errors.New("cannot inspect registration")
			}
			providers := []integration.CleanupProvider{gcProvider{name: "tmux", calls: &calls}, provider}
			if failure != "missing workspace target" {
				providers = append(providers, gcProvider{name: "workspace", calls: &calls})
			}
			result, err := Run(context.Background(), store, cleanup.New(providers), nil, done.Add(ExpiryRetention), Options{WorkspaceRoot: root})
			if err != nil || len(result.Deleted) != 0 || len(result.Skipped) != 1 || len(calls) != 0 {
				t.Fatalf("unsafe removal: result=%+v err=%v calls=%+v", result, err, calls)
			}
		})
	}
}

func TestReopenDuringPreviewCancelsExpiry(t *testing.T) {
	for _, recomplete := range []bool{false, true} {
		t.Run(map[bool]string{false: "reopen", true: "recomplete"}[recomplete], func(t *testing.T) {
			store, root, done, tasks := expiryFixture(t)
			var calls []cleanupCall
			provider := expiryProvider{gcProvider: gcProvider{name: "workspace", calls: &calls}, previewHook: func() {
				tasks[0].Attention = "in_progress"
				tasks[0].SourceRefs[0].Signal = "in_progress"
				store.SetTasks(tasks)
				if recomplete {
					tasks[0].Attention = "done"
					tasks[0].SourceRefs[0].Signal = "done"
					tasks[0].SourceRefs[0].Metadata["completed_at"] = done.Add(7 * 24 * time.Hour).Format(time.RFC3339)
					store.SetTasks(tasks)
				}
			}}
			guarded := false
			result, err := Run(context.Background(), store, cleanup.New([]integration.CleanupProvider{provider}), nil, done.Add(ExpiryRetention), Options{WorkspaceRoot: root, GuardExecution: func(_ context.Context, _ protocol.Task, execute func() error) error {
				guarded = true
				return execute()
			}})
			if err != nil || !guarded || len(result.Deleted) != 0 || len(result.Skipped) != 1 || len(calls) != 0 || !strings.Contains(result.Skipped[0].Reason, "completion changed") {
				t.Fatalf("reopened task removed: result=%+v err=%v calls=%+v", result, err, calls)
			}
		})
	}
}

func TestStandaloneWorktreeNeverExpires(t *testing.T) {
	store := testStore(t)
	root := t.TempDir()
	store.SetTasks([]protocol.Task{
		makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "work")),
		makeTask("in_progress", "worktree", worktreeRef(filepath.Join(root, "observed"), "acme/app", "work")),
	})
	var calls []cleanupCall
	result, err := Run(context.Background(), store, cleanup.New([]integration.CleanupProvider{gcProvider{name: "git", dirty: true, calls: &calls}}), nil, time.Now().Add(90*24*time.Hour), Options{WorkspaceRoot: root, IgnoreRetention: true})
	if err != nil || len(result.Deleted) != 0 || len(result.Skipped) != 1 || len(calls) != 0 {
		t.Fatalf("standalone workspace expired: %+v, %v", result, err)
	}
}
