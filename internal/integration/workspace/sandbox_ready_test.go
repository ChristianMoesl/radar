package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
)

const readySecret = "sentinel-secret"

type readyRunner struct {
	Runner
	calls   []call
	lookups []string
	ready   func(context.Context) (string, error)
}

func (r *readyRunner) LookPath(name string) error {
	r.lookups = append(r.lookups, name)
	return r.Runner.LookPath(name)
}

func (r *readyRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	r.calls = append(r.calls, call{cwd: cwd, name: name, args: append([]string(nil), args...)})
	if name == "sbx" && len(args) > 0 && args[0] == "exec" {
		if r.ready != nil {
			return r.ready(ctx)
		}
		return readySecret, nil
	}
	return r.Runner.Run(ctx, cwd, name, args...)
}

func readyCalls(calls []call) []call {
	var result []call
	for _, c := range calls {
		if c.name == "sbx" && len(c.args) > 0 && c.args[0] == "exec" {
			result = append(result, c)
		}
	}
	return result
}

func assertNoReadyLaunches(t *testing.T, calls []call) {
	t.Helper()
	for _, c := range calls {
		if c.name == "tmux" && len(c.args) > 0 && (c.args[0] == "new-session" || c.args[0] == "new-window" || c.args[0] == "switch-client" || c.args[0] == "kill-session") {
			t.Fatalf("launched or switched tmux after readiness failure: %+v", c)
		}
		if c.name == "sbx" && len(c.args) > 0 && c.args[0] == "rm" {
			t.Fatalf("removed provisioned runtime: %+v", c)
		}
	}
}

func assertReadyBeforeLaunches(t *testing.T, calls []call) {
	t.Helper()
	readyIndex, launches := -1, 0
	for i, c := range calls {
		if c.name == "sbx" && len(c.args) > 0 && c.args[0] == "exec" {
			readyIndex = i
		}
		if c.name == "tmux" && len(c.args) > 0 && (c.args[0] == "new-session" || c.args[0] == "new-window" || c.args[0] == "switch-client") {
			launches++
			if readyIndex < 0 || readyIndex >= i {
				t.Fatalf("launch before readiness: %+v", calls)
			}
		}
	}
	if launches == 0 || readyIndex < 0 {
		t.Fatalf("no readiness/launch calls: %+v", calls)
	}
}

func TestRepoSandboxReadyCommandSelectionAndSnapshot(t *testing.T) {
	for _, tt := range []struct {
		name, data string
		want       []string
	}{
		{"inherit absent sbx", `{}`, []string{"global", " global arg "}},
		{"inherit omitted", `{"sbx":{}}`, []string{"global", " global arg "}},
		{"override", `{"sbx":{"ready_command":["repo", "", " repo arg "]}}`, []string{"repo", "", " repo arg "}},
		{"disable", `{"sbx":{"ready_command":[]}}`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(tt.data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadRepoConfig(repo)
			if err != nil {
				t.Fatal(err)
			}
			global := []string{"global", " global arg "}
			selected := workspaceSandboxConfig(cfg, true, "shell", "", "", global, nil)
			if !reflect.DeepEqual(selected.ReadyCommand, tt.want) {
				t.Fatalf("selected = %#v", selected.ReadyCommand)
			}
			global[0] = "changed global"
			if cfg.SBX != nil && cfg.SBX.ReadyCommand != nil {
				// A pointer to [] must survive repo JSON serialization to disable inheritance.
				data, err := json.Marshal(cfg)
				if err != nil || !strings.Contains(string(data), `"ready_command"`) {
					t.Fatalf("repo marshal = %s, %v", data, err)
				}
				if len(*cfg.SBX.ReadyCommand) > 0 {
					(*cfg.SBX.ReadyCommand)[0] = "changed repo"
				}
			}
			if !reflect.DeepEqual(selected.ReadyCommand, tt.want) {
				t.Fatal("selected argv aliases input")
			}
		})
	}
}

func TestWaitForSandboxReadyNoCommandDoesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &readyRunner{Runner: &fakeRunner{}}
	for _, sandbox := range []*workspacegroup.Sandbox{nil, {Name: "ABC-123"}, {Name: "ABC-123", ReadyCommand: []string{}}} {
		if err := waitForSandboxReady(ctx, runner, "/anchor", sandbox); err != nil {
			t.Fatal(err)
		}
	}
	if len(runner.calls) != 0 || len(runner.lookups) != 0 {
		t.Fatalf("calls = %+v, lookups = %#v", runner.calls, runner.lookups)
	}
}

