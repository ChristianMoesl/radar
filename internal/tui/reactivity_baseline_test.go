package tui

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

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/server"
	"radar/internal/state"
	"radar/internal/taskservice"
)

// Keep this fixture synthetic and bounded: no live providers, terminal emulator,
// periodic collector, user state, or filesystem watcher is involved.
func reactivityBaselineModel() model {
	m := model{width: 120, height: 40}
	for i := 0; i < 200; i++ {
		path := fmt.Sprintf("/work/workspace-%03d", i)
		m.tasks = append(m.tasks, protocol.Task{
			ID: i + 1, Title: fmt.Sprintf("ABC-%03d Workspace", i), Attention: "in_progress",
			SourceRefs: []protocol.SourceRef{{
				ID: "workspace:" + path, Source: "workspace", Kind: "workspace", Path: path,
				Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace,
				Authority: protocol.SourceRefAuthorityNone, WorkspaceEntry: true, ProvidesWorkspace: true,
			}},
		})
	}
	m.rawTasks = m.tasks
	m.startOperation(m.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", nil)
	return m
}

// Measures Update and View separately, not OS keyboard-to-terminal latency.
func BenchmarkReactivityInput(b *testing.B) {
	for _, name := range []string{"navigation", "search", "inspect", "blocked-open"} {
		b.Run(name, func(b *testing.B) {
			base := reactivityBaselineModel()
			key := tea.KeyMsg{Type: tea.KeyDown}
			switch name {
			case "search":
				base.mode = "search"
				key = runeKey('W')
			case "inspect":
				key = runeKey('i')
			case "blocked-open":
				base.cursor = 1
				key = tea.KeyMsg{Type: tea.KeyEnter}
			}
			var update, render time.Duration
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				next, _ := base.Update(key)
				updated := time.Now()
				view := next.(model).View()
				finished := time.Now()
				if len(view) == 0 {
					b.Fatal("empty view")
				}
				update += updated.Sub(start)
				render += finished.Sub(updated)
			}
			b.ReportMetric(float64(update.Nanoseconds())/float64(b.N), "update-ns/op")
			b.ReportMetric(float64(render.Nanoseconds())/float64(b.N), "view-ns/op")
		})
	}
}

// The wrapper measures the actual provider Collect call without changing its
// result. Remaining refresh time includes grouping, locks and publication; it
// must not be labeled pure publication time.
type baselineWorkspaceSource struct {
	workspace.Source
	duration time.Duration
}

func (s *baselineWorkspaceSource) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	start := time.Now()
	result := s.Source.Collect(ctx, req)
	s.duration = time.Since(start)
	return result
}

type reactivityBaselineFixture struct {
	source  *baselineWorkspaceSource
	service *taskservice.Service
	store   *state.Store
	model   model
	anchor  string
	away    string
}

func newReactivityBaselineFixture(t testing.TB) *reactivityBaselineFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("RADAR_STATE", filepath.Join(root, "state.db"))
	configDir := filepath.Join(root, "config", "radar")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, "workspaces")
	config := fmt.Sprintf("workspace:\n  root_dir: %q\nlinking_mark_prefixes: [ABC]\n", workspaceRoot)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor := filepath.Join(workspaceRoot, "baseline")
	if err := os.MkdirAll(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "ABC-123 Baseline", Path: anchor}
	if err := workspacegroup.Save(workspaceRoot, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	source := &baselineWorkspaceSource{Source: workspace.NewSource(nil)}
	registry := integration.NewRegistry(source)
	service := taskservice.New(store, logger, registry)
	service.Refresh(context.Background(), true)

	// Short socket directory also works with long Go test names on macOS.
	socketDir, err := os.MkdirTemp("", "radar-reactivity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	path := filepath.Join(socketDir, "s")
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	s := server.New(store, logger, nil, nil, nil, registry, cleanup.New(nil)).
		SetResourcePublication(service.MarkResourceRefreshPending, service.ResourceSnapshot).
		SetOnReady(func(context.Context) { close(ready) })
	go func() { done <- s.ListenAndServe(ctx, path) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("baseline server did not stop")
		}
	})
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("baseline server did not listen")
	}
	m := model{socketPath: path, width: 120, height: 40}
	m.applyResponse(service.ResourceSnapshot(), false)
	return &reactivityBaselineFixture{source: source, service: service, store: store, model: m, anchor: anchor, away: filepath.Join(root, "away")}
}

