package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"radar.nvim/internal/protocol"
)

func TestViewRendersTasksAndSources(t *testing.T) {
	model := model{
		summary: protocol.Summary{Attention: 1},
		tasks: []protocol.Task{{
			Title:     "Review change",
			Reason:    "review requested",
			Attention: "attention",
			SourceRefs: []protocol.SourceRef{{
				ID: "github:pr:owner/repo:1",
			}},
		}},
		sources: []protocol.SourceStatus{{Name: "github", Status: "ok", SourceRefCount: 1}},
	}

	view := model.View()
	for _, want := range []string{"Review change", "review requested", "github:pr:owner/repo:1", "github", "1 refs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestCreateRepoViewRendersFuzzySearch(t *testing.T) {
	model := model{mode: "create_repo", create: createForm{repoList: picker{query: "rad", options: []string{"/repo/radar", "/repo/other"}}}}

	view := model.View()
	for _, want := range []string{"Create workstream", "Repository", "rad", "/repo/radar"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestCreateNameViewRendersSelectedRepoAndBase(t *testing.T) {
	model := model{mode: "create_name", create: createForm{repo: "/repo/radar", base: "origin/main", name: "small-fix"}}

	view := model.View()
	for _, want := range []string{"Create workstream", "Repository /repo/radar", "Base       origin/main", "Name       small-fix"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestCreateRepoViewShortensHomePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "workspace", "radar")
	model := model{mode: "create_repo", create: createForm{repoList: picker{options: []string{path}}}}

	view := model.View()
	if !strings.Contains(view, "~/workspace/radar") {
		t.Fatalf("View() did not shorten home path:\n%s", view)
	}
	if strings.Contains(view, path) {
		t.Fatalf("View() contains unshortened home path:\n%s", view)
	}
}

func TestFuzzyMatch(t *testing.T) {
	if !fuzzyMatch("/repo/radar", "rdr") {
		t.Fatal("fuzzyMatch() did not match ordered characters")
	}
	if fuzzyMatch("/repo/radar", "zzz") {
		t.Fatal("fuzzyMatch() matched missing characters")
	}
}

func TestTmuxSessionTargetUsesStableSessionID(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{
		Source: "tmux",
		Kind:   "session",
		Title:  "radar",
		Metadata: map[string]string{
			"session_id":    "$3",
			"switch_target": "$3",
		},
	}}}

	if got := tmuxSessionTarget(task); got != "$3" {
		t.Fatalf("tmuxSessionTarget() = %q, want $3", got)
	}
}
