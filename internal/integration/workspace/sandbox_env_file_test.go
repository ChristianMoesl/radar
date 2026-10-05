package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/config"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
)

// Fake SBX runners must account for optional flags before the positional kit.
func sandboxCreateMountArgs(args []string) []string {
	index := 1
	for index < len(args) && strings.HasPrefix(args[index], "--") {
		index += 2
	}
	return args[index+1:]
}

func writeSandboxEnvFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "generic env file")
	// Deliberately not a format Radar should validate or interpret.
	if err := os.WriteFile(path, []byte("opaque secret content; $(do-not-run)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRepoSandboxEnvFileInheritance(t *testing.T) {
	for _, test := range []struct {
		name, config, want string
	}{
		{"no sbx", `{}`, "/user/env"},
		{"absent", `{"sbx":{}}`, "/user/env"},
		{"override", `{"sbx":{"env_file":"/repo/env file"}}`, "/repo/env file"},
		{"disable", `{"sbx":{"env_file":""}}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadRepoConfig(repo)
			if err != nil {
				t.Fatal(err)
			}
			settings := workspaceSandboxConfig(cfg, true, "shell", "", "/user/env", nil)
			if settings.EnvFile != test.want {
				t.Fatalf("env-file = %q, want %q", settings.EnvFile, test.want)
			}
		})
	}
}

func TestRepoSandboxEnvFileRejectsRelativePaths(t *testing.T) {
	for _, path := range []string{"env", "./env", "../env", "~", "~someone/env", " "} {
		t.Run(path, func(t *testing.T) {
			repo := t.TempDir()
			data, _ := json.Marshal(RepoConfig{SBX: &SandboxConfig{EnvFile: &path}})
			if err := os.WriteFile(filepath.Join(repo, ".radar.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadRepoConfig(repo); err == nil || !strings.Contains(err.Error(), "absolute or start with ~/") {
				t.Fatalf("loadRepoConfig() error = %v", err)
			}
		})
	}
}

func TestResolveSandboxEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, test := range []struct{ path, want string }{
		{"", ""},
		{"~/env files/../env file", filepath.Join(home, "env file")},
		{"/env files/../env file", "/env file"},
		{"/env file ", "/env file "},
	} {
		got, err := resolveSandboxEnvFile(test.path)
		if err != nil || got != test.want {
			t.Fatalf("resolveSandboxEnvFile(%q) = %q, %v, want %q", test.path, got, err, test.want)
		}
	}
}

func TestSandboxCreateArgsWithEnvFile(t *testing.T) {
	got := sandboxCreateArgs("ABC-123", SandboxKitConfig{Name: "custom", Path: "/kit path"}, "/env file", []string{"/work path", "/shared:ro"})
	want := []string{"create", "--name", "ABC-123", "--kit", "/kit path", "--env-file", "/env file", "custom", "/work path", "/shared:ro"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestCreateRecordsAndPassesSelectedSandboxEnvFile(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	env := writeSandboxEnvFile(t)
	for _, mode := range []string{"user", "home", "repo", "disabled", "first member"} {
		t.Run(mode, func(t *testing.T) {
			repo, root := t.TempDir(), t.TempDir()
			options := CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxKitName: "shell", SandboxEnvFile: env, NoteAuthor: testNoteAuthor(t)}
			want := env
			switch mode {
			case "home":
				t.Setenv("HOME", filepath.Dir(env))
				options.SandboxEnvFile = "~/" + filepath.Base(env)
			case "repo", "first member":
				options.SandboxEnvFile = "/missing inherited file"
				data, _ := json.Marshal(RepoConfig{SBX: &SandboxConfig{EnvFile: &env}})
				if err := os.WriteFile(filepath.Join(repo, ".radar.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
				if mode == "first member" {
					second := t.TempDir()
					other := "/missing second repo env"
					data, _ := json.Marshal(RepoConfig{SBX: &SandboxConfig{EnvFile: &other}})
					if err := os.WriteFile(filepath.Join(second, ".radar.json"), data, 0o600); err != nil {
						t.Fatal(err)
					}
					options.Worktrees = []DesiredWorkspaceWorktree{{Repository: second, BranchMode: integration.WorkspaceBranchNew, Name: "ABC-123", Base: "origin/main"}}
				}
			case "disabled":
				want = ""
				options.SandboxEnvFile = "/missing inherited file"
				if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"sbx":{"env_file":""}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &fakeRunner{repo: repo}
			created, err := Create(context.Background(), multiRepoEnvFileRunner{runner}, options)
			if err != nil {
				t.Fatal(err)
			}
			_, recorded, found, err := RegisteredWorkspace(created.Path, root)
			if err != nil || !found || recorded.Sandbox.EnvFile != want {
				t.Fatalf("recorded workspace = %+v, %t, %v", recorded, found, err)
			}
			assertSandboxEnvFileArg(t, runner.calls, want)
		})
	}
}

func assertSandboxEnvFileArg(t *testing.T, calls []call, want string) {
	t.Helper()
	count := 0
	for _, call := range calls {
		if strings.Contains(strings.Join(call.args, " "), "opaque secret content") {
			t.Fatalf("command contains env-file contents: %+v", call)
		}
		if call.name != "sbx" || len(call.args) == 0 || call.args[0] != "create" {
			continue
		}
		count++
		found := false
		for index, arg := range call.args {
			if arg == "--env-file" {
				if want == "" || index+1 >= len(call.args) || call.args[index+1] != want {
					t.Fatalf("env-file argument: %#v, want %q", call.args, want)
				}
				found = true
			}
		}
		if found != (want != "") {
			t.Fatalf("env-file argument: %#v, want %q", call.args, want)
		}
	}
	if count == 0 {
		t.Fatal("no SBX creation")
	}
}

func TestCreateSessionPassesSandboxEnvFile(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := filepath.Join(home, "env file")
	if err := os.WriteFile(env, []byte("opaque secret content"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	_, err := CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: t.TempDir(), SessionName: "ABC-123", SandboxName: "managed", SandboxKitName: "shell", SandboxEnvFile: "~/env file"})
	if err != nil {
		t.Fatal(err)
	}
	assertSandboxEnvFileArg(t, runner.calls, env)
}

func invalidSandboxEnvFiles(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "unreadable")
	if err := os.WriteFile(unreadable, []byte("opaque secret content"), 0o000); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"missing": filepath.Join(dir, "missing"), "directory": dir, "relative": "env file"}
	file, err := os.Open(unreadable)
	if err == nil {
		file.Close()
		t.Log("current user can read mode-000 files; skipping unreadable case")
	} else {
		paths["unreadable"] = unreadable
	}
	return paths
}

func TestInvalidSandboxEnvFileFailsBeforeProvisioning(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	for name, path := range invalidSandboxEnvFiles(t) {
		t.Run(name, func(t *testing.T) {
			for _, session := range []bool{false, true} {
				repo, root := t.TempDir(), t.TempDir()
				runner := &fakeRunner{repo: repo}
				var err error
				if session {
					_, err = CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: repo, SessionName: "ABC-123", Sandbox: true, SandboxEnvFile: path})
				} else {
					_, err = Create(context.Background(), runner, CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxEnvFile: path, NoteAuthor: testNoteAuthor(t)})
				}
				if err == nil || !strings.Contains(err.Error(), "sbx.env_file") || !strings.Contains(err.Error(), path) {
					t.Fatalf("error = %v", err)
				}
				assertNotCalledContains(t, runner.calls, "sbx", "create")
				assertNotCalledContains(t, runner.calls, "sbx", "rm")
				assertNotCalledContains(t, runner.calls, "git", "worktree add")
				assertNotCalledContains(t, runner.calls, "tmux", "new-session")
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.IsDir() {
						t.Fatalf("workspace provisioned: %s", entry.Name())
					}
				}
			}
		})
	}
}

func TestReconcileSandboxInvalidEnvFileDoesNotRemoveOrSetUpMounts(t *testing.T) {
	for name, path := range invalidSandboxEnvFiles(t) {
		t.Run(name, func(t *testing.T) {
			mount := filepath.Join(t.TempDir(), "not-created")
			runner := &sandboxRetryRunner{name: "ABC-123", exists: true, mounts: []string{"/old-mount"}}
			group := workspacegroup.Workspace{ID: "ABC-123", Path: mount, Sandbox: &workspacegroup.Sandbox{Name: runner.name, Agent: "shell", EnvFile: path, Mounts: []string{mount}}}
			if err := reconcileSandboxWithPolicy(context.Background(), runner, group, nil, sandboxReconcilePolicy{}); err == nil || !strings.Contains(err.Error(), "sbx.env_file") {
				t.Fatalf("error = %v", err)
			}
			if runner.removeCalls != 0 || runner.createCalls != 0 || !runner.exists {
				t.Fatalf("runtime touched: %+v", runner)
			}
			if _, err := os.Stat(mount); !os.IsNotExist(err) {
				t.Fatalf("mount set up: %v", err)
			}
		})
	}
}

func TestSandboxEnvFileRecreationRetriesAndMatchingRuntime(t *testing.T) {
	env := writeSandboxEnvFile(t)
	mount := t.TempDir()
	runner := &sandboxRetryRunner{name: "ABC-123", exists: true, mounts: []string{"/old-mount"}, createFailures: 2}
	group := workspacegroup.Workspace{ID: "ABC-123", Path: mount, Sandbox: &workspacegroup.Sandbox{Name: runner.name, Agent: "shell", EnvFile: env, Mounts: []string{mount}}}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if err := reconcileSandboxWithPolicy(context.Background(), runner, group, logger, sandboxReconcilePolicy{createAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	assertSandboxEnvFileArg(t, runner.calls, env)
	if runner.createCalls != 3 || !reflect.DeepEqual(runner.mounts, []string{mount}) {
		t.Fatalf("runner = %+v", runner)
	}
	if strings.Contains(logs.String(), "opaque secret content") || strings.Contains(logs.String(), env) {
		t.Fatalf("logged env-file: %s", logs.String())
	}
	if err := os.WriteFile(env, []byte("changed opaque secret content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileSandboxWithPolicy(context.Background(), runner, group, logger, sandboxReconcilePolicy{}); err != nil {
		t.Fatal(err)
	}
	if runner.createCalls != 3 {
		t.Fatal("env-file contents change recreated a matching runtime")
	}
	if err := os.Remove(env); err != nil {
		t.Fatal(err)
	}
	// The runtime already matches: deleting or editing a file cannot recreate it.
	if err := reconcileSandboxWithPolicy(context.Background(), runner, group, logger, sandboxReconcilePolicy{}); err != nil {
		t.Fatal(err)
	}
	if runner.createCalls != 3 {
		t.Fatal("matching runtime recreated")
	}
}

func TestSandboxEnvFileAllowsOrdinarySymlink(t *testing.T) {
	env := writeSandboxEnvFile(t)
	link := filepath.Join(t.TempDir(), "env link")
	if err := os.Symlink(env, link); err != nil {
		t.Fatal(err)
	}
	if err := validateSandboxEnvFile(link); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRevisionIncludesSandboxEnvFile(t *testing.T) {
	group := workspacegroup.Workspace{Sandbox: &workspacegroup.Sandbox{Name: "ABC-123", Agent: "shell"}}
	before, err := workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	group.Sandbox.EnvFile = "/recorded env"
	after, err := workspaceRevision(group, nil)
	if err != nil || before == after {
		t.Fatalf("revisions = %s, %s, %v", before, after, err)
	}
}

// Canonical repository queries must preserve the first/second member identity.
type multiRepoEnvFileRunner struct{ *fakeRunner }

func (r multiRepoEnvFileRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "git" && strings.Join(args, " ") == "rev-parse --show-toplevel" {
		r.calls = append(r.calls, call{cwd: cwd, name: name, args: args})
		return cwd, nil
	}
	return r.fakeRunner.Run(ctx, cwd, name, args...)
}

func TestExistingWorkspaceRecoveryUsesRecordedSandboxEnvFile(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	for _, recordedEnv := range []string{"", writeSandboxEnvFile(t)} {
		repo, root := t.TempDir(), t.TempDir()
		runner := &fakeRunner{repo: repo}
		options := CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxKitName: "shell", SandboxEnvFile: recordedEnv, NoteAuthor: testNoteAuthor(t)}
		created, err := Create(context.Background(), runner, options)
		if err != nil {
			t.Fatal(err)
		}
		// Neither changed user settings nor changed repository settings can
		// backfill/replace the recorded env-file during missing-runtime recovery.
		options.SandboxEnvFile = "/missing new user env"
		if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"sbx":{"env_file":"/missing new repo env"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		runner.calls = nil
		if reopened, err := Create(context.Background(), runner, options); err != nil || reopened.Path != created.Path {
			t.Fatalf("reopen = %+v, %v", reopened, err)
		}
		assertSandboxEnvFileArg(t, runner.calls, recordedEnv)
		// An existing tmux session cannot prevent missing-runtime recovery.
		runner.calls = nil
		runner.hasSession = true
		_, group, _, err := RegisteredWorkspace(created.Path, root)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := startWorkspaceRuntime(context.Background(), runner, group, ""); err != nil {
			t.Fatal(err)
		}
		assertSandboxEnvFileArg(t, runner.calls, recordedEnv)
		if group.Sandbox.EnvFile != recordedEnv {
			t.Fatalf("recorded env changed: %+v", group.Sandbox)
		}
		// Mount-driven recreation still uses the original record.
		runner.calls = nil
		group.Sandbox.Mounts = append(group.Sandbox.Mounts, t.TempDir())
		if err := reconcileSandboxWithPolicy(context.Background(), runner, group, nil, sandboxReconcilePolicy{}); err != nil {
			t.Fatal(err)
		}
		assertSandboxEnvFileArg(t, runner.calls, recordedEnv)
	}
}

func TestMissingRecordedEnvFileFailsBeforeRecoveryFilesystemSetup(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	root := t.TempDir()
	anchor := filepath.Join(root, "ABC-123")
	if err := os.Mkdir(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	// A valid canonical note exists but has not yet been linked into the anchor.
	author := testNoteAuthor(t)
	note, err := author.PrepareWorkspaceNote(context.Background(), "ABC-123")
	if err != nil {
		t.Fatal(err)
	}
	if err := author.EnsureWorkspaceNote(context.Background(), note); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{Path: anchor, SessionName: "ABC-123", NotePath: note.Path, Sandbox: &workspacegroup.Sandbox{Name: "ABC-123", Agent: "shell", EnvFile: filepath.Join(root, "missing env")}}
	shared, err := newSharedDirectory(anchor)
	if err != nil {
		t.Fatal(err)
	}
	group.Sandbox.SharedDirectory = shared
	runner := &fakeRunner{}
	if _, _, err := startWorkspaceRuntime(context.Background(), runner, group, ""); err == nil || !strings.Contains(err.Error(), "sbx.env_file") {
		t.Fatalf("recovery error = %v", err)
	}
	assertNotCalledContains(t, runner.calls, "tmux", "new-session")
	assertNotCalledContains(t, runner.calls, "sbx", "create")
	for _, path := range []string{filepath.Join(anchor, "notes.md"), shared} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("resource set up before env validation: %s, %v", path, err)
		}
	}
}

func TestSourceCreateOptionsPropagatesGlobalSandboxEnvFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := config.EnsureFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"sbx":{"enabled":true,"env_file":"/missing generic env file"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	options, err := (Source{}).createOptions(integration.ManagedWorkspaceRequest{Name: "ABC-123"})
	if err != nil || options.SandboxEnvFile != "/missing generic env file" {
		t.Fatalf("options = %+v, %v", options, err)
	}
}

func TestApplyWorkspacePlanPreflightsEnvFileBeforeResources(t *testing.T) {
	root := t.TempDir()
	anchor := filepath.Join(root, "ABC-123")
	for _, env := range []string{"", filepath.Join(root, "missing env")} {
		runner := &fakeRunner{}
		group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "ABC-123", Path: anchor, Sandbox: &workspacegroup.Sandbox{Name: "ABC-123", Agent: "shell", EnvFile: env, Mounts: []string{anchor}}}
		plan := ReconcileWorkspacePlan{WorkspaceID: group.ID, root: root, group: group}
		if env == "" {
			// Stop at the first normal filesystem operation. The env-file change
			// must not introduce a sandbox lookup before that legacy operation.
			plan.noteAdded = true
		}
		request := ReconcileWorkspaceRequest{Desired: DesiredWorkspaceDescription{Note: &DesiredWorkspaceNote{Path: filepath.Join(root, "note.md")}}}
		_, err := applyWorkspacePlan(context.Background(), runner, nil, request, plan)
		if err == nil {
			t.Fatal("expected failed preflight/note link")
		}
		if env == "" {
			if len(runner.calls) != 0 {
				t.Fatalf("env-file-free path added lookups: %+v", runner.calls)
			}
		} else if !strings.Contains(err.Error(), "sbx.env_file") {
			t.Fatalf("error = %v", err)
		}
		assertNotCalledContains(t, runner.calls, "sbx", "rm")
		assertNotCalledContains(t, runner.calls, "sbx", "create")
		if _, err := os.Stat(workspacegroup.Path(root)); !os.IsNotExist(err) {
			t.Fatalf("registry changed before preflight: %v", err)
		}
	}
}

// Simulate a provider echoing an offending env-file value on both output streams.
type sandboxEnvDiagnosticRunner struct {
	sandboxRetryRunner
	value string
}

func (r *sandboxEnvDiagnosticRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	output, err := r.sandboxRetryRunner.Run(ctx, cwd, name, args...)
	if err != nil && name == "sbx" && len(args) > 0 && args[0] == "create" {
		return r.value, fmt.Errorf("%w\ninvalid environment entry: %s", err, r.value)
	}
	return output, err
}

func TestSandboxEnvFileCreationDiagnosticsDoNotExposeValues(t *testing.T) {
	env := writeSandboxEnvFile(t)
	const value = "PRIVATE_ENV_VALUE_do_not_log"
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			envFile := ""
			if configured {
				envFile = env
			}
			runner := &sandboxEnvDiagnosticRunner{sandboxRetryRunner: sandboxRetryRunner{permanentFailure: true}, value: value}
			output, err := startSandboxWithMounts(context.Background(), runner, t.TempDir(), "ABC-123", SandboxKitConfig{Name: "shell"}, envFile, nil)
			if err == nil {
				t.Fatal("expected creation failure")
			}
			if configured {
				if output != "" || strings.Contains(err.Error(), value) || !strings.Contains(err.Error(), "diagnostics withheld") {
					t.Fatalf("unsafe env-file failure: output=%q, error=%v", output, err)
				}
			} else if output != value || !strings.Contains(err.Error(), value) {
				t.Fatalf("ordinary SBX diagnostics changed: output=%q, error=%v", output, err)
			}
		})
	}
}

func TestSandboxEnvFileReconciliationDiagnosticsDoNotExposeValues(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(fmt.Sprint(permanent), func(t *testing.T) {
			env, mount := writeSandboxEnvFile(t), t.TempDir()
			const value = "PRIVATE_ENV_VALUE_do_not_log"
			runner := &sandboxEnvDiagnosticRunner{
				sandboxRetryRunner: sandboxRetryRunner{name: "ABC-123", exists: true, mounts: []string{"/old-mount"}, createFailures: 2, permanentFailure: permanent},
				value:              value,
			}
			group := workspacegroup.Workspace{ID: "ABC-123", Name: "ABC-123", Path: mount, Sandbox: &workspacegroup.Sandbox{Name: runner.name, Agent: "shell", EnvFile: env, Mounts: []string{mount}}}
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			err := reconcileSandboxWithPolicy(context.Background(), runner, group, logger, sandboxReconcilePolicy{createAttempts: 3})
			if permanent {
				if err == nil || runner.createCalls != 1 || !strings.Contains(err.Error(), "diagnostics withheld") {
					t.Fatalf("permanent failure: attempts=%d, error=%v", runner.createCalls, err)
				}
				logRetryableReconciliationFailure(logger, ReconcileWorkspacePlan{group: group}, "sandbox", ReconcileWorkspaceResult{}, err)
				if strings.Contains(err.Error(), value) {
					t.Fatalf("returned env-file value: %v", err)
				}
			} else if err != nil || runner.createCalls != 3 {
				t.Fatalf("transient retry classification changed: attempts=%d, error=%v", runner.createCalls, err)
			}
			if strings.Contains(logs.String(), value) || strings.Contains(logs.String(), env) {
				t.Fatalf("logged env-file diagnostics: %s", logs.String())
			}
			if !strings.Contains(logs.String(), "diagnostics withheld") {
				t.Fatalf("missing safe creation diagnostic: %s", logs.String())
			}
		})
	}
}