func TestWaitForSandboxReadyPreservesArgvWithoutShellAndBoundsContext(t *testing.T) {
	argv := []string{"/only/in/sandbox", " spaced arg ", "", "$(echo sentinel-secret)", "; touch never"}
	runner := &readyRunner{Runner: &fakeRunner{}, ready: func(ctx context.Context) (string, error) {
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining > 60*time.Second || remaining < 59*time.Second {
			t.Fatalf("deadline = %v, %v", deadline, ok)
		}
		return readySecret, nil
	}}
	anchor := "/anchor with spaces/ABC-123"
	if err := waitForSandboxReady(context.Background(), runner, anchor, &workspacegroup.Sandbox{Name: "ABC-123", ReadyCommand: argv}); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"exec", "--workdir", anchor, "ABC-123"}, argv...)
	if len(runner.calls) != 1 || runner.calls[0].cwd != anchor || runner.calls[0].name != "sbx" || !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("calls = %+v", runner.calls)
	}
	if argv[0] != "/only/in/sandbox" {
		t.Fatal("argv mutated")
	}
	if !reflect.DeepEqual(runner.lookups, []string{"sbx"}) {
		t.Fatalf("looked for command on the host: %#v", runner.lookups)
	}
}

func TestSandboxReadyFailureDiagnosticsAndContext(t *testing.T) {
	for _, mode := range []string{"failure", "timeout", "cancel during exec", "already canceled", "wrapped timeout", "wrapped cancellation"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			var cancel context.CancelFunc
			var wantCause error
			wantText := "failed"
			switch mode {
			case "timeout":
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				wantCause = context.DeadlineExceeded
				wantText = "timed out"
			case "cancel during exec", "already canceled":
				ctx, cancel = context.WithCancel(ctx)
				wantCause = context.Canceled
				wantText = "canceled"
			case "wrapped timeout":
				wantCause = context.DeadlineExceeded
				wantText = "timed out"
			case "wrapped cancellation":
				wantCause = context.Canceled
				wantText = "canceled"
			}
			if cancel != nil {
				defer cancel()
			}
			if mode == "already canceled" {
				cancel()
			}
			runner := &readyRunner{Runner: &fakeRunner{}, ready: func(ctx context.Context) (string, error) {
				if mode == "cancel during exec" {
					cancel()
				}
				if mode == "timeout" || mode == "cancel during exec" {
					<-ctx.Done()
					return readySecret, fmt.Errorf("provider %s: %w", readySecret, ctx.Err())
				}
				if wantCause != nil {
					return readySecret, fmt.Errorf("provider %s: %w", readySecret, wantCause)
				}
				return readySecret, fmt.Errorf("500 Internal Server Error: failed sandbox container %s", readySecret)
			}}
			err := waitForSandboxReady(ctx, runner, "/anchor", &workspacegroup.Sandbox{Name: "ABC-123", ReadyCommand: []string{"ready", readySecret}})
			if err == nil || !isSandboxReadinessError(err) || !strings.Contains(err.Error(), wantText) || strings.Contains(err.Error(), readySecret) {
				t.Fatalf("error = %v", err)
			}
			if wantCause != nil && !errors.Is(err, wantCause) {
				t.Fatalf("context cause lost: %v", err)
			}
			if retryableSandboxCreateError(err) {
				t.Fatalf("readiness treated as transient create error: %v", err)
			}
			if mode == "already canceled" && len(runner.calls) != 0 {
				t.Fatalf("exec after cancellation: %+v", runner.calls)
			}
		})
	}
}

