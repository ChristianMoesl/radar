package taskservice

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacegc"
)

type cleanupOtherAuthor struct{ *testAuthor }

func (a cleanupOtherAuthor) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "other-author"}
}

func cleanupAuthorRegistry(f *fixture, mode string) integration.Registry {
	switch mode {
	case "missing":
		return integration.NewRegistry()
	case "ambiguous":
		return integration.NewRegistry(f.author, f.author)
	case "different":
		return integration.NewRegistry(cleanupOtherAuthor{f.author})
	default:
		return integration.NewRegistry(f.author)
	}
}

func TestCleanupGuardRequiresRelevantAuthorCapability(t *testing.T) {
	for _, mode := range []string{"missing", "ambiguous", "different", "current"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			ref, ok := authoredRef(done, "obsidian")
			if !ok || !ref.Authored {
				t.Fatal("fixture has no relevant authored completion")
			}
			snapshot := f.store.DashboardSnapshot()
			collections := f.author.calls.Load()
			f.service = New(f.store, f.service.logger, cleanupAuthorRegistry(f, mode))
			calls := 0
			callbackError := errors.New("native execution error")
			err := f.service.GuardCleanup(context.Background(), done, func() error { calls++; return callbackError })
			if mode == "current" {
				if !errors.Is(err, callbackError) || calls != 1 || f.author.calls.Load() != collections+1 {
					t.Fatalf("valid author/callback changed: %v calls=%d", err, calls)
				}
			} else if err == nil || calls != 0 || f.author.calls.Load() != collections || !strings.Contains(err.Error(), "authoring integration") {
				t.Fatalf("unverifiable authored completion admitted: mode=%s err=%v calls=%d collections=%d", mode, err, calls, f.author.calls.Load()-collections)
			}
			if !reflect.DeepEqual(snapshot, f.store.DashboardSnapshot()) {
				t.Fatal("guard changed or published cached state")
			}
			if !f.service.applyMu.TryLock() {
				t.Fatal("guard leaked lifecycle admission")
			}
			f.service.applyMu.Unlock()
			// Capability recovery never replays the rejected callback. A separately
			// invoked native guard still collects fresh completion and executes once.
			f.service.integrations = integration.NewRegistry(f.author)
			if err := f.service.GuardCleanup(context.Background(), done, func() error { calls++; return nil }); err != nil || calls != map[bool]int{true: 2, false: 1}[mode == "current"] {
				t.Fatalf("fresh guard failed after capability recovery: %v calls=%d", err, calls)
			}
		})
	}
}

func TestCleanupGuardMissingAuthorDoesNotBlockUnrelatedTasks(t *testing.T) {
	for _, mode := range []string{"missing", "ambiguous", "different", "current"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.author.collectError = errors.New("unrelated author unreadable")
			f.service.integrations = cleanupAuthorRegistry(f, mode)
			collections := f.author.calls.Load()
			calls := 0
			task := protocol.Task{Attention: "done", SourceRefs: []protocol.SourceRef{{Source: "remote", Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done"}}}
			if err := f.service.GuardCleanup(context.Background(), task, func() error { calls++; return nil }); err != nil || calls != 1 || f.author.calls.Load() != collections {
				t.Fatalf("unrelated author became mandatory: %v calls=%d", err, calls)
			}
		})
	}
}

func TestGCStagesRefuseLostRelevantAuthorCapabilityAfterPreview(t *testing.T) {
	for _, age := range []time.Duration{24 * time.Hour, 7 * 24 * time.Hour} {
		for _, mode := range []string{"missing", "ambiguous", "different", "current"} {
			t.Run(age.String()+"/"+mode, func(t *testing.T) {
				f := newFixture(t)
				root := t.TempDir()
				path := filepath.Join(root, "completed")
				t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
				ref := protocol.SourceRef{ID: "workspace-test", Source: "workspace", Kind: "workspace", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace, Authority: protocol.SourceRefAuthorityNone, Path: path, ProvidesWorkspace: true, WorkspaceID: "workspace-test", LinkingKeys: []string{f.task.SourceRefs[0].ID}}
				f.store.SetTasksForSources([]protocol.Task{{Attention: "in_progress", SourceRefs: []protocol.SourceRef{ref}}}, []string{"workspace"})
				done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
				doneAt, err := time.Parse(time.RFC3339, done.DoneAt)
				if err != nil {
					t.Fatal(err)
				}
				snapshot := f.store.DashboardSnapshot()
				collections := f.author.calls.Load()
				previews := 0
				provider := &handoffCleanup{path: path, beforeExecute: func() { previews++; f.service.integrations = cleanupAuthorRegistry(f, mode) }}
				result, err := workspacegc.Run(context.Background(), f.store, cleanup.New([]integration.CleanupProvider{provider}), f.service.logger, doneAt.Add(age), workspacegc.Options{WorkspaceRoot: root, GuardExecution: f.service.GuardCleanup})
				if err != nil || previews != 1 {
					t.Fatalf("native planning changed: %+v %v previews=%d", result, err, previews)
				}
				if mode == "current" {
					// The fixture reports no runtime change. Retirement can dispatch without
					// adding Retired, unlike expiry; do not turn dispatch into resource proof.
					if !provider.executed || len(result.Skipped) != 0 || f.author.calls.Load() != collections+1 || len(result.Deleted) != map[bool]int{true: 1, false: 0}[age == 7*24*time.Hour] {
						t.Fatalf("valid native control: %+v executed=%v", result, provider.executed)
					}
				} else if provider.executed || len(result.Retired) != 0 || len(result.Deleted) != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "authoring integration") || f.author.calls.Load() != collections {
					t.Fatalf("lost capability authorized native GC: mode=%s result=%+v executed=%v collections=%d", mode, result, provider.executed, f.author.calls.Load()-collections)
				}
				if !reflect.DeepEqual(snapshot, f.store.DashboardSnapshot()) {
					t.Fatal("GC guard collected or changed cached state")
				}
				if !f.service.applyMu.TryLock() {
					t.Fatal("GC refusal leaked admission")
				}
				f.service.applyMu.Unlock()
			})
		}
	}
}
