// Read-only inventory for the breaking CLI OAuth rollout. Never prints values
// from configuration or credentials and never modifies/revokes/migrates data.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
	"radar/internal/config"
	"radar/internal/setup"
	"radar/internal/state"
)

type entry struct {
	Format   string   `json:"format"`
	Path     string   `json:"path"`
	Exists   bool     `json:"exists"`
	Handling string   `json:"handling"`
	Fields   []string `json:"manual_fields,omitempty"`
}

func inventory(path, format string, obsolete map[string][]string) entry {
	e := entry{Format: format, Path: path, Handling: "unchanged"}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		e.Handling = "absent"
		return e
	}
	e.Exists = true
	if err != nil {
		e.Handling = "inspect manually (unreadable)"
		return e
	}
	if obsolete == nil {
		return e
	}
	var document map[string]yaml.Node
	if yaml.Unmarshal(data, &document) != nil || document == nil {
		e.Handling = "inspect manually (invalid mapping)"
		return e
	}
	for _, namespace := range []string{"jira", "datadog"} {
		fields := map[string]yaml.Node{}
		if node, exists := document[namespace]; exists {
			if node.Decode(&fields) != nil {
				e.Handling = "inspect manually (invalid integration mapping)"
				return e
			}
		}
		for _, key := range obsolete[namespace] {
			if _, exists := fields[key]; exists {
				e.Fields = append(e.Fields, namespace+"."+key)
			}
		}
	}
	if len(e.Fields) > 0 {
		e.Handling = "intentionally rejected by hard-switch target; edit manually"
	}
	return e
}
func main() {
	path, err := config.Path()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot resolve Radar configuration path")
		os.Exit(1)
	}
	secretPath, _ := config.SecretsPath()
	entries := []entry{
		inventory(path, "config.yaml", map[string][]string{"jira": {"email", "api_token"}, "datadog": {"api_key", "app_key", "access_token"}}),
		inventory(secretPath, "secrets.yaml", map[string][]string{"jira": {"api_token"}, "datadog": {"api_key", "app_key", "access_token"}}),
	}
	statePath, err := state.Path()
	if err == nil {
		entries = append(entries, inventory(statePath, "task state", nil))
	}
	stateRoot := os.Getenv("XDG_STATE_HOME")
	if stateRoot == "" {
		home, _ := os.UserHomeDir()
		stateRoot = filepath.Join(home, ".local", "state")
	}
	agents := os.Getenv("RADAR_AGENT_ROOT")
	if agents == "" {
		agents = filepath.Join(stateRoot, "radar", "agents")
	}
	e := entry{Format: "durable agents", Path: agents, Handling: "unchanged"}
	_, err = os.Stat(agents)
	e.Exists = err == nil
	entries = append(entries, e)
	// Decode only non-secret paths to inventory notes/workspaces, even if old
	// connection fields intentionally prevent loading the new configuration.
	cfg := config.Default()
	if b, err := os.ReadFile(path); err == nil {
		var paths struct {
			Workspace struct {
				RootDir string `yaml:"root_dir"`
			} `yaml:"workspace"`
			Obsidian struct {
				VaultPath string `yaml:"vault_path"`
			} `yaml:"obsidian"`
		}
		if yaml.Unmarshal(b, &paths) == nil {
			if paths.Workspace.RootDir != "" {
				cfg.Workspace.RootDir = paths.Workspace.RootDir
			}
			cfg.Obsidian.VaultPath = paths.Obsidian.VaultPath
		}
	}
	for _, p := range []struct{ format, path string }{{"workspaces", cfg.Workspace.RootDir}, {"notes", cfg.Obsidian.VaultPath}} {
		if strings.TrimSpace(p.path) == "" {
			continue
		}
		expanded, err := setup.ExpandPath(p.path)
		if err != nil {
			continue
		}
		_, err = os.Stat(expanded)
		entries = append(entries, entry{Format: p.format, Path: expanded, Exists: err == nil, Handling: "unchanged"})
	}
	result := struct {
		ReleaseQualified bool    `json:"release_qualified"`
		Inventory        []entry `json:"inventory"`
	}{false, entries}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(result)
}