func TestMalformedReadyCommandFailsBeforeProvisioning(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	for _, argv := range [][]string{{""}, {"  "}, {"ready", readySecret + "\x00"}} {
		for _, mode := range []string{"create", "session", "runtime", "reconcile", "apply", "repo"} {
			t.Run(mode+fmt.Sprint(argv), func(t *testing.T) {
				repo, root := t.TempDir(), t.TempDir()
				runner := &readyRunner{Runner: &fakeRunner{repo: repo}}
				group := workspacegroup.Workspace{ID: "ABC-123", Path: filepath.Join(root, "ABC-123"), Sandbox: &workspacegroup.Sandbox{Name: "ABC-123", Agent: "shell", ReadyCommand: argv}}
				var err error
				switch mode {
				case "create":
					_, err = Create(context.Background(), runner, CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxReadyCommand: argv, NoteAuthor: testNoteAuthor(t)})
				case "session":
					_, err = CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: repo, Sandbox: true, SandboxReadyCommand: argv})
				case "runtime":
					_, _, err = startWorkspaceRuntime(context.Background(), runner, group, "")
				case "reconcile":
					err = reconcileSandboxWithPolicy(context.Background(), runner, group, nil, sandboxReconcilePolicy{})
				case "apply":
					_, err = applyWorkspacePlan(context.Background(), runner, nil, ReconcileWorkspaceRequest{}, ReconcileWorkspacePlan{group: group, root: root})
				case "repo":
					data, _ := json.Marshal(RepoConfig{SBX: &SandboxConfig{ReadyCommand: &argv}})
					if err := os.WriteFile(filepath.Join(repo, ".radar.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
					_, err = loadRepoConfig(repo)
				}
				if err == nil || !strings.Contains(err.Error(), "sbx.ready_command") || strings.Contains(err.Error(), readySecret) {
					t.Fatalf("error = %v", err)
				}
				for _, c := range runner.calls {
					if c.name == "sbx" || c.name == "tmux" || (c.name == "git" && len(c.args) > 1 && c.args[0] == "worktree" && c.args[1] == "add") {
						t.Fatalf("resources touched: %+v", c)
					}
				}
				if _, err := os.Stat(group.Path); !os.IsNotExist(err) {
					t.Fatalf("anchor created: %v", err)
				}
			})
		}
	}
}

func TestCreateReadinessGatesPiAndRepositorySetupAndRetainsRuntime(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			repo, root := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"setup":["echo setup"],"sbx":{"ready_command":["repo-ready"," repo arg ",""]}}`), 0600); err != nil {
				t.Fatal(err)
			}
			runner := &readyRunner{Runner: &fakeRunner{repo: repo}}
			if fail {
				runner.ready = func(context.Context) (string, error) { return readySecret, errors.New(readySecret) }
			}
			options := CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxKitName: "shell", SandboxReadyCommand: []string{"global-ready"}, NoteAuthor: testNoteAuthor(t), Switch: true}
			created, err := Create(context.Background(), runner, options)
			if (err != nil) != fail || (err != nil && strings.Contains(err.Error(), readySecret)) {
				t.Fatalf("create = %+v, %v", created, err)
			}
			_, recorded, found, loadErr := RegisteredWorkspace(created.Path, root)
			if loadErr != nil || !found {
				t.Fatalf("record = %+v, %v", recorded, loadErr)
			}
			want := []string{"repo-ready", " repo arg ", ""}
			if !reflect.DeepEqual(recorded.Sandbox.ReadyCommand, want) {
				t.Fatalf("recorded argv = %#v", recorded.Sandbox.ReadyCommand)
			}
			checks := readyCalls(runner.calls)
			if len(checks) != 1 || !reflect.DeepEqual(checks[0].args[4:], want) {
				t.Fatalf("checks = %+v", checks)
			}
			if recorded.Members[0].SetupScheduled == fail {
				t.Fatalf("setup scheduled = %v", recorded.Members[0].SetupScheduled)
			}
			if fail {
				assertNoReadyLaunches(t, runner.calls)
			} else {
				assertReadyBeforeLaunches(t, runner.calls)
				assertCalledContains(t, runner.calls, "tmux", "echo setup")
			}
		})
	}
}

func listedReadySandbox(sandbox *workspacegroup.Sandbox, status string) string {
	data, _ := json.Marshal(map[string]any{"sandboxes": []any{map[string]any{"name": sandbox.Name, "agent": sandbox.Agent, "status": status, "mounts": sandbox.Mounts}}})
	return string(data)
}

func TestRecordedReadyCommandRecoveryAndExistingRuntimeWait(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	for _, argv := range [][]string{nil, {"recorded-ready", " recorded arg "}} {
		for _, state := range []string{"missing", "running", "stopped"} {
			t.Run(fmt.Sprint(argv)+"/"+state, func(t *testing.T) {
				repo, root := t.TempDir(), t.TempDir()
				base := &fakeRunner{repo: repo}
				runner := &readyRunner{Runner: base}
				options := CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxKitName: "shell", SandboxReadyCommand: argv, NoteAuthor: testNoteAuthor(t)}
				created, err := Create(context.Background(), runner, options)
				if err != nil {
					t.Fatal(err)
				}
				_, recorded, _, err := RegisteredWorkspace(created.Path, root)
				if err != nil {
					t.Fatal(err)
				}
				if state != "missing" {
					base.sbxListOutput = listedReadySandbox(recorded.Sandbox, state)
				}
				base.hasSession = true
				// Current global and repository selections must never replace the record,
				// including an old record with no readiness field.
				options.SandboxReadyCommand = []string{"new-global"}
				options.Switch = true
				if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"sbx":{"ready_command":["new-repo"]}}`), 0600); err != nil {
					t.Fatal(err)
				}
				runner.calls = nil
				reopened, err := Create(context.Background(), runner, options)
				if err != nil || reopened.Path != created.Path {
					t.Fatalf("reopen = %+v, %v", reopened, err)
				}
				checks := readyCalls(runner.calls)
				if len(argv) == 0 {
					if len(checks) != 0 {
						t.Fatalf("backfilled readiness: %+v", checks)
					}
				} else {
					if len(checks) != 1 || !reflect.DeepEqual(checks[0].args[4:], argv) {
						t.Fatalf("recovery checks = %+v", checks)
					}
					assertReadyBeforeLaunches(t, runner.calls)
				}
				if state != "missing" {
					assertNotCalledContains(t, runner.calls, "sbx", "create")
				}
			})
		}
	}
}

