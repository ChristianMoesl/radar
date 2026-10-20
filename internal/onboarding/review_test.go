package onboarding

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"radar/internal/config"
	"radar/internal/integration/tmux"
	"radar/internal/setup"
)

func TestReviewIsCompactAndReadOnly(t *testing.T) {
	_, ui, _, home := fixture(t)
	cfg := config.Default()
	cfg.RepositoryDirs = []string{filepath.Join(home, "repos")}
	cfg.Workspace.RootDir = filepath.Join(home, "workspaces")
	cfg.Obsidian.VaultPath = filepath.Join(home, "notes")
	path, _ := config.Path()
	plan, err := tmux.PlanConfig(true)
	if err != nil {
		t.Fatal(err)
	}
	secret := "do-not-display-credential"
	report, err := buildReview(path, []byte("large-config-body\n"), config.Secrets{"custom": {"api_token": secret}}, reviewPlans(t, plan, cfg))
	if err != nil {
		t.Fatal(err)
	}
	text := report.summary(ui.Style())
	for _, want := range []string{"Review changes", "CREATE", "~/config/radar/config.yaml", "~/config/radar/secrets.yaml", "~/config/radar/AGENTS.md", "~/config/radar/tmux.conf", "~/.tmux.conf", "New directories", "~/notes/Tasks", "plaintext", "0600"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q: %s", want, text)
		}
	}
	for _, unwanted := range []string{"large-config-body", "# Radar agent instructions", "extended-keys on", secret, "Already done", home} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("summary dumped %q: %s", unwanted, text)
		}
	}
	if strings.Count(text, "\n") > 16 {
		t.Fatalf("summary is too long: %s", text)
	}
	for _, file := range report.files {
		if strings.Contains(file.details, secret) {
			t.Fatalf("credential entered detail model: %s", file.path)
		}
		if _, err := os.Lstat(file.path); !os.IsNotExist(err) {
			t.Fatalf("preview created %s: %v", file.path, err)
		}
	}
	for _, dir := range report.directories {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("preview created directory %s", dir)
		}
	}
}

func TestReviewFileActionsAndDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	file, err := reviewFileChange(path, "Settings", []byte("model: new\n"))
	if err != nil || file.action != "CREATE" || file.details != "model: new\n" {
		t.Fatalf("new file: %+v %v", file, err)
	}
	if err := os.WriteFile(path, []byte("model: old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err = reviewFileChange(path, "Settings", []byte("model: new\n"))
	if err != nil || file.action != "UPDATE" || !strings.Contains(file.details, "-model: old") || !strings.Contains(file.details, "+model: new") {
		t.Fatalf("diff: %+v %v", file, err)
	}
	file, err = reviewFileChange(path, "Settings", []byte("model: old\n"))
	if err != nil || file.action != "UNCHANGED" || strings.Contains(file.details, "model: old") {
		t.Fatalf("unchanged: %+v %v", file, err)
	}
}

func TestReviewSecretsComparesValuesWithoutExposingThem(t *testing.T) {
	fixture(t)
	if file, err := reviewSecrets(nil); err != nil || file != nil {
		t.Fatalf("unneeded credentials should have no row: %+v %v", file, err)
	}
	before := config.Secrets{"custom": {"api_token": "existing-private-token"}, "other": {"token": "unrelated-private-token"}}
	if err := config.SaveSecrets(before); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		updates config.Secrets
		action  string
	}{
		{nil, "UNCHANGED"},
		{config.Secrets{"custom": {"api_token": "existing-private-token"}}, "UNCHANGED"},
		{config.Secrets{"custom": {"api_token": "new-private-token"}}, "UPDATE"},
		{config.Secrets{"other": {"token": "new-private-token"}}, "UPDATE"},
		{config.Secrets{"custom": {"new_key": ""}}, "UPDATE"},
	} {
		file, err := reviewSecrets(tc.updates)
		if err != nil || file.action != tc.action {
			t.Fatalf("credentials: %+v %v", file, err)
		}
		if strings.Contains(file.details, "private-token") {
			t.Fatalf("credentials leaked: %s", file.details)
		}
	}
	got, err := config.LoadSecrets()
	if err != nil || got["custom"]["api_token"] != before["custom"]["api_token"] || len(got["datadog"]) != 0 {
		t.Fatalf("preview changed credentials: %v", err)
	}
}

