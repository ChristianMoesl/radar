package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

func TestWorkspaceContextRegistrationOnly(t *testing.T) {
	root := t.TempDir()
	workspaces := filepath.Join(root, "workspaces")
	anchor := filepath.Join(workspaces, "fixture")
	member := filepath.Join(anchor, "member")
	vault := filepath.Join(root, "vault")
	configDir := filepath.Join(root, "config", "radar")
	for _, path := range []string{configDir, filepath.Join(vault, ".obsidian")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := json.Marshal(map[string]any{
		"workspace":             map[string]string{"root_dir": workspaces},
		"obsidian":              map[string]string{"vault_path": vault},
		"linking_mark_prefixes": []string{"ABC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(configDir))
	// No Git, SBX, tmux or repository discovery executable is available. Even
	// the recorded member and sandbox can be missing: registration is enough.
	t.Setenv("PATH", "")
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "fixture", Path: anchor,
		Members: []workspacegroup.Member{{Repository: filepath.Join(root, "source"), Path: member, Branch: "feature"}},
		Sandbox: &workspacegroup.Sandbox{Name: "fixture", Agent: "shell"},
	}
	if err := workspacegroup.Save(workspaces, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{anchor, filepath.Join(anchor, "notes.md"), member, filepath.Join(member, "src"), filepath.Join(root, "unrelated"), anchor + "-sibling"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			stdout, err := os.CreateTemp(root, "stdout-*")
			if err != nil {
				t.Fatal(err)
			}
			defer stdout.Close()
			previous := os.Stdout
			os.Stdout = stdout
			defer func() { os.Stdout = previous }()
			runWorkspaceContext([]string{"--workspace", path, "--registration-only"})
			if _, err := stdout.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Registered bool   `json:"registered"`
				Path       string `json:"workspace_path"`
			}
			if err := json.NewDecoder(stdout).Decode(&result); err != nil {
				t.Fatal(err)
			}
			want := path != filepath.Join(root, "unrelated") && path != anchor+"-sibling"
			if result.Registered != want || (want && result.Path != anchor) || (!want && result.Path != "") {
				t.Fatalf("registration for %s = %+v, want registered=%t", path, result, want)
			}
		})
	}
}
