package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	sessionlayout "radar/internal/integration/tmux/layout"
	workspacegroup "radar/internal/integration/workspace/group"
)

type setupRunner struct {
	fakeRunner
	missing string
}

func (r *setupRunner) LookPath(name string) error {
	if name == r.missing || (r.missing == "sbx" && name == "sbx.exe") {
		return errors.New("not found")
	}
	return nil
}

func TestSandboxInstallAndOverrideMatrix(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		for _, installed := range []bool{false, true} {
			for _, global := range []string{"auto", "true", "false"} {
				for _, repo := range []string{"auto", "true", "false"} {
					t.Run(fmt.Sprintf("%s/installed=%t/global=%s/repo=%s", goos, installed, global, repo), func(t *testing.T) {
						withWorkspaceGOOS(t, goos)
						tmp := t.TempDir()
						t.Setenv("HOME", tmp)
						t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
						t.Setenv("PATH", tmp)
						if installed {
							if err := os.WriteFile(filepath.Join(tmp, "sbx"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
								t.Fatal(err)
							}
						}
						cfgPath, err := config.EnsureFile()
						if err != nil {
							t.Fatal(err)
						}
						cfgJSON := `{}`
						if global != "auto" {
							cfgJSON = `{"sbx":{"enabled":` + global + `}}`
						}
						if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0600); err != nil {
							t.Fatal(err)
						}
						repository := filepath.Join(tmp, "repo")
						if err := os.Mkdir(repository, 0755); err != nil {
							t.Fatal(err)
						}
						if repo != "auto" {
							if err := os.WriteFile(filepath.Join(repository, ".radar.json"), []byte(`{"sbx":{"enabled":`+repo+`}}`), 0600); err != nil {
								t.Fatal(err)
							}
						}
						runner := &setupRunner{fakeRunner: fakeRunner{repo: repository}}
						if !installed {
							runner.missing = "sbx"
						}
						source := NewSource(testNoteAuthor(t))
						options, err := source.createOptions(integration.ManagedWorkspaceRequest{
							Repo: repository, Name: "install-check", Base: "origin/main", BranchMode: integration.WorkspaceBranchNew,
							WorkspaceRoot: filepath.Join(tmp, "workspaces"),
						})
						if err != nil {
							t.Fatal(err)
						}
						enabled := goos == "darwin" && installed
						if global != "auto" {
							enabled = global == "true"
						}
						if repo != "auto" {
							enabled = repo == "true"
						}
						plan, _, err := planCreate(context.Background(), runner, options)
						wantError := enabled && (goos != "darwin" || !installed)
						if (err != nil) != wantError {
							t.Fatalf("enabled=%t plan=%+v err=%v", enabled, plan, err)
						}
						if err == nil && (plan.group.Sandbox != nil) != enabled {
							t.Fatalf("sandbox=%+v, want enabled=%t", plan.group.Sandbox, enabled)
						}
						if data, _ := os.ReadFile(cfgPath); string(data) != cfgJSON {
							t.Fatal("detection rewrote the config")
						}
						if _, err := os.Stat(filepath.Join(options.WorkspaceRoot, "install-check")); !os.IsNotExist(err) {
							t.Fatal("preview created an anchor")
						}
						for _, call := range runner.calls {
							if (call.name == "sbx" && len(call.args) > 0 && call.args[0] != "ls") || (call.name == "tmux" && len(call.args) > 0 && call.args[0] == "new-session") {
								t.Fatalf("preview provisioned a resource: %+v", call)
							}
						}
					})
				}
			}
		}
	}
}

