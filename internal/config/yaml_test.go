package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/configfile"
)

func TestGeneratedYAMLMatchesGoldenAndRoundTrips(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	data, err := marshalConfig(Default())
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "default.yaml")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(golden), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("generated config differs from %s:\n%s", golden, data)
	}
	path := setupDraftFixture(t)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := marshalConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, roundtrip) {
		t.Fatalf("generated config changed semantics:\n%s", roundtrip)
	}
}

func TestGeneratedConfigHeaderSurvivesRepeatedSetup(t *testing.T) {
	path := setupDraftFixture(t)
	if _, err := EnsureFile(); err != nil {
		t.Fatal(err)
	}
	header := "# " + strings.ReplaceAll(configHeader, "\n", "\n# ") + "\n"
	for _, root := range []string{"/first", "/second", "/second"} {
		draft, err := LoadSetupDraft()
		if err != nil {
			t.Fatal(err)
		}
		cfg := draft.Config
		cfg.Workspace.RootDir = root
		preview, err := draft.Preview(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(preview, []byte(header)) || bytes.Count(preview, []byte(header)) != 1 {
			t.Fatalf("expected exactly one header at the start:\n%s", preview)
		}
		if err := draft.Save(cfg); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, preview) {
			t.Fatal("saved header differs from preview")
		}
	}
}

func TestSetupUsesSharedGeneratorAndPreservesCommentsOnRepeat(t *testing.T) {
	path := setupDraftFixture(t)
	draft, err := LoadSetupDraft()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := draft.Preview(draft.Config)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := marshalConfig(draft.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(preview, generated) {
		t.Fatal("onboarding uses different generation")
	}
	if err := draft.Save(draft.Config); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, preview) {
		t.Fatal("preview differs from saved YAML")
	}

	original := "# My setup\nmodel: 'custom' # private choice\nrepository_dirs:\n  - ~/workspace # primary\nworkspace:\n  # Keep work on this disk\n  root_dir: '/old' # old location\n  auto_confirm: false\nfuture: 9007199254740993\njira:\n  authoritative_issue_types: []\n  status_mapping: {}\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		draft, err := LoadSetupDraft()
		if err != nil {
			t.Fatal(err)
		}
		unchanged, err := draft.Preview(draft.Config)
		if err != nil {
			t.Fatal(err)
		}
		current, _ := os.ReadFile(path)
		if !bytes.Equal(unchanged, current) {
			t.Fatal("no-op preview changed bytes")
		}
		cfg := draft.Config
		cfg.Workspace.RootDir = "/new"
		cfg.Jira.BaseURL = "https://example.atlassian.net"
		preview, err := draft.Preview(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := draft.Save(cfg); err != nil {
			t.Fatal(err)
		}
		saved, _ := os.ReadFile(path)
		if !bytes.Equal(saved, preview) {
			t.Fatal("preview changed during save")
		}
		for _, want := range []string{"# My setup", "model: 'custom' # private choice", "# primary", "# Keep work on this disk", "root_dir: '/new' # old location", "auto_confirm: false", "future: 9007199254740993", "authoritative_issue_types: []", "status_mapping: {}"} {
			if strings.Count(string(saved), want) != 1 {
				t.Fatalf("missing/duplicated %q:\n%s", want, saved)
			}
		}
		loaded, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Jira.StatusMapping) != 0 || len(loaded.Jira.AuthoritativeIssueTypes) != 0 {
			t.Fatal("empty collections turned into defaults")
		}
	}
}

