package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"radar/internal/protocol"
)

func overviewAgent(id, title string, running, attention bool, problem string) protocol.SourceRef {
	return protocol.SourceRef{ID: "agent:instance:" + id, Source: "agent", Kind: "agent", AgentID: id, Title: title, Role: protocol.SourceRefRoleAuthoritative, Signal: "attention", Metadata: map[string]string{"running": fmt.Sprint(running), "attention": fmt.Sprint(attention), "summary": "Watching ABC-123 health checks", "runtime_error": problem}}
}
func overviewAgentTask(ref protocol.SourceRef) protocol.Task {
	return protocol.Task{ID: 100, Title: ref.Title, Attention: "attention", SourceRefs: []protocol.SourceRef{ref}}
}
func agentOverviewModel(refs ...protocol.SourceRef) model {
	m := newModel("unused.sock")
	m.width = 100
	m.height = 35
	m.loading = false
	tasks := []protocol.Task{}
	for i, ref := range refs {
		task := overviewAgentTask(ref)
		task.ID += i
		tasks = append(tasks, task)
	}
	m.applyResponse(protocol.Response{Tasks: tasks, Sources: []protocol.SourceStatus{{Name: "agent", Status: "ok"}}}, false)
	return m
}
func overviewKey(t *testing.T, m model, key string) model {
	t.Helper()
	msg := inspectKey(key)
	if key == "tab" {
		msg = tea.KeyMsg{Type: tea.KeyTab}
	}
	result, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatalf("%s unexpectedly dispatched a command", key)
	}
	return result.(model)
}

func TestWorkspaceAgentSeparationUsesOwnershipNotNamesAndKeepsFacts(t *testing.T) {
	agent := overviewAgent("watcher", "Health watcher", true, true, "")
	agent.Metadata["owned_workspace_id"] = "owned"
	agent.Metadata["owned_note_path"] = "/notes/owned.md"
	agent.Metadata["owned_note_key"] = "note:owned"
	anchor := protocol.SourceRef{ID: "workspace:owned", Source: "workspace", Kind: "workspace", WorkspaceID: "owned", Path: "/work/owned", Role: protocol.SourceRefRoleAuthoritative}
	note := protocol.SourceRef{ID: "note:owned", Source: "obsidian", Kind: "task", Title: "Agent backing note", Authored: true, WorkspaceAnchorPath: "/notes/owned.md", CanonicalKey: "note:owned", Role: protocol.SourceRefRoleAuthoritative, Signal: "low_priority"}
	backing := protocol.Task{ID: 2, Title: note.Title, Attention: "done", SourceRefs: []protocol.SourceRef{anchor, note, {ID: "git:owned", Source: "git", WorkspaceID: "owned", Role: protocol.SourceRefRoleAuthoritative}}}
	ordinary := protocol.Task{ID: 3, Title: "agent-watcher ordinary work", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "workspace:ordinary", Source: "workspace", Kind: "workspace", WorkspaceID: "ordinary", Path: "/work/agent-watcher", Role: protocol.SourceRefRoleAuthoritative}}}
	independent := protocol.Task{ID: 4, Title: agent.Title, Attention: "attention", SourceRefs: []protocol.SourceRef{agent, anchor, note, {ID: "remote:ABC-123", Source: "remote", Kind: "issue", Title: "Independent ABC-123", Role: protocol.SourceRefRoleAuthoritative, Signal: "low_priority", Status: "Open review"}}}
	interactive := protocol.Task{ID: 5, Title: "Interactive Pi", Attention: "in_progress", Activity: protocol.ActivityBusy, SourceRefs: []protocol.SourceRef{anchor, {ID: "pi:session:ordinary", Source: "pi", Kind: "session", Role: protocol.SourceRefRoleAuthoritative, Signal: "in_progress", Activity: protocol.ActivityBusy}}}
	raw := []protocol.Task{overviewAgentTask(agent), backing, ordinary, independent, interactive}
	before, _ := json.Marshal(raw)
	got := workspaceTasks(raw, []protocol.SourceRef{agent})
	if len(got) != 3 || got[0].ID != 3 || got[1].ID != 4 || got[2].ID != 5 {
		t.Fatalf("ordinary work hidden or backing work duplicated: %+v", got)
	}
	if got[1].Title != "Independent ABC-123" || got[1].Attention != "low_priority" || got[1].Reason != "Open review" {
		t.Fatalf("agent urgency/title leaked into independent work: %+v", got[1])
	}
	if !reflect.DeepEqual(got[1].SourceRefs, independent.SourceRefs) || got[2].Activity != protocol.ActivityBusy {
		t.Fatal("discarded lifecycle/resource/interactive facts")
	}
	after, _ := json.Marshal(raw)
	if string(before) != string(after) {
		t.Fatal("mutated served facts")
	}
	if matchesTaskSearch(got[1], []string{"health watcher"}) {
		t.Fatal("agent title leaked into workspace search")
	}
	if len(overviewSourceRefs(got[1])) != 1 {
		t.Fatal("durable source row remains in workspace overview")
	}
	// Without ownership proof, even the identical name, path and association
	// remain visible. Explicit binding is not ownership.
	delete(agent.Metadata, "owned_workspace_id")
	if len(workspaceTasks([]protocol.Task{backing, ordinary}, []protocol.SourceRef{agent})) != 2 {
		t.Fatal("inferred dedicated ownership")
	}
}