func TestOpenReadinessFailureRetainsNewRuntimeAndExistingSession(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo, root := t.TempDir(), t.TempDir()
	configPath, err := config.EnsureFile()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"workspace": map[string]any{"root_dir": root}})
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	base := &fakeRunner{repo: repo}
	runner := &readyRunner{Runner: base}
	options := CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxKitName: "shell", SandboxReadyCommand: []string{"ready"}, NoteAuthor: testNoteAuthor(t), Switch: true}
	created, err := Create(context.Background(), runner, options)
	if err != nil {
		t.Fatal(err)
	}
	runner.ready = func(context.Context) (string, error) { return readySecret, errors.New(readySecret) }
	for _, hasSession := range []bool{false, true} {
		base.hasSession = hasSession
		for _, open := range []string{"registered", "create reopen"} {
			runner.calls = nil
			var err error
			if open == "registered" {
				_, err = OpenRegisteredWorkspace(context.Background(), runner, created.Path, true)
			} else {
				_, err = Create(context.Background(), runner, options)
			}
			if err == nil || !isSandboxReadinessError(err) || strings.Contains(err.Error(), readySecret) {
				t.Fatalf("open = %v", err)
			}
			assertCalledContains(t, runner.calls, "sbx", "create")
			assertNoReadyLaunches(t, runner.calls)
		}
	}
}

func TestCreateSessionReadinessBeforeLaunchAndReuse(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	for _, hasSession := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("session=%t/fail=%t", hasSession, fail), func(t *testing.T) {
				path := t.TempDir()
				runner := &readyRunner{Runner: &fakeRunner{hasSession: hasSession, sbxListOutput: `{"sandboxes":[{"name":"ABC-123","status":"stopped"}]}`}}
				if fail {
					runner.ready = func(context.Context) (string, error) { return readySecret, errors.New(readySecret) }
				}
				_, err := CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: path, SessionName: "ABC-123", SandboxName: "ABC-123", SandboxKitName: "shell", SandboxReadyCommand: []string{"/only/in/sandbox", "", " spaced arg "}, Switch: true})
				if (err != nil) != fail {
					t.Fatalf("session = %v", err)
				}
				if fail {
					assertNoReadyLaunches(t, runner.calls)
				} else {
					assertReadyBeforeLaunches(t, runner.calls)
				}
				if len(readyCalls(runner.calls)) != 1 {
					t.Fatalf("checks = %+v", runner.calls)
				}
				assertNotCalledContains(t, runner.calls, "sbx", "create")
			})
		}
	}
	// No configured command keeps the old existing-session fast path: no SBX calls.
	runner := &readyRunner{Runner: &fakeRunner{hasSession: true}}
	if _, err := CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: t.TempDir(), SessionName: "ABC-123", Sandbox: true, Switch: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range runner.calls {
		if c.name == "sbx" {
			t.Fatalf("legacy reuse changed: %+v", runner.calls)
		}
	}
}