func TestReviewPreservesInstructionsAndShowsExactTmuxAppend(t *testing.T) {
	_, _, _, home := fixture(t)
	path, _ := config.Path()
	instructions := filepath.Join(filepath.Dir(path), "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "missing-target"), instructions); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(home, ".tmux.conf")
	original := "set -g prefix C-a\n# keep my settings\n"
	if err := os.WriteFile(userPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := tmux.PlanConfig(true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RepositoryDirs = []string{filepath.Join(home, "repos")}
	report, err := buildReview(path, []byte("{}\n"), nil, reviewPlans(t, plan, cfg))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range report.files {
		if file.path == instructions && file.action != "UNCHANGED" {
			t.Fatal("dangling instruction symlink was not preserved")
		}
		if file.path == userPath && (file.action != "APPEND" || !strings.Contains(file.details, "+"+plan.Include) || strings.Contains(file.details, "-set -g prefix")) {
			t.Fatalf("tmux append: %+v", file)
		}
	}
	got, _ := os.ReadFile(userPath)
	if string(got) != original {
		t.Fatal("preview modified tmux config")
	}
}

func reviewKey(m reviewModel, key tea.KeyMsg) reviewModel {
	next, _ := m.Update(key)
	return next.(reviewModel)
}

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestReviewEnterSavesOnlyFromSummary(t *testing.T) {
	ui := &fakeUI{}
	report := setupReview{files: []reviewFile{{path: "/settings", purpose: "Settings", action: "CREATE", details: "requested contents"}}}
	m := newReviewModel(report, ui.Style())
	if !strings.Contains(m.View(), "[Enter] Save   [v] View details   [c] Cancel") || strings.Contains(m.View(), "requested contents") {
		t.Fatal(m.View())
	}
	m = reviewKey(m, runeKey("v"))
	if m.screen != reviewFiles || m.accepted {
		t.Fatal("details request approved writes")
	}
	m = reviewKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != reviewDetails || m.accepted || !strings.Contains(m.View(), "requested contents") {
		t.Fatal("file selection did not open details safely")
	}
	m = reviewKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.accepted {
		t.Fatal("Enter inside file details approved writes")
	}
	m = reviewKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = reviewKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = reviewKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.accepted || m.aborted || !strings.Contains(m.View(), "Changes approved") || strings.Contains(m.View(), "requested contents") {
		t.Fatal("summary Enter did not approve just the reviewed changes")
	}
}

func TestReviewCancellationAndScrolling(t *testing.T) {
	var contents strings.Builder
	for i := range 100 {
		fmt.Fprintf(&contents, "line %d with a long explanation that wraps\n", i)
	}
	report := setupReview{files: []reviewFile{{path: "/settings", purpose: "Settings", action: "CREATE", details: contents.String()}}}
	ui := &fakeUI{}
	for _, screen := range []reviewScreen{reviewSummary, reviewFiles, reviewDetails} {
		for _, key := range []tea.KeyMsg{runeKey("c"), {Type: tea.KeyCtrlC}, {Type: tea.KeyCtrlD}} {
			m := newReviewModel(report, ui.Style())
			m.screen = screen
			m = reviewKey(m, key)
			if !m.aborted || m.accepted {
				t.Fatal("cancellation approved changes")
			}
		}
	}
	m := newReviewModel(report, ui.Style())
	m.screen = reviewDetails
	next, _ := m.Update(tea.WindowSizeMsg{Width: 35, Height: 14})
	m = next.(reviewModel)
	if strings.Count(m.View(), "\n") > 14 || strings.Contains(m.View(), "line 99") {
		t.Fatalf("details dumped the full file: %s", m.View())
	}
	m = reviewKey(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.viewport.YOffset == 0 {
		t.Fatal("details cannot scroll")
	}
}

func TestReviewWarningsOnlyDescribeCompletedActions(t *testing.T) {
	ui := &fakeUI{}
	report := setupReview{}
	if strings.Contains(report.summary(ui.Style()), "Already done") {
		t.Fatal("claimed side effects when none occurred")
	}
	report.completed = []string{"pi-radar installed", "GitHub login"}
	text := report.summary(ui.Style())
	if !strings.Contains(text, "Already done: pi-radar installed; GitHub login. Cancelling will not undo these.") {
		t.Fatal(text)
	}
}

func reviewPlans(t *testing.T, plan tmux.ConfigPlan, cfg config.Config) []setup.Plan {
	t.Helper()
	p, err := plan.SetupPlan(&fakeUI{}, &fakeSystem{})
	if err != nil {
		t.Fatal(err)
	}
	p.Directories = []string{cfg.RepositoryDirs[0], cfg.Workspace.RootDir, cfg.Obsidian.VaultPath, config.ObsidianTaskRoot(cfg.Obsidian.VaultPath)}
	return []setup.Plan{p}
}