func TestMissingWorkspacePrerequisitesLeaveNoResources(t *testing.T) {
	for _, missing := range []string{"tmux", "pi", "nvim", "sbx", "vault"} {
		t.Run(missing, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			tmp := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
			vault := filepath.Join(tmp, "vault")
			if missing != "vault" {
				if err := os.MkdirAll(filepath.Join(vault, ".obsidian"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			root := filepath.Join(tmp, "workspaces")
			runner := &setupRunner{missing: missing}
			_, err := Create(context.Background(), runner, CreateOptions{
				Name: "setup-check", WorkspaceRoot: root, Sandbox: missing == "sbx", NoteAuthor: obsidian.NewSourceAt(vault),
			})
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("missing %s: error=%v", missing, err)
			}
			for _, path := range []string{filepath.Join(root, "setup-check"), filepath.Join(root, ".radar-workspaces.json"), filepath.Join(vault, "Tasks")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("failed preflight left resource %s", path)
				}
			}
			if len(runner.calls) != 0 {
				t.Fatalf("failed preflight executed commands: %+v", runner.calls)
			}
		})
	}
}

func TestCustomLayoutDoesNotRequireUnusedEditor(t *testing.T) {
	runner := &setupRunner{missing: "nvim"}
	cfg := sessionlayout.Config{Windows: []sessionlayout.Window{{Name: "agent", Panes: []sessionlayout.Pane{{Command: "pi $RADAR_PI_ARGS"}}}}}
	if err := validateSessionDependencies(runner, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestExistingWorkspaceRuntimeIgnoresChangedGlobalDefaults(t *testing.T) {
	for _, sandboxed := range []bool{false, true} {
		t.Run(fmt.Sprintf("sandboxed=%t", sandboxed), func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			root := t.TempDir()
			author := testNoteAuthor(t)
			runner := &fakeRunner{}
			created, err := Create(context.Background(), runner, CreateOptions{Name: "existing", WorkspaceRoot: root, NoteAuthor: author, Sandbox: sandboxed})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := workspacegroup.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(registry)
			// The same create path must open the registered workspace, not change its runtime.
			runner.calls = nil
			_, err = Create(context.Background(), runner, CreateOptions{Name: "existing", WorkspaceRoot: root, NoteAuthor: author, NotePath: registry.Workspaces[0].NotePath, TaskLinkingKey: registry.Workspaces[0].NoteKey(), Sandbox: !sandboxed})
			if err != nil {
				t.Fatal(err)
			}
			after, err := workspacegroup.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			afterJSON, _ := json.Marshal(after)
			if string(before) != string(afterJSON) {
				t.Fatalf("opening %s changed the registration", created.Path)
			}
			if !sandboxed {
				assertNotCalled(t, runner.calls, "sbx")
			}
		})
	}
}

func TestExistingSessionDoesNotRequirePaneToolsToBeReinstalled(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	created, err := Create(context.Background(), runner, CreateOptions{Name: "existing", WorkspaceRoot: root, NoteAuthor: testNoteAuthor(t)})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	running := &setupRunner{fakeRunner: fakeRunner{hasSession: true}, missing: "nvim"}
	if _, _, err := startWorkspaceRuntime(context.Background(), running, registry.Workspaces[0], ""); err != nil {
		t.Fatalf("could not reopen %s: %v", created.Path, err)
	}
	assertNotCalledContains(t, running.calls, "tmux", "new-session")
}

type failingSandboxSetup struct{ fakeRunner }

func (r *failingSandboxSetup) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	output, err := r.fakeRunner.Run(ctx, cwd, name, args...)
	if name == "sbx" && len(args) > 0 && args[0] == "create" {
		return "", errors.New("invalid sandbox kit")
	}
	return output, err
}

func TestSandboxProvisioningFailureNeverLaunchesHostSetup(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	repo, root := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"setup":["echo setup-marker"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &failingSandboxSetup{fakeRunner: fakeRunner{repo: repo}}
	_, err := Create(context.Background(), runner, CreateOptions{
		Repo: repo, Name: "sandbox-failure", Base: "origin/main", BranchMode: integration.WorkspaceBranchNew,
		WorkspaceRoot: root, NoteAuthor: testNoteAuthor(t), Sandbox: true,
	})
	if err == nil {
		t.Fatal("sandbox provisioning failure was hidden")
	}
	assertNotCalledContains(t, runner.calls, "tmux", "new-session")
	assertNotCalledContains(t, runner.calls, "tmux", "setup-marker")
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Workspaces) != 1 || registry.Workspaces[0].Sandbox == nil {
		t.Fatal("failed sandbox was silently removed from desired state")
	}
}
