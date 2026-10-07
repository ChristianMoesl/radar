package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func setupDraftFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), "config"))
	path, _ := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetupDraftPreservesUneditedSettingsAndSymlink(t *testing.T) {
	path := setupDraftFixture(t)
	target := filepath.Join(os.Getenv("HOME"), "dotfiles", "radar.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	original := `future:
  large: 9007199254740993
model: custom-model
jira:
  status_mapping:
    Ready: low_priority
  future: keep
workspace:
  auto_confirm: false
  future: keep`
	if err := os.WriteFile(target, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	draft, err := LoadSetupDraft()
	if err != nil {
		t.Fatal(err)
	}
	cfg := draft.Config
	cfg.Workspace.RootDir = "/new/workspaces"
	cfg.Jira.BaseURL = "https://example.atlassian.net"
	preview, err := draft.Preview(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`9007199254740993`, `custom-model`, `Ready: low_priority`, `auto_confirm: false`, `future: keep`} {
		if !strings.Contains(string(preview), want) {
			t.Fatalf("missing %s from %s", want, preview)
		}
	}
	if err := WithSetupLock(func() error { return draft.Save(cfg) }); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(path)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced symlink")
	}
	dir, _ := os.Stat(filepath.Dir(target))
	if dir.Mode().Perm() != 0755 {
		t.Fatal("changed dotfiles directory permissions")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(preview) {
		t.Fatal("saved document differs from reviewed document")
	}
	loaded, err := Load()
	if err != nil || loaded.Model != "custom-model" || loaded.Workspace.RootDir != "/new/workspaces" {
		t.Fatalf("reload: %+v %v", loaded, err)
	}
}

func TestSetupDraftRejectsConcurrentConfigOrSecrets(t *testing.T) {
	for _, kind := range []string{"config", "secrets", "deleted", "appeared"} {
		t.Run(kind, func(t *testing.T) {
			path := setupDraftFixture(t)
			if kind != "appeared" {
				if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			draft, err := LoadSetupDraft()
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "secrets":
				err = SaveSecrets(Secrets{"other": {"token": "concurrent"}})
			case "deleted":
				err = os.Remove(path)
			default:
				err = os.WriteFile(path, []byte(`model: concurrent`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := draft.CheckUnchanged(); err == nil {
				t.Fatal("concurrent edit accepted")
			}
		})
	}
}

func TestSetupDraftCanRemoveAnEditedOptionalField(t *testing.T) {
	path := setupDraftFixture(t)
	if err := os.WriteFile(path, []byte(`jira:
  api_base_url: https://example.test/rest/api/3
  future: true`), 0600); err != nil {
		t.Fatal(err)
	}
	draft, err := LoadSetupDraft()
	if err != nil {
		t.Fatal(err)
	}
	cfg := draft.Config
	cfg.Jira.APIBaseURL = ""
	data, err := draft.Preview(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]map[string]any
	if err := yaml.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if _, ok := object["jira"]["api_base_url"]; ok || object["jira"]["future"] != true {
		t.Fatal(string(data))
	}
}