func TestAgentsOverviewCountsSortAndSearchAreIndependent(t *testing.T) {
	a := overviewAgent("alpha", "Zulu", true, true, "runtime failed")
	b := overviewAgent("beta", "Alpha", false, false, "")
	c := overviewAgent("gamma", "Alpha", false, false, "schedule failed")
	m := agentOverviewModel(b, a, c, a) // duplicate contribution, one registration row
	if len(m.agents.rows) != 3 || len(m.tasks) != 0 {
		t.Fatal("registration rows not separated/deduplicated")
	}
	if m.agents.rows[0].AgentID != "gamma" || m.agents.rows[1].AgentID != "alpha" {
		t.Fatal("attention/error ordering is wrong")
	}
	nav := ansi.Strip(m.overviewNavigation(100))
	if !strings.Contains(nav, "[Workspaces]") || !strings.Contains(nav, "Agents 3 · !2") {
		t.Fatalf("counts are not distinct/global: %s", nav)
	}
	m = overviewKey(t, m, "tab")
	m = overviewKey(t, m, "/")
	for _, r := range "BETA HEALTH" {
		result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = result.(model)
	}
	m = overviewKey(t, m, "enter")
	if len(m.visibleAgents()) != 1 || m.visibleAgents()[0].AgentID != "beta" {
		t.Fatal("search terms not matched across registration, title and summary")
	}
	if !strings.Contains(ansi.Strip(m.View()), "[Agents 3 · !2]") {
		t.Fatal("search changed global counts")
	}
	m = overviewKey(t, m, "tab")
	if m.activeView != "" || m.agents.query != "BETA HEALTH" {
		t.Fatal("view switch lost applied agent query")
	}
	m = overviewKey(t, m, "tab")
	m = overviewKey(t, m, "esc")
	if m.agents.query != "" || m.mode != "" {
		t.Fatal("Escape did not clear local query")
	}
}

