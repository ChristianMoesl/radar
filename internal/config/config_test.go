package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadUsesDefaultsWhenConfigIsMissing(t *testing.T) {
	home := t.TempDir()
	dataHome := filepath.Join(home, "data")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", dataHome)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.RepositoryDirs, []string{"~/workspace", "~/code", "~/src", "~/dev", "~/projects"}) {
		t.Fatalf("RepositoryDirs = %#v", cfg.RepositoryDirs)
	}
	if cfg.WorkspaceRoot != filepath.Join(dataHome, "radar", "workspaces") {
		t.Fatalf("WorkspaceRoot = %q", cfg.WorkspaceRoot)
	}
	if cfg.SBX.Enabled {
		t.Fatal("SBX.Enabled = true, want disabled default")
	}
	if cfg.SBX.Kit.Name != "shell" || cfg.SBX.Kit.Path != "" {
		t.Fatalf("SBX.Kit = %#v, want default shell kit without a path", cfg.SBX.Kit)
	}
	if cfg.SBX.AdditionalMounts == nil || len(cfg.SBX.AdditionalMounts) != 0 {
		t.Fatalf("SBX.AdditionalMounts = %#v, want empty list", cfg.SBX.AdditionalMounts)
	}
	if len(cfg.Tmux.Windows) != 2 || cfg.Tmux.Windows[0].Name != "pi" || cfg.Tmux.Windows[1].Name != "nvim" {
		t.Fatalf("Tmux.Windows = %#v, want default Pi and nvim windows", cfg.Tmux.Windows)
	}
	if cfg.Jira.IssueTypes == nil || len(cfg.Jira.IssueTypes) != 0 {
		t.Fatalf("Jira.IssueTypes = %#v, want all issue types", cfg.Jira.IssueTypes)
	}
}

func TestDefaultWorkspaceRootUsesXDGDataFallback(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")

	if got := Default().WorkspaceRoot; got != "~/.local/share/radar/workspaces" {
		t.Fatalf("WorkspaceRoot = %q", got)
	}
}

func TestDefaultWorkspaceRootIgnoresRelativeXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "relative/data")

	if got := Default().WorkspaceRoot; got != "~/.local/share/radar/workspaces" {
		t.Fatalf("WorkspaceRoot = %q", got)
	}
}

func TestLoadReadsConfigFile(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{
  "repository_dirs": ["~/repos"],
  "workspace_root": "~/streams",
  "model": "github-copilot/claude-sonnet-4.5",
  "thinking": "high",
  "sbx": {
    "enabled": true,
    "kit": {"name": "radar", "path": "~/kits/radar"},
    "additional_mounts": ["~/shared", "/opt/tools"]
  },
  "tmux": {
    "windows": [{
      "name": "workspace",
      "layout": "horizontal",
      "panes": [
        {"command": "pi $RADAR_PI_ARGS"},
        {"command": "nvim ."}
      ]
    }]
  },
  "github": {"filters": {"mute_repos": ["org/noisy"]}},
  "jira": {"issue_types": ["Story", "Bug"]},
  "datadog": {"monitor_query": "tag:team:platform"}
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.RepositoryDirs, []string{"~/repos"}) {
		t.Fatalf("RepositoryDirs = %#v", cfg.RepositoryDirs)
	}
	if cfg.WorkspaceRoot != "~/streams" {
		t.Fatalf("WorkspaceRoot = %q", cfg.WorkspaceRoot)
	}
	if cfg.Model != "github-copilot/claude-sonnet-4.5" {
		t.Fatalf("Model = %q", cfg.Model)
	}
	if cfg.Thinking != "high" {
		t.Fatalf("Thinking = %q", cfg.Thinking)
	}
	if !cfg.SBX.Enabled {
		t.Fatal("SBX.Enabled = false, want true")
	}
	if cfg.SBX.Kit.Name != "radar" || cfg.SBX.Kit.Path != "~/kits/radar" {
		t.Fatalf("SBX.Kit = %#v", cfg.SBX.Kit)
	}
	if !reflect.DeepEqual(cfg.SBX.AdditionalMounts, []string{"~/shared", "/opt/tools"}) {
		t.Fatalf("SBX.AdditionalMounts = %#v", cfg.SBX.AdditionalMounts)
	}
	if len(cfg.Tmux.Windows) != 1 || cfg.Tmux.Windows[0].Name != "workspace" || cfg.Tmux.Windows[0].Layout != "horizontal" {
		t.Fatalf("Tmux.Windows = %#v", cfg.Tmux.Windows)
	}
	if !reflect.DeepEqual(cfg.GitHub.Filters.MuteRepos, []string{"org/noisy"}) {
		t.Fatalf("GitHub.Filters.MuteRepos = %#v", cfg.GitHub.Filters.MuteRepos)
	}
	if !reflect.DeepEqual(cfg.Jira.IssueTypes, []string{"Story", "Bug"}) {
		t.Fatalf("Jira.IssueTypes = %#v", cfg.Jira.IssueTypes)
	}
	if cfg.Datadog.MonitorQuery != "tag:team:platform" {
		t.Fatalf("Datadog.MonitorQuery = %q", cfg.Datadog.MonitorQuery)
	}
}

func TestLoadRejectsInvalidThinking(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"thinking":"maximum"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid thinking error")
	}
}

func TestLoadRejectsEmptyJiraIssueType(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"jira":{"issue_types":["Story", " "]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "jira.issue_types[1]") {
		t.Fatalf("Load() error = %v, want invalid Jira issue type error", err)
	}
}

func TestEnsureFileCreatesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	path, err := EnsureFile()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, "config", "radar", "config.json") {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var generated Config
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	if generated.Jira.IssueTypes == nil || len(generated.Jira.IssueTypes) != 0 {
		t.Fatalf("generated Jira.IssueTypes = %#v, want all issue types", generated.Jira.IssueTypes)
	}
	if generated.Datadog.MonitorQuery != "" {
		t.Fatalf("generated Datadog.MonitorQuery = %q, want disabled empty query", generated.Datadog.MonitorQuery)
	}
	if !strings.Contains(string(data), `"jira"`) || !strings.Contains(string(data), `"issue_types"`) {
		t.Fatalf("generated config is missing Jira settings: %s", data)
	}
	if !strings.Contains(string(data), `"datadog"`) || !strings.Contains(string(data), `"monitor_query"`) {
		t.Fatalf("generated config is missing Datadog settings: %s", data)
	}
}