func TestReactivityBaselineExternalWorkspace(t *testing.T) {
	f := newReactivityBaselineFixture(t)
	initial := f.service.ResourceSnapshot()
	if initial.ResourceRefreshPending || len(initial.Tasks) != 1 {
		t.Fatalf("unexpected healthy baseline: %+v", initial)
	}
	for _, missing := range []bool{true, false} {
		from, to := f.anchor, f.away
		if !missing {
			from, to = to, from
		}
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
		// Filesystem changes alone are not task-service events at this baseline.
		if f.store.Revision() != f.model.revision {
			t.Fatal("external rename unexpectedly published before collection")
		}
		f.service.Refresh(context.Background(), true)
		msg := f.model.watch(f.model.revision)().(watchMsg)
		if msg.err != nil || msg.response.Revision <= f.model.revision {
			t.Fatalf("watch did not deliver authoritative refresh: %+v", msg)
		}
		updated, _ := f.model.Update(msg)
		f.model = updated.(model)
		view := ansi.Strip(f.model.View())
		if strings.Contains(view, "workspace partial") != missing {
			t.Fatalf("existing source-health display did not follow directory presence: %s", view)
		}
		// Missing registration paths are diagnosed, not proof of successful
		// cleanup. The registered workspace reference deliberately survives.
		if len(f.model.tasks) != 1 || !f.model.tasks[0].SourceRefs[0].ProvidesWorkspace {
			t.Fatal("missing path was incorrectly treated as successful removal")
		}
		// This marker fences known Radar mutations, not every unhealthy
		// external observation. Source health remains independently visible.
		if msg.response.ResourceRefreshPending {
			t.Fatal("external path change unexpectedly invalidated the mutation epoch")
		}
		f.model.sourcesExpanded = true
		detail := ansi.Strip(f.model.sourceList(500))
		if strings.Contains(detail, "workspace anchor is missing") != missing {
			t.Fatalf("source diagnostic did not follow directory presence: %s", detail)
		}
		f.model.sourcesExpanded = false
	}
}

// Explicit-refresh collection/publication cost, not filesystem detection delay.
func BenchmarkReactivityExternalCollection(b *testing.B) {
	f := newReactivityBaselineFixture(b)
	var observation, overhead time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		from, to := f.anchor, f.away
		if i%2 != 0 {
			from, to = to, from
		}
		if err := os.Rename(from, to); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		start := time.Now()
		f.service.Refresh(context.Background(), true)
		elapsed := time.Since(start)
		observation += f.source.duration
		overhead += elapsed - f.source.duration
	}
	b.ReportMetric(float64(observation.Nanoseconds())/float64(b.N), "source-collect-ns/op")
	b.ReportMetric(float64(overhead.Nanoseconds())/float64(b.N), "service-overhead-ns/op")
}

// A watch issued against the pre-publication revision catches up through the
// real Unix socket, native snapshot, JSON client, Update and View. This is not
// a measurement of a parked long-poll, Bubble Tea scheduling or terminal paint.
func BenchmarkReactivityWatchRender(b *testing.B) {
	f := newReactivityBaselineFixture(b)
	base := reactivityBaselineModel()
	f.store.SetTasks(base.tasks)
	base.socketPath = f.model.socketPath
	var publication, watch, update, render time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		before := f.store.Revision()
		start := time.Now()
		f.service.MarkResourceRefreshPending()
		published := time.Now()
		msg := base.watch(before)().(watchMsg)
		delivered := time.Now()
		if msg.err != nil || !msg.response.ResourceRefreshPending || msg.response.Revision <= before {
			b.Fatalf("invalid pending snapshot: %+v", msg)
		}
		next, _ := base.Update(msg)
		updated := time.Now()
		view := next.(model).View()
		finished := time.Now()
		if !strings.Contains(view, "Resource list refresh pending") {
			b.Fatal("pending publication not rendered")
		}
		publication += published.Sub(start)
		watch += delivered.Sub(published)
		update += updated.Sub(delivered)
		render += finished.Sub(updated)
	}
	for name, duration := range map[string]time.Duration{"publication": publication, "watch": watch, "update": update, "view": render} {
		b.ReportMetric(float64(duration.Nanoseconds())/float64(b.N), name+"-ns/op")
	}
}