func TestAgentsOverviewRestoresIndependentSelectionScrollAndQueries(t *testing.T) {
	var agents []protocol.SourceRef
	for i := 0; i < 35; i++ {
		agents = append(agents, overviewAgent(fmt.Sprintf("id-%02d", i), fmt.Sprintf("Agent %02d", i), i%2 == 0, false, ""))
	}
	m := agentOverviewModel(agents...)
	m.tasks = longTaskListFixture()
	m.searchQuery = "Task"
	m.cursor = 20
	m.syncTaskScroll()
	cursor, scroll := m.cursor, m.scroll
	m = overviewKey(t, m, "tab")
	m.agents.query = "Agent"
	m.syncAgentsSelection()
	for range 28 {
		m = overviewKey(t, m, "down")
	}
	selected, agentScroll := m.agents.selected, m.agents.scroll
	if agentScroll == 0 {
		t.Fatal("agents did not scroll")
	}
	m = overviewKey(t, m, "tab")
	if m.cursor != cursor || m.scroll != scroll || m.searchQuery != "Task" {
		t.Fatal("Workspaces context was overwritten")
	}
	m = overviewKey(t, m, "tab")
	if m.agents.selected != selected || m.agents.scroll != agentScroll || m.agents.query != "Agent" {
		t.Fatal("Agents context was overwritten")
	}
}

func TestAgentsTabDoesNotInterceptSearchDetailsOrModals(t *testing.T) {
	for _, mode := range []string{"search", "agent", "detail", "cleanup_confirm", "task_delete_confirm", "workspace_name", "create_repo", "task_authoring", "open_link", "worktree_session"} {
		t.Run(mode, func(t *testing.T) {
			m := agentOverviewModel(overviewAgent("one", "One", true, false, ""))
			m.activeView = "agents"
			m.mode = mode
			if mode == "agent" {
				m.agent.id = "one"
			}
			result, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = result.(model)
			if m.activeView != "agents" || m.mode != mode {
				t.Fatalf("Tab escaped modal %q", mode)
			}
		})
	}
}

func TestAgentsOverviewRefreshPinsIdentityAndRemovalDoesNotRetargetDetails(t *testing.T) {
	a, b, c := overviewAgent("a", "A", true, false, ""), overviewAgent("b", "B", false, false, ""), overviewAgent("c", "C", false, false, "")
	m := agentOverviewModel(a, b, c)
	m = overviewKey(t, m, "tab")
	m = overviewKey(t, m, "down")
	b.Metadata["attention"] = "true"
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{overviewAgentTask(c), overviewAgentTask(b), overviewAgentTask(a)}}, false)
	if m.agents.selected != "b" || m.agents.cursor != 0 || m.activeView != "agents" {
		t.Fatal("refresh reordered selection/focus")
	}
	result, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = result.(model)
	if cmd == nil || m.mode != "agent" || m.agent.id != "b" {
		t.Fatal("Enter did not reuse native agent details")
	}
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{overviewAgentTask(c), overviewAgentTask(a)}}, false)
	if m.mode != "agent" || m.agent.id != "b" || !m.agent.unavailable || len(m.agentChoices()) != 0 {
		t.Fatal("missing detail silently retargeted or still actionable")
	}
	if !strings.Contains(m.agentView(), "Agent no longer available") {
		t.Fatal("disappearance not reported")
	}
	m = overviewKey(t, m, "esc")
	if m.activeView != "agents" || m.agents.selected != "a" {
		t.Fatalf("nearest remaining row not selected: %+v", m.agents)
	}
}