func TestSandboxReadinessAfterRecreationRetriesAndOnMatchingReuse(t *testing.T) {
	mount := t.TempDir()
	base := &sandboxRetryRunner{name: "ABC-123", exists: true, mounts: []string{"/old-mount"}, createFailures: 2}
	runner := &readyRunner{Runner: base, ready: func(context.Context) (string, error) {
		return readySecret, errors.New("500 Internal Server Error failed to run sandbox container " + readySecret)
	}}
	group := workspacegroup.Workspace{ID: "ABC-123", Path: mount, Sandbox: &workspacegroup.Sandbox{Name: "ABC-123", Agent: "shell", ReadyCommand: []string{"ready"}, Mounts: []string{mount}}}
	err := reconcileSandboxWithPolicy(context.Background(), runner, group, nil, sandboxReconcilePolicy{createAttempts: 3})
	if err == nil || !isSandboxReadinessError(err) || strings.Contains(err.Error(), readySecret) {
		t.Fatalf("recreate = %v", err)
	}
	if base.createCalls != 3 || base.removeCalls != 3 || !base.exists || len(readyCalls(runner.calls)) != 1 {
		t.Fatalf("runtime = %+v; checks = %+v", base, readyCalls(runner.calls))
	}
	// Retry a readiness failure by checking the provisioned, matching runtime,
	// never by interpreting the provider's transient-looking exec error as create.
	runner.ready = nil
	if err := reconcileSandboxWithPolicy(context.Background(), runner, group, nil, sandboxReconcilePolicy{}); err != nil {
		t.Fatal(err)
	}
	if base.createCalls != 3 || base.removeCalls != 3 || len(readyCalls(runner.calls)) != 2 {
		t.Fatalf("runtime recreated on readiness retry: %+v", base)
	}
}

