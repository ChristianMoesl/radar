package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadUsesDefaultsWhenConfigIsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.RepositoryDirs, []string{"~/workspace", "~/code", "~/src", "~/dev", "~/projects"}) {
		t.Fatalf("RepositoryDirs = %#v", cfg.RepositoryDirs)
	}
	if cfg.WorkspaceRoot != "~/workspaces" {
		t.Fatalf("WorkspaceRoot = %q", cfg.WorkspaceRoot)
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
  "filters": {"mute_repos": ["org/noisy"]}
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
	if !reflect.DeepEqual(cfg.Filters.MuteRepos, []string{"org/noisy"}) {
		t.Fatalf("Filters.MuteRepos = %#v", cfg.Filters.MuteRepos)
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
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