func TestAgentsOverviewEmptyNoMatchesAndCollectionFailureRemainDistinct(t *testing.T) {
	m := agentOverviewModel()
	m = overviewKey(t, m, "tab")
	if !strings.Contains(ansi.Strip(m.View()), "No durable agents registered") || !strings.Contains(ansi.Strip(m.View()), "[Agents 0]") {
		t.Fatal("empty Agents is unreachable")
	}
	m = agentOverviewModel(overviewAgent("a", "A", false, false, ""))
	m.activeView = "agents"
	m.agents.query = "not-found"
	m.syncAgentsSelection()
	if !strings.Contains(ansi.Strip(m.View()), "No agents match your search") {
		t.Fatal("no matches conflated with no registrations")
	}
	m.agents.query = ""
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{}, Sources: []protocol.SourceStatus{{Name: "agent", Status: "error", Detail: "registration list unreadable"}}}, false)
	if len(m.agents.rows) != 1 || !strings.Contains(ansi.Strip(m.View()), "last-known") || !strings.Contains(ansi.Strip(m.overviewNavigation(100)), "?") {
		t.Fatal("failed collection discarded last-known facts")
	}
	m.activeView = ""
	if !strings.Contains(ansi.Strip(m.View()), "?") || !strings.Contains(ansi.Strip(m.View()), "Agents 1") {
		t.Fatal("error indicator absent from Workspaces")
	}
	m = agentOverviewModel()
	result, _ := m.Update(fetchMsg{err: errors.New("daemon unavailable")})
	m = result.(model)
	m.activeView = "agents"
	if strings.Contains(ansi.Strip(m.View()), "Agents 0") || !strings.Contains(ansi.Strip(m.View()), "Agent collection unavailable") {
		t.Fatal("initial transport failure asserted zero/healthy agents")
	}
}

func TestAgentsOverviewRuntimeAttentionAndShortcutsRemainSeparate(t *testing.T) {
	m := agentOverviewModel(overviewAgent("one", "One", true, true, "native runtime error"))
	m.activeView = "agents"
	view := ansi.Strip(m.View())
	for _, want := range []string{"Running", "Attention", "Error", "Watching ABC-123", "Tab: switch view"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %s", want, view)
		}
	}
	for _, bad := range []string{"Idle", "Busy", "Needs input", "mark done", "mute/unmute", "cleanup"} {
		if strings.Contains(view, bad) {
			t.Fatalf("unsupported agent-row fact/action %q", bad)
		}
	}
	for _, key := range []string{"d", "D", "m", "p", "x", "w"} {
		m = overviewKey(t, m, key)
		if m.mode != "" {
			t.Fatal("task shortcut affected agent row")
		}
	}
	if m.agents.rows[0].Metadata["attention"] != "true" {
		t.Fatal("viewing acknowledged attention")
	}
}

func TestAgentsCompactLayoutPreservesNavigationAndRuntimeBeforeSummary(t *testing.T) {
	t.Setenv("TMUX", "test")
	for _, width := range []int{32, 40, 60, 100, 160} {
		m := agentOverviewModel(overviewAgent("a", strings.Repeat("Long title", 20), true, true, "failure"))
		m.activeView = "agents"
		m.width = width
		m.height = 30
		view := ansi.Strip(m.View())
		assertNoWideLines(t, view, width)
		for _, want := range []string{"[Agents 1 · !1]", "Workspaces", "Tab: switch view", "Running", "Attention", "Error"} {
			if !strings.Contains(view, want) {
				t.Fatalf("width %d lost %q: %s", width, want, view)
			}
		}
		if lipgloss.Height(m.View()) != m.height {
			t.Fatalf("width %d height %d != %d", width, lipgloss.Height(m.View()), m.height)
		}
	}
}

func TestAgentsAssociatedWorkspaceUsesOriginalResourceDetailAndEditor(t *testing.T) {
	m := agentOverviewModel(overviewAgent("one", "One", true, false, ""))
	m.activeView = "agents"
	m.mode = "agent"
	anchor := protocol.SourceRef{ID: "workspace:owned", Source: "workspace", Kind: "workspace", WorkspaceID: "owned", Path: "/work/owned", Role: protocol.SourceRefRoleAuthoritative}
	task := protocol.Task{ID: 7, Title: "Original backing task", SourceRefs: []protocol.SourceRef{anchor}}
	m.rawTasks = append(m.rawTasks, task)
	m.agent = agentState{id: "one", status: protocol.AgentStatus{Workspace: protocol.AgentWorkspaceBinding{ID: "owned", Path: "/work/owned"}}}
	for i, choice := range m.agentChoices() {
		if choice.operation == "inspect_workspace" {
			m.agent.cursor = i
			break
		}
	}
	m = overviewKey(t, m, "enter")
	if m.mode != "detail" || m.detail.task.ID != 7 || m.detailReturn != "agent" {
		t.Fatal("associated workspace facts are inaccessible")
	}
	m = overviewKey(t, m, "esc")
	if m.mode != "agent" {
		t.Fatal("inspection lost agent parent context")
	}
	for i, choice := range m.agentChoices() {
		if choice.operation == "resources" {
			m.agent.cursor = i
			break
		}
	}
	result, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = result.(model)
	if cmd == nil || m.mode != "workspace_loading" || m.editor.task.ID != 7 {
		t.Fatal("existing reconciliation editor unreachable")
	}
	// Do not execute the resource command: this is navigation, not authorization
	// to provision live resources in a unit test.
}