func TestApplyReadinessFailureIsRetryableAndSetupUnscheduled(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	repo, root := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"setup":["echo setup"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, create := range []bool{false, true} {
		for _, startSession := range []bool{false, true} {
			t.Run(fmt.Sprintf("create=%t/session=%t", create, startSession), func(t *testing.T) {
				base := &fakeRunner{repo: repo}
				runner := &readyRunner{Runner: base}
				options := CreateOptions{Repo: repo, Name: fmt.Sprintf("ABC-123-%t-%t", create, startSession), BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxKitName: "shell", SandboxReadyCommand: []string{"ready"}, NoteAuthor: testNoteAuthor(t)}
				created, err := Create(context.Background(), runner, options)
				if err != nil {
					t.Fatal(err)
				}
				_, group, _, err := RegisteredWorkspace(created.Path, root)
				if err != nil {
					t.Fatal(err)
				}
				group.Members[0].SetupScheduled = false
				if err := registerWorkspace(root, group); err != nil {
					t.Fatal(err)
				}
				base.sbxListOutput = listedReadySandbox(group.Sandbox, "stopped")
				base.hasSession = !startSession
				runner.calls = nil
				runner.ready = func(context.Context) (string, error) { return readySecret, errors.New(readySecret) }
				plan := ReconcileWorkspacePlan{WorkspaceID: group.ID, WorkspaceName: group.Name, group: group, root: root, create: create, startSession: startSession}
				var logs bytes.Buffer
				result, err := applyWorkspacePlan(context.Background(), runner, slog.New(slog.NewTextHandler(&logs, nil)), ReconcileWorkspaceRequest{}, plan)
				if err != nil || result.OK || !result.Retryable || !strings.Contains(result.Error, "readiness") || strings.Contains(result.Error, readySecret) {
					t.Fatalf("result = %+v, %v", result, err)
				}
				if !strings.Contains(logs.String(), "phase=sandbox_ready") || strings.Contains(logs.String(), readySecret) {
					t.Fatalf("logs = %s", logs.String())
				}
				assertNoReadyLaunches(t, runner.calls)
				_, recorded, _, err := RegisteredWorkspace(created.Path, root)
				if err != nil || recorded.Members[0].SetupScheduled {
					t.Fatalf("setup incorrectly scheduled: %+v, %v", recorded, err)
				}
				// Reconciliation retry succeeds without needing a new container.
				runner.calls = nil
				runner.ready = nil
				result, err = applyWorkspacePlan(context.Background(), runner, nil, ReconcileWorkspaceRequest{}, plan)
				if err != nil || !result.OK {
					t.Fatalf("retry = %+v, %v", result, err)
				}
				assertReadyBeforeLaunches(t, runner.calls)
				if len(readyCalls(runner.calls)) != 1 {
					t.Fatalf("checked the same provisioned runtime twice: %+v", readyCalls(runner.calls))
				}
				assertNotCalledContains(t, runner.calls, "sbx", "create")
				assertNotCalledContains(t, runner.calls, "sbx", "rm")
			})
		}
	}
}

func TestSourceCreateOptionsPropagatesSandboxReadyCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := config.EnsureFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"sbx":{"ready_command":["generic-ready"," arg ",""]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	options, err := (Source{}).createOptions(integration.ManagedWorkspaceRequest{Name: "ABC-123"})
	if err != nil || !reflect.DeepEqual(options.SandboxReadyCommand, []string{"generic-ready", " arg ", ""}) {
		t.Fatalf("options = %+v, %v", options, err)
	}
}

func TestWorkspaceRevisionIncludesReadyCommandButEmptyKeepsOldHash(t *testing.T) {
	group := workspacegroup.Workspace{ID: "ABC-123", Name: "ABC-123", Path: "/workspace/ABC-123", Sandbox: &workspacegroup.Sandbox{Name: "ABC-123", Agent: "shell"}}
	before, err := workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Golden revision from the version-2 state before ready_command was added.
	const oldRevision = "9708c20751cf04ba742fb7a09b5969af"
	if before != oldRevision {
		t.Fatalf("old revision = %s, want %s", before, oldRevision)
	}
	group.Sandbox.ReadyCommand = []string{}
	empty, err := workspaceRevision(group, nil)
	if err != nil || empty != before {
		t.Fatalf("empty command changed revision: %s, %v", empty, err)
	}
	group.Sandbox.ReadyCommand = []string{"ready", " spaced arg ", ""}
	after, err := workspaceRevision(group, nil)
	if err != nil || after == before {
		t.Fatalf("ready command missing from revision: %s, %v", after, err)
	}
	group.Sandbox.ReadyCommand[1] = "spaced arg"
	trimmed, err := workspaceRevision(group, nil)
	if err != nil || trimmed == after {
		t.Fatalf("argv spacing missing from revision: %s, %v", trimmed, err)
	}
}

// Model a stateful SBX provider while keeping Git/tmux calls in the existing
// fake runner, so the full reconciliation pipeline can be exercised offline.
type readyPipelineRunner struct {
	*fakeRunner
	sandbox *sandboxRetryRunner
}

func (r readyPipelineRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "sbx" && len(args) > 0 && args[0] != "ports" {
		return r.sandbox.Run(ctx, cwd, name, args...)
	}
	return r.fakeRunner.Run(ctx, cwd, name, args...)
}

func TestApplyMountRecreationReadinessGatesPiAndSetup(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	repo, root := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"setup":["echo setup"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	base := &fakeRunner{repo: repo}
	runner := &readyRunner{Runner: base}
	options := CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxKitName: "shell", SandboxReadyCommand: []string{"recorded-ready", " recorded arg "}, NoteAuthor: testNoteAuthor(t)}
	created, err := Create(context.Background(), runner, options)
	if err != nil {
		t.Fatal(err)
	}
	_, group, _, err := RegisteredWorkspace(created.Path, root)
	if err != nil {
		t.Fatal(err)
	}
	group.Members[0].SetupScheduled = false
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	runtime := &sandboxRetryRunner{name: group.Sandbox.Name, exists: true, mounts: []string{"/previous-mount"}}
	runner.Runner = readyPipelineRunner{fakeRunner: base, sandbox: runtime}
	runner.ready = func(context.Context) (string, error) { return readySecret, errors.New(readySecret) }
	runner.calls = nil
	plan := ReconcileWorkspacePlan{WorkspaceID: group.ID, WorkspaceName: group.Name, group: group, root: root, startSession: true}
	var logs bytes.Buffer
	result, err := applyWorkspacePlan(context.Background(), runner, slog.New(slog.NewTextHandler(&logs, nil)), ReconcileWorkspaceRequest{}, plan)
	if err != nil || result.OK || !result.Retryable || !strings.Contains(result.Error, "readiness") {
		t.Fatalf("recreation = %+v, %v", result, err)
	}
	if runtime.createCalls != 1 || runtime.removeCalls != 1 || !runtime.exists {
		t.Fatalf("recreated runtime lost: %+v", runtime)
	}
	checks := readyCalls(runner.calls)
	if len(checks) != 1 || !reflect.DeepEqual(checks[0].args[4:], group.Sandbox.ReadyCommand) {
		t.Fatalf("checks = %+v", checks)
	}
	readySeen := false
	for _, c := range runner.calls {
		if c.name == "sbx" && len(c.args) > 0 && c.args[0] == "exec" {
			readySeen = true
		}
		if readySeen && c.name == "sbx" && len(c.args) > 0 && (c.args[0] == "rm" || c.args[0] == "create") {
			t.Fatalf("readiness failure entered create retries: %+v", runner.calls)
		}
		if c.name == "tmux" {
			t.Fatalf("session launched after readiness failure: %+v", c)
		}
	}
	if !strings.Contains(logs.String(), "phase=sandbox_ready") || strings.Contains(logs.String(), readySecret) {
		t.Fatalf("logs = %s", logs.String())
	}
	_, recorded, _, err := RegisteredWorkspace(created.Path, root)
	if err != nil || recorded.Members[0].SetupScheduled {
		t.Fatalf("setup scheduled on failure: %+v, %v", recorded, err)
	}
	runner.ready = nil
	runner.calls = nil
	result, err = applyWorkspacePlan(context.Background(), runner, nil, ReconcileWorkspaceRequest{}, plan)
	if err != nil || !result.OK {
		t.Fatalf("retry = %+v, %v", result, err)
	}
	assertReadyBeforeLaunches(t, runner.calls)
	if len(readyCalls(runner.calls)) != 1 {
		t.Fatalf("checked the same recreated runtime twice: %+v", readyCalls(runner.calls))
	}
	assertCalledContains(t, runner.calls, "tmux", "echo setup")
	if runtime.createCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf("good container recreated during retry: %+v", runtime)
	}
}

