package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultUsesProductDefaults(t *testing.T) {
	home := t.TempDir()
	dataHome := filepath.Join(home, "data")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", dataHome)

	cfg := Default()
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
	if !reflect.DeepEqual(cfg.Jira.AuthoritativeIssueTypes, []string{"Task", "Bug", "Sub-task"}) {
		t.Fatalf("Jira.AuthoritativeIssueTypes = %#v, want defaults", cfg.Jira.AuthoritativeIssueTypes)
	}
	if cfg.Jira.SignalForStatus(" in review ") != "in_progress" || cfg.Jira.SignalForStatus("Selected for Development") != "low_priority" {
		t.Fatalf("Jira status defaults = %#v, fallback %q", cfg.Jira.StatusMapping, cfg.Jira.UnmappedStatus)
	}
	if !reflect.DeepEqual(cfg.Datadog.MonitorStatuses, []string{"Alert", "Warn", "No Data"}) {
		t.Fatalf("Datadog.MonitorStatuses = %#v, want default unhealthy statuses", cfg.Datadog.MonitorStatuses)
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
  "linking_mark_prefixes": ["xyz"],
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
  "jira": {
    "authoritative_issue_types": [" Story ", "Bug"],
    "status_mapping": {"Blocked": "attention"},
    "unmapped_status": "immediate"
  },
  "datadog": {
    "monitor_query": "tag:team:platform",
    "monitor_statuses": [" alert ", "warn"]
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.LinkingMarkPrefixes, []string{"XYZ"}) {
		t.Fatalf("LinkingMarkPrefixes = %#v", cfg.LinkingMarkPrefixes)
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
	if !reflect.DeepEqual(cfg.Jira.AuthoritativeIssueTypes, []string{"Story", "Bug"}) {
		t.Fatalf("Jira.AuthoritativeIssueTypes = %#v", cfg.Jira.AuthoritativeIssueTypes)
	}
	if !cfg.Jira.IsAuthoritativeIssueType(" story ") || cfg.Jira.IsAuthoritativeIssueType("Epic") {
		t.Fatalf("Jira authoritative issue type matching is incorrect: %#v", cfg.Jira.AuthoritativeIssueTypes)
	}
	if cfg.Jira.SignalForStatus(" blocked ") != "attention" || cfg.Jira.SignalForStatus("Open") != "immediate" {
		t.Fatalf("Jira status config = %#v, fallback %q", cfg.Jira.StatusMapping, cfg.Jira.UnmappedStatus)
	}
	if cfg.Datadog.MonitorQuery != "tag:team:platform" {
		t.Fatalf("Datadog.MonitorQuery = %q", cfg.Datadog.MonitorQuery)
	}
	if !reflect.DeepEqual(cfg.Datadog.MonitorStatuses, []string{"Alert", "Warn"}) {
		t.Fatalf("Datadog.MonitorStatuses = %#v", cfg.Datadog.MonitorStatuses)
	}
}

func TestLoadRequiresLinkingMarkPrefixes(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "linking_mark_prefixes must not be empty") {
		t.Fatalf("Load() error = %v, want required linking mark prefixes error", err)
	}
}

func TestLoadRejectsInvalidLinkingMarkPrefix(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"linking_mark_prefixes":["XYZ-"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "linking_mark_prefixes[0]") {
		t.Fatalf("Load() error = %v, want invalid linking mark prefix error", err)
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
	if err := os.WriteFile(path, []byte(`{"linking_mark_prefixes":["XYZ"],"thinking":"maximum"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid thinking error")
	}
}

func TestLoadPreservesExplicitlyEmptyAuthoritativeJiraIssueTypes(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"linking_mark_prefixes":["XYZ"],"jira":{"authoritative_issue_types":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Jira.AuthoritativeIssueTypes == nil || len(cfg.Jira.AuthoritativeIssueTypes) != 0 {
		t.Fatalf("Jira.AuthoritativeIssueTypes = %#v, want explicit empty list", cfg.Jira.AuthoritativeIssueTypes)
	}
}

func TestLoadDoesNotTreatRemovedIssueTypesAsAlias(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"linking_mark_prefixes":["XYZ"],"jira":{"issue_types":["Story"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Jira.AuthoritativeIssueTypes, []string{"Task", "Bug", "Sub-task"}) {
		t.Fatalf("legacy issue_types changed config: %#v", cfg.Jira.AuthoritativeIssueTypes)
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
	if err := os.WriteFile(path, []byte(`{"linking_mark_prefixes":["XYZ"],"jira":{"authoritative_issue_types":["Story", " "]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "jira.authoritative_issue_types[1]") {
		t.Fatalf("Load() error = %v, want invalid Jira issue type error", err)
	}
}

func TestLoadAllowsExplicitlyEmptyJiraStatusMapping(t *testing.T) {
	writeConfig := func(contents string) {
		home := t.TempDir()
		configHome := filepath.Join(home, "config")
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", configHome)
		path := filepath.Join(configHome, "radar", "config.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(`{"linking_mark_prefixes":["XYZ"],"jira":{"status_mapping":{},"unmapped_status":"attention"}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Jira.StatusMapping) != 0 || cfg.Jira.SignalForStatus("In Progress") != "attention" {
		t.Fatalf("Jira status config = %#v, fallback %q", cfg.Jira.StatusMapping, cfg.Jira.UnmappedStatus)
	}
}

func TestLoadRejectsInvalidJiraStatusMapping(t *testing.T) {
	tests := []struct {
		name  string
		jira  string
		field string
	}{
		{name: "empty status", jira: `{"status_mapping":{" ":"attention"}}`, field: "jira.status_mapping"},
		{name: "unsupported mapping", jira: `{"status_mapping":{"Blocked":"done"}}`, field: `jira.status_mapping["Blocked"]`},
		{name: "unsupported fallback", jira: `{"unmapped_status":"done"}`, field: "jira.unmapped_status"},
		{name: "empty fallback", jira: `{"unmapped_status":""}`, field: "jira.unmapped_status"},
		{name: "duplicate normalized status", jira: `{"status_mapping":{"Blocked":"attention"," blocked ":"immediate"}}`, field: "match case-insensitively"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			configHome := filepath.Join(home, "config")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			path := filepath.Join(configHome, "radar", "config.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"linking_mark_prefixes":["XYZ"],"jira":`+tt.jira+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Load() error = %v, want field %s", err, tt.field)
			}
		})
	}
}

func TestLoadRejectsInvalidDatadogMonitorStatuses(t *testing.T) {
	tests := []struct {
		name     string
		statuses string
		field    string
	}{
		{name: "empty statuses", statuses: `[]`, field: "datadog.monitor_statuses must not be empty"},
		{name: "unsupported status", statuses: `["OK"]`, field: `datadog.monitor_statuses[0]`},
		{name: "duplicate normalized status", statuses: `["Warn"," warn "]`, field: "match case-insensitively"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			configHome := filepath.Join(home, "config")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			path := filepath.Join(configHome, "radar", "config.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			contents := `{"linking_mark_prefixes":["XYZ"],"datadog":{"monitor_statuses":` + tt.statuses + `}}`
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Load() error = %v, want field %s", err, tt.field)
			}
		})
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
	if !reflect.DeepEqual(generated.Jira.AuthoritativeIssueTypes, []string{"Task", "Bug", "Sub-task"}) {
		t.Fatalf("generated Jira.AuthoritativeIssueTypes = %#v, want defaults", generated.Jira.AuthoritativeIssueTypes)
	}
	if generated.Jira.SignalForStatus("In Progress") != "in_progress" || generated.Jira.UnmappedStatus != "low_priority" {
		t.Fatalf("generated Jira status config = %#v, fallback %q", generated.Jira.StatusMapping, generated.Jira.UnmappedStatus)
	}
	if generated.LinkingMarkPrefixes == nil || len(generated.LinkingMarkPrefixes) != 0 {
		t.Fatalf("generated LinkingMarkPrefixes = %#v, want mandatory empty list", generated.LinkingMarkPrefixes)
	}
	if generated.Datadog.MonitorQuery != "" {
		t.Fatalf("generated Datadog.MonitorQuery = %q, want disabled empty query", generated.Datadog.MonitorQuery)
	}
	if !reflect.DeepEqual(generated.Datadog.MonitorStatuses, []string{"Alert", "Warn", "No Data"}) {
		t.Fatalf("generated Datadog.MonitorStatuses = %#v, want default unhealthy statuses", generated.Datadog.MonitorStatuses)
	}
	if !strings.Contains(string(data), `"jira"`) || !strings.Contains(string(data), `"authoritative_issue_types"`) {
		t.Fatalf("generated config is missing Jira settings: %s", data)
	}
	if !strings.Contains(string(data), `"datadog"`) || !strings.Contains(string(data), `"monitor_query"`) || !strings.Contains(string(data), `"monitor_statuses"`) {
		t.Fatalf("generated config is missing Datadog settings: %s", data)
	}
}

func TestObsidianVaultValidationAndPreparation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	vault := filepath.Join(home, "Documents", "Obsidian", "Work")
	if err := os.MkdirAll(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := (ObsidianConfig{VaultPath: "~/Documents/Obsidian/Work"}).ValidateAndPrepare()
	if err != nil {
		t.Fatal(err)
	}
	if got != vault {
		t.Fatalf("vault = %q, want %q", got, vault)
	}
	if info, err := os.Stat(filepath.Join(vault, "Tasks")); err != nil || !info.IsDir() {
		t.Fatalf("task root was not created: info=%v err=%v", info, err)
	}

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "missing", path: ""},
		{name: "relative", path: "relative/vault"},
		{name: "not found", path: filepath.Join(home, "missing")},
		{name: "not vault", path: home},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := (ObsidianConfig{VaultPath: test.path}).ValidateAndPrepare(); err == nil {
				t.Fatalf("ValidateAndPrepare(%q) error = nil", test.path)
			}
		})
	}
}