func TestAgentsSourceOnlyFailureDoesNotTrustStaleOwnership(t *testing.T) {
	agent := overviewAgent("one", "One", false, false, "")
	agent.Metadata["owned_workspace_id"] = "owned"
	backing := protocol.Task{ID: 1, Title: "Workspace", SourceRefs: []protocol.SourceRef{{ID: "workspace:owned", Source: "workspace", Kind: "workspace", WorkspaceID: "owned", Role: protocol.SourceRefRoleAuthoritative}}}
	m := agentOverviewModel(agent)
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{overviewAgentTask(agent), backing}}, false)
	if len(m.tasks) != 0 {
		t.Fatal("proven backing-only task remains visible")
	}
	m.applyResponse(protocol.Response{Sources: []protocol.SourceStatus{{Name: "agent", Status: "error", Detail: "ownership snapshot unavailable"}}}, false)
	if len(m.tasks) != 1 || m.tasks[0].ID != 1 || len(m.agents.rows) != 1 {
		t.Fatal("stale ownership hid ordinary work or discarded last-known registration")
	}
	m.applyResponse(protocol.Response{Sources: []protocol.SourceStatus{{Name: "agent", Status: "ok"}}}, false)
	if len(m.tasks) != 0 {
		t.Fatal("healthy source did not restore presentation separation")
	}
}

func TestAgentsAttentionClearsFromEitherViewWithoutNavigation(t *testing.T) {
	a := overviewAgent("one", "One", true, true, "error")
	m := agentOverviewModel(a)
	for _, active := range []string{"", "agents"} {
		m.activeView = active
		m.agents.query = "no-match"
		a = overviewAgent("one", "One", false, false, "")
		m.applyResponse(protocol.Response{Tasks: []protocol.Task{overviewAgentTask(a)}}, false)
		if strings.Contains(ansi.Strip(m.overviewNavigation(100)), "!1") || m.activeView != active {
			t.Fatal("underlying error/attention clear changed view or left stale global count")
		}
	}
}

func TestRemovedAgentDoesNotRegainControlsOnCollectionFailure(t *testing.T) {
	m := agentOverviewModel(overviewAgent("one", "One", true, false, ""))
	m.activeView = "agents"
	m.mode = "agent"
	m.agent = agentState{id: "one"}
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{}}, false)
	m.applyResponse(protocol.Response{Sources: []protocol.SourceStatus{{Name: "agent", Status: "error", Detail: "unavailable"}}}, false)
	if !m.agent.unavailable || len(m.agentChoices()) != 0 {
		t.Fatal("collection failure revived a removed registration")
	}
}

func TestAssociatedWorkspaceInspectionKeepsAgentRefreshTimerAlive(t *testing.T) {
	m := newModel("unused.sock")
	m.mode = "detail"
	m.detailReturn = "agent"
	m.agent = agentState{id: "one", generation: 7}
	result, cmd := m.Update(agentTickMsg{id: "one", generation: 7})
	if cmd == nil || result.(model).agent.busy || result.(model).mode != "detail" {
		t.Fatal("resource inspection stopped refresh or inspected through a modal")
	}
}