func TestCreateAndSessionPreserveSafeReadinessContextCauses(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, session := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/session=%t", cause, session), func(t *testing.T) {
				repo, root := t.TempDir(), t.TempDir()
				runner := &readyRunner{Runner: &fakeRunner{repo: repo}, ready: func(context.Context) (string, error) { return readySecret, fmt.Errorf("%s: %w", readySecret, cause) }}
				var err error
				if session {
					_, err = CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: repo, SessionName: "ABC-123", Sandbox: true, SandboxReadyCommand: []string{"ready"}, Switch: true})
				} else {
					_, err = Create(context.Background(), runner, CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxReadyCommand: []string{"ready"}, NoteAuthor: testNoteAuthor(t), Switch: true})
				}
				if !errors.Is(err, cause) || strings.Contains(err.Error(), readySecret) {
					t.Fatalf("cause lost or private data exposed: %v", err)
				}
				assertNoReadyLaunches(t, runner.calls)
			})
		}
	}
}

func TestRuntimeRecoveredAfterReconciliationStillChecksReadiness(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	repo, root := t.TempDir(), t.TempDir()
	base := &fakeRunner{repo: repo}
	runner := &readyRunner{Runner: base}
	created, err := Create(context.Background(), runner, CreateOptions{Repo: repo, Name: "ABC-123", BranchMode: integration.WorkspaceBranchNew, Base: "origin/main", WorkspaceRoot: root, Sandbox: true, SandboxReadyCommand: []string{"ready"}, NoteAuthor: testNoteAuthor(t)})
	if err != nil {
		t.Fatal(err)
	}
	_, group, _, err := RegisteredWorkspace(created.Path, root)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate removal by an external actor between successful reconciliation
	// and launching the runtime. An earlier readiness result cannot cover it.
	runner.calls = nil
	runner.ready = func(context.Context) (string, error) { return readySecret, errors.New(readySecret) }
	session, sandbox, err := startWorkspaceRuntimeWithReadiness(context.Background(), runner, group, "", true)
	if err == nil || !isSandboxReadinessError(err) || session || !sandbox {
		t.Fatalf("recovery = %t, %t, %v", session, sandbox, err)
	}
	if len(readyCalls(runner.calls)) != 1 {
		t.Fatalf("recovered runtime did not wait: %+v", runner.calls)
	}
	assertCalledContains(t, runner.calls, "sbx", "create")
	assertNoReadyLaunches(t, runner.calls)
}