func TestYAMLJiraMappingReplacesDefaultsAndTracksEmptyFallback(t *testing.T) {
	path := setupDraftFixture(t)
	for _, data := range []string{"jira:\n  status_mapping: {}\n", "jira:\n  status_mapping:\n    Blocked: immediate\n"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := cfg.Jira.StatusMapping["In Progress"]; exists {
			t.Fatal("custom map merged defaults")
		}
	}
	if err := os.WriteFile(path, []byte("jira:\n  unmapped_status: ''\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("explicit empty fallback accepted")
	}
}

func TestSecretYAMLRoundTripCommentsAndRedaction(t *testing.T) {
	path := secretFixture(t)
	values := Secrets{"jira": {"api_token": "true", "number": "00123", "special": "! @ # : [ ] { } & *", "multiline": "first\nsecond\n"}, "other": {"token": "9007199254740993"}}
	if err := SaveSecrets(values); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, values) {
		t.Fatal("secret string values changed")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# Plaintext credentials") || !strings.Contains(string(data), "RADAR_JIRA_API_TOKEN") {
		t.Fatal("missing secret guidance")
	}
	original := "# Private account\njira:\n  # rotate monthly\n  api_token: 'old' # personal token\nother:\n  token: unchanged # do not rotate\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveSecrets(Secrets{"jira": {"api_token": "new"}}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	for _, want := range []string{"# Private account", "# rotate monthly", "api_token: 'new' # personal token", "token: unchanged # do not rotate"} {
		if !strings.Contains(string(data), want) {
			t.Fatal("secret comments or other namespaces changed")
		}
	}
	if err := SaveSecrets(Secrets{"jira": {"api_token": "new"}}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(data, after) {
		t.Fatal("no-op secret update changed bytes")
	}
	for _, malformed := range []string{"jira: [do-not-echo", "jira:\n  api_token: [do-not-echo]\n", "jira:\n  do-not-echo: a\n  do-not-echo: b\n", "jira: {}\n---\nother: do-not-echo\n"} {
		if err := os.WriteFile(path, []byte(malformed), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSecrets(); err == nil || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatal("secret error absent or not redacted")
		}
		if err := SaveSecrets(values); err == nil || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatal("unsafe secret update accepted or disclosed data")
		}
		unchanged, _ := os.ReadFile(path)
		if string(unchanged) != malformed {
			t.Fatal("invalid file overwritten")
		}
	}
}

func TestLoadYAMLReportsFileAndLine(t *testing.T) {
	path := setupDraftFixture(t)
	if err := os.WriteFile(path, []byte("workspace:\n  auto_confirm: true\n  auto_confirm: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("error lacks source location: %v", err)
	}
}

func TestConfigYAMLNestedTagsAndMultilineValues(t *testing.T) {
	path := setupDraftFixture(t)
	data := `repository_dirs: [~/checkout]
workspace:
  root_dir: ~/worktrees
  auto_confirm: false
  cleanup:
    disposable_entries: [cache]
sbx:
  enabled: false
  kit: {name: custom, path: ~/kit}
  env_file: ~/private.env
  ready_command: [ready, '--wait']
  additional_mounts: [~/tools:ro]
tmux:
  windows:
    - name: agent
      layout: vertical
      panes:
        - command: |
            pi $RADAR_PI_ARGS
            echo finished
github:
  filters:
    mute_repos: [example/private]
    rules:
      - name: quiet
        users: [bot]
        action: deprioritize
jira:
  base_url: https://example.atlassian.net
  email: user@example.com
  authoritative_issue_types: []
  status_mapping: {}
  unmapped_status: attention
datadog:
  monitor_query: 'tag:team:platform'
  monitor_statuses: [Warn]
obsidian:
  vault_path: ~/notes
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace.AutoConfirm || cfg.Workspace.Cleanup.DisposableEntries[0] != "cache" || cfg.SBX.EnvFile != "~/private.env" || cfg.GitHub.Filters.Rules[0].Users[0] != "bot" || cfg.Obsidian.VaultPath != "~/notes" || !strings.Contains(cfg.Tmux.Windows[0].Panes[0].Command, "\necho finished\n") {
		t.Fatal("nested fields did not decode")
	}
	encoded, err := marshalConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := Default()
	if err := configfile.Decode(encoded, &reloaded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, reloaded) {
		t.Fatal("nested config failed to round-trip")
	}
}
