package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sessionlayout "radar/internal/integration/tmux/layout"
)

func TestWorkspacePanesRealTmuxAfterServerDirectoryRemoved(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprintf("early=%t", early), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			serverPath := filepath.Join(root, "previous-workspace")
			if err := os.Mkdir(serverPath, 0o755); err != nil {
				t.Fatal(err)
			}
			runner := newIsolatedPaneTmux(t, serverPath)
			// The server outlives workspace cleanup. tmux 3.8 can then ignore
			// -c and leave every new pane in this unlinked directory.
			if err := os.Remove(serverPath); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "workspace with 'quotes'")
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			physicalPath, err := filepath.EvalSymlinks(path)
			if err != nil {
				t.Fatal(err)
			}
			cwdCommand := func(name string) string {
				output := shellQuote(filepath.Join(path, name))
				return "pwd -P > " + output + "; printf '\nready\n' >> " + output + "; sleep 60"
			}
			cfg := sessionlayout.Config{Windows: []sessionlayout.Window{
				{Name: "workspace", Panes: []sessionlayout.Pane{
					{Command: cwdCommand("pi-cwd") + " # " + sessionlayout.PiArgsPlaceholder},
					{Command: cwdCommand("editor-cwd")},
				}},
				{Name: "other", Panes: []sessionlayout.Pane{
					{Command: cwdCommand("other-cwd")},
				}},
			}}
			if err := createTmuxWorkspaceWithGates(ctx, runner, path, path, "workspace", cfg, "--name test", nil, early); err != nil {
				t.Fatal(err)
			}
			if early {
				waitPaneCondition(t, func() bool { _, err := os.Stat(filepath.Join(path, "pi-cwd")); return err == nil })
				for _, name := range []string{"editor-cwd", "other-cwd"} {
					if _, err := os.Stat(filepath.Join(path, name)); !os.IsNotExist(err) {
						t.Fatalf("auxiliary pane ran before release: %s: %v", name, err)
					}
				}
				if err := releaseWorkspacePanes(ctx, runner, path, "workspace"); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"pi-cwd", "editor-cwd", "other-cwd"} {
				file := filepath.Join(path, name)
				waitPaneCondition(t, func() bool {
					data, _ := os.ReadFile(file)
					return strings.HasSuffix(string(data), "\nready\n")
				})
				data, err := os.ReadFile(file)
				if err != nil || string(data) != physicalPath+"\n\nready\n" {
					t.Errorf("%s: cwd = %q, want %q; error = %v", name, data, physicalPath, err)
				}
			}
		})
	}
}

func TestWorkspacePaneCommandStopsIfDirectoryIsMissing(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-run")
	command, _ := workspacePaneCommand(filepath.Join(root, "missing"), "touch "+shellQuote(marker), "", false)
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("command succeeded in a missing workspace: %s", output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("program ran despite missing workspace: %v", err)
	}
}
