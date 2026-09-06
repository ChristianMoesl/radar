package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func editorModel() model {
	member := integration.DesiredWorkspaceWorktree{Repository: "/repos/app", BranchMode: integration.WorkspaceBranchExisting, Branch: "feature"}
	desired := integration.DesiredWorkspaceDescription{Worktrees: []integration.DesiredWorkspaceWorktree{member}, Note: &integration.DesiredWorkspaceNote{Path: "/vault/Tasks/Plan/Plan.md", LinkingKey: "obsidian:task:one"}, Sandbox: &integration.DesiredWorkspaceSandbox{Ports: []integration.SandboxPort{{HostPort: 3000, SandboxPort: 3000}}}}
	return model{mode: "workspace_edit", editor: workspaceEditor{active: true, state: integration.WorkspaceState{Path: "/work/plan", Name: "Plan", Revision: "original", Desired: desired, Members: []integration.WorkspaceStateMember{{Repository: member.Repository, Branch: member.Branch, Path: "/work/plan/app--feature"}}}, desired: desired}}
}

func TestWorkspaceKeyInspectsSelectedTask(t *testing.T) {
	m := model{tasks: []protocol.Task{{ID: 7, Title: "Plan"}}}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	got := updated.(model)
	if cmd == nil || got.mode != "workspace_loading" || got.editor.task.ID != 7 {
		t.Fatalf("editor = %+v", got.editor)
	}
}

func TestWorkspaceCreateStartsWithNameNotRepository(t *testing.T) {
	updated, cmd := (model{}).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	got := updated.(model)
	if cmd != nil || got.mode != "workspace_name" {
		t.Fatalf("mode = %q", got.mode)
	}
	got.editor.create.Name = "Plan"
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(model)
	if cmd != nil || got.mode != "workspace_edit" || len(got.editor.desired.Worktrees) != 0 {
		t.Fatal("name entry created resources")
	}
}

func TestWorkspaceRemovalIsStagedAndPreservesOtherResources(t *testing.T) {
	m := editorModel()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	got := updated.(model)
	if cmd != nil || len(got.editor.desired.Worktrees) != 0 || got.editor.desired.Note == nil || len(got.editor.desired.Sandbox.Ports) != 1 {
		t.Fatalf("draft = %+v", got.editor.desired)
	}
	if len(got.editor.state.Desired.Worktrees) != 1 {
		t.Fatal("removal mutated inspected state")
	}
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || updated.(model).editor.active {
		t.Fatal("cancel applied a mutation")
	}
}

func TestWorkspaceDirtyRemovalIsBlocked(t *testing.T) {
	m := editorModel()
	m.editor.state.Members[0].Dirty = true
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	got := updated.(model)
	if cmd != nil || got.err == nil || len(got.editor.desired.Worktrees) != 1 {
		t.Fatal("dirty removal was staged")
	}
}

func TestWorkspaceRepositoryPickerAddsToDraft(t *testing.T) {
	m := editorModel()
	m.mode = "create_name"
	m.create = createForm{repo: "/repos/api", branchMode: integration.WorkspaceBranchNew, name: "ABC-123", base: "origin/main"}
	updated, cmd := m.submitCreate()
	got := updated.(model)
	if cmd != nil || got.mode != "workspace_edit" || len(got.editor.desired.Worktrees) != 2 {
		t.Fatal("repository selection did not return to draft")
	}
	if got.editor.desired.Worktrees[1].Name != "ABC-123" || got.editor.state.Name != "Plan" {
		t.Fatal("branch naming changed workspace name")
	}
	got.mode = "create_repo"
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || updated.(model).mode != "workspace_edit" {
		t.Fatal("picker cancel discarded workspace")
	}
}

func TestWorkspaceNoteCannotBeDetached(t *testing.T) {
	m := editorModel()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd != nil || updated.(model).editor.desired.Note == nil {
		t.Fatal("n changed attached note")
	}
	if strings.Contains(strings.ToLower(m.View()), "detach") {
		t.Fatal("editor advertises note detachment")
	}
}

func TestWorkspacePreviewRequiresConfirmation(t *testing.T) {
	m := editorModel()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(model)
	if cmd == nil || got.mode != "workspace_loading" {
		t.Fatal("review did not request a preview")
	}
	plan := integration.WorkspaceReconcilePlan{PlanID: "confirmed", Changes: []integration.WorkspaceChange{{Summary: "remove app worktree"}}, Warnings: []string{"unpublished branch"}}
	updated, cmd = got.Update(workspacePlanMsg{plan: plan})
	got = updated.(model)
	if cmd != nil || got.mode != "workspace_confirm" || !strings.Contains(got.View(), "unpublished branch") {
		t.Fatal("preview did not show confirmation")
	}
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || updated.(model).mode != "workspace_edit" {
		t.Fatal("cancelled confirmation applied")
	}
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || updated.(model).mode != "workspace_applying" {
		t.Fatal("confirmation did not apply")
	}
}

func TestWorkspaceChangedPlanRequiresAnotherConfirmation(t *testing.T) {
	m := editorModel()
	m.mode = "workspace_applying"
	plan := &integration.WorkspaceReconcilePlan{PlanID: "changed", Changes: []integration.WorkspaceChange{{Summary: "new consequence"}}}
	updated, cmd := m.Update(workspaceAppliedMsg{result: integration.WorkspaceReconcileResult{ReconfirmRequired: true, Plan: plan}})
	got := updated.(model)
	if cmd != nil || got.mode != "workspace_confirm" || got.editor.plan.PlanID != "changed" {
		t.Fatal("changed plan was not shown")
	}
}

func TestWorkspaceFailedApplyDiscardsStaleDraft(t *testing.T) {
	m := editorModel()
	updated, _ := m.Update(workspaceAppliedMsg{err: fmt.Errorf("stale revision")})
	got := updated.(model)
	if got.editor.active || got.err == nil {
		t.Fatal("failed apply retained stale draft")
	}
}

func TestWorkspaceWatchRefreshKeepsDraft(t *testing.T) {
	m := editorModel()
	updated, _ := m.Update(watchMsg{response: protocol.Response{OK: true, Revision: 42, Tasks: []protocol.Task{{ID: 9, Title: "Different task"}}}})
	got := updated.(model)
	if got.editor.state.Revision != "original" || got.editor.state.Name != "Plan" {
		t.Fatal("watch changed editor target")
	}
}

func TestWorkspaceConfirmationCanScrollAllWarnings(t *testing.T) {
	m := editorModel()
	m.mode, m.width, m.height = "workspace_confirm", 90, 22
	for index := 0; index < 30; index++ {
		m.editor.plan.Warnings = append(m.editor.plan.Warnings, fmt.Sprintf("Warning %d", index))
	}
	for index := 0; index < 40; index++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	if !strings.Contains(m.View(), "Warning 29") || !strings.Contains(m.View(), "y/enter apply") {
		t.Fatal("last warning or confirmation controls are inaccessible")
	}
}

func TestWorkspaceNoteReusesSelectedTasksAuthoredNote(t *testing.T) {
	m := editorModel()
	m.editor.desired.Note = nil
	m.editor.task = protocol.Task{SourceRefs: []protocol.SourceRef{{ID: "obsidian:task:existing", Authored: true, WorkspaceAnchorPath: "/vault/Tasks/Existing/Existing.md"}}}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	got := updated.(model)
	if cmd != nil || got.editor.desired.Note == nil || got.editor.desired.Note.Create || got.editor.desired.Note.LinkingKey != "obsidian:task:existing" {
		t.Fatal("existing authored note was not reused")
	}
}
