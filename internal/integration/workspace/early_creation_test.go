package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/integration"
	sessionlayout "radar/internal/integration/tmux/layout"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/pi"
)

// These barriers stop the provider, not the test scheduler. Nothing contacts
// real Git, tmux, SBX, or the user's note vault.
type earlyCreationBarrier struct {
	entered chan struct{}
	release chan struct{}
	err     error
}

func newEarlyCreationBarrier() *earlyCreationBarrier {
	return &earlyCreationBarrier{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *earlyCreationBarrier) run(ctx context.Context) (string, error) {
	close(b.entered)
	select {
	case <-b.release:
		return "", b.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type earlyCreationRunner struct {
	*fakeRunner
	create, ready *earlyCreationBarrier
	switchErr     error

	mu           sync.Mutex
	calls        []call
	pending      map[string]string
	window, pane int
	launchErr    error
	wantMembers  int
}

func (r *earlyCreationRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call{cwd: cwd, name: name, args: append([]string(nil), args...)})
	if name == "tmux" && len(args) > 0 {
		switch args[0] {
		case "new-session", "new-window", "split-window":
			if args[0] == "new-session" {
				// Inspect at the actual launch boundary, not just later when
				// create is blocked: Pi must see the complete registration.
				_, r.launchErr = earlyCreationResources(filepath.Dir(cwd), cwd, r.wantMembers)
			}
			if args[0] != "split-window" {
				r.window++
			}
			r.pane++
			output := fmt.Sprintf("@%d %%%d", r.window, r.pane)
			r.mu.Unlock()
			return output, nil
		case "set-option":
			if len(args) == 6 && args[4] == workspacePaneGateOption {
				r.pending[args[3]] = args[5]
			}
		case "list-panes":
			var lines []string
			for pane, gate := range r.pending {
				lines = append(lines, pane+" "+gate)
			}
			sort.Strings(lines)
			r.mu.Unlock()
			return strings.Join(lines, "\n"), nil
		case "wait-for":
			if len(args) == 3 && args[1] == "-S" {
				for pane, gate := range r.pending {
					if gate == args[2] {
						delete(r.pending, pane)
					}
				}
			}
		case "switch-client":
			r.mu.Unlock()
			return "", r.switchErr
		}
	}
	r.mu.Unlock()
	if name == "sbx" && len(args) > 0 {
		if args[0] == "create" && r.create != nil {
			return r.create.run(ctx)
		}
		if args[0] == "exec" && r.ready != nil {
			return r.ready.run(ctx)
		}
	}
	// Canonicalize each fake repository independently for multi-member tests.
	if name == "git" && strings.Join(args, " ") == "rev-parse --show-toplevel" {
		return cwd, nil
	}
	if name == "git" && strings.Join(args, " ") == "rev-parse --path-format=absolute --git-common-dir" {
		return filepath.Join(cwd, ".git"), nil
	}
	return r.fakeRunner.Run(ctx, cwd, name, args...)
}

func (r *earlyCreationRunner) snapshot() ([]call, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]call(nil), r.calls...), len(r.pending), r.launchErr
}

func earlyCreationFixture(t *testing.T, members int) (*earlyCreationRunner, CreateOptions) {
	t.Helper()
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	options := CreateOptions{
		Name: "ABC-123", Path: filepath.Join(root, "ABC-123"), SessionName: "ABC-123", WorkspaceRoot: root,
		NoteAuthor: testNoteAuthor(t), Sandbox: true, SandboxKitName: "shell", SandboxReadyCommand: []string{"ready"}, Switch: true,
		Tmux: sessionlayout.Config{Windows: []sessionlayout.Window{
			{Name: "work", Layout: "horizontal", Panes: []sessionlayout.Pane{{Command: "echo auxiliary"}, {Command: "pi " + sessionlayout.PiArgsPlaceholder}}},
			{Name: "editor", Panes: []sessionlayout.Pane{{Command: "nvim ."}}},
		}},
	}
	for i := range members {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"setup":["echo setup"]}`), 0600); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			options.Repo, options.BranchMode, options.Base = repo, integration.WorkspaceBranchNew, "origin/main"
		} else {
			options.Worktrees = append(options.Worktrees, DesiredWorkspaceWorktree{
				Repository: repo, BranchMode: integration.WorkspaceBranchNew, Name: "ABC-123", Base: "origin/main",
			})
		}
	}
	return &earlyCreationRunner{fakeRunner: &fakeRunner{}, create: newEarlyCreationBarrier(), ready: newEarlyCreationBarrier(), pending: map[string]string{}, wantMembers: members}, options
}

func earlyCreationResources(root, anchor string, members int) (workspacegroup.Workspace, error) {
	_, group, found, err := RegisteredWorkspace(anchor, root)
	if err != nil || !found {
		return group, fmt.Errorf("registration missing: found=%t, error=%v", found, err)
	}
	if len(group.Members) != members || group.NotePath == "" || group.Sandbox == nil || group.Sandbox.SharedDirectory == "" {
		return group, fmt.Errorf("incomplete early registration: %+v", group)
	}
	target, err := os.Readlink(filepath.Join(anchor, "notes.md"))
	if err != nil || target != group.NotePath {
		return group, fmt.Errorf("canonical note link = %q, error=%v", target, err)
	}
	paths := []string{anchor, group.NotePath, group.Sandbox.SharedDirectory}
	for _, member := range group.Members {
		paths = append(paths, member.Path)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return group, fmt.Errorf("host resource %s missing: %w", path, err)
		}
	}
	return group, nil
}

type earlyCreationResult struct {
	workspace Workspace
	err       error
}

func startEarlyCreation(t *testing.T, runner Runner, options CreateOptions) (context.Context, context.CancelFunc, <-chan earlyCreationResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	result, finished := make(chan earlyCreationResult, 1), make(chan struct{})
	go func() {
		defer close(finished)
		workspace, err := Create(ctx, runner, options)
		result <- earlyCreationResult{workspace, err}
	}()
	// Join before fixture/environment cleanup, including assertion failures.
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("creation did not stop after cancellation")
		}
	})
	return ctx, cancel, result
}

func awaitEarlyCreationPhase(t *testing.T, ctx context.Context, phase <-chan struct{}, result <-chan earlyCreationResult) {
	t.Helper()
	select {
	case <-phase:
	case got := <-result:
		t.Fatalf("creation returned before provider barrier: %+v, %v", got.workspace, got.err)
	case <-ctx.Done():
		t.Fatalf("creation did not reach provider barrier: %v", ctx.Err())
	}
}

func awaitEarlyCreationResult(t *testing.T, result <-chan earlyCreationResult) earlyCreationResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("creation did not return after provider completion")
		return earlyCreationResult{}
	}
}

func assertEarlyCreationBlocked(t *testing.T, runner *earlyCreationRunner, result <-chan earlyCreationResult) []call {
	t.Helper()
	select {
	case got := <-result:
		t.Fatalf("creation returned while provisioning was blocked: %+v, %v", got.workspace, got.err)
	default:
	}
	calls, pending, launchErr := runner.snapshot()
	if launchErr != nil || pending != 2 {
		t.Fatalf("early host resources/layout incomplete: pending=%d, error=%v", pending, launchErr)
	}
	assertNotCalledContains(t, calls, "sbx", "ports")
	assertNotCalledContains(t, calls, "tmux", "echo setup")
	assertNotCalledContains(t, calls, "tmux", "wait-for -S")
	return calls
}

func assertEarlyCreationOrder(t *testing.T, calls []call, order ...call) {
	t.Helper()
	for i := 1; i < len(order); i++ {
		assertCallOrder(t, calls, order[i-1], order[i])
	}
}

func TestEarlyCreationSwitchAndPiPrecedeProvisioning(t *testing.T) {
	for _, tt := range []struct {
		name       string
		members    int
		switchOff  bool
		switchFail bool
		fork       string
	}{
		{name: "note only"},
		{name: "all members and fork", members: 2, fork: "parent session"},
		{name: "without switch", members: 1, switchOff: true},
		{name: "switch failure still provisions", members: 1, switchFail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner, options := earlyCreationFixture(t, tt.members)
			options.Switch, options.ForkPiSession = !tt.switchOff, tt.fork
			if tt.switchFail {
				runner.switchErr = errors.New("client cannot switch")
			}
			guard, err := pi.RequireSandboxPath()
			if err != nil {
				t.Fatal(err)
			}
			ctx, _, result := startEarlyCreation(t, runner, options)
			awaitEarlyCreationPhase(t, ctx, runner.create.entered, result)
			calls := assertEarlyCreationBlocked(t, runner, result)
			early, err := earlyCreationResources(options.WorkspaceRoot, options.Path, tt.members)
			if err != nil {
				t.Fatal(err)
			}
			for _, member := range early.Members {
				if member.SetupScheduled {
					t.Fatalf("setup marked complete before provisioning: %+v", member)
				}
			}
			order := []call{{name: "tmux", args: []string{"new-session"}}, {name: "tmux", args: []string{"select-pane", "-t", "%2"}}}
			if options.Switch {
				order = append(order, call{name: "tmux", args: []string{"switch-client", "-t", options.SessionName}})
			} else {
				assertNotCalledContains(t, calls, "tmux", "switch-client")
			}
			order = append(order, call{name: "sbx", args: []string{"create"}})
			assertEarlyCreationOrder(t, calls, order...)
			assertNotCalledContains(t, calls, "sbx", "exec")
			piPanes, gatedPanes := 0, 0
			for _, c := range calls {
				if c.name != "tmux" || (c.args[0] != "new-session" && c.args[0] != "new-window" && c.args[0] != "split-window") {
					continue
				}
				command := c.args[len(c.args)-1]
				if strings.HasPrefix(command, "pi ") || strings.Contains(command, "\npi ") {
					piPanes++
					if !strings.Contains(command, "--extension "+shellQuote(guard)) || strings.Contains(command, "tmux wait-for") || strings.Contains(command, sessionlayout.PiArgsPlaceholder) {
						t.Fatalf("Pi not launched early with its required guard: %q", command)
					}
					if tt.fork != "" && !strings.Contains(command, "--fork "+shellQuote(tt.fork)) {
						t.Fatalf("early Pi lost fork: %q", command)
					}
				} else if strings.HasPrefix(command, "tmux wait-for ") {
					gatedPanes++
				} else {
					t.Fatalf("unmarked pane can execute before readiness: %q", command)
				}
			}
			if piPanes != 1 || gatedPanes != 2 {
				t.Fatalf("early pane commands: Pi=%d, gated=%d", piPanes, gatedPanes)
			}

			close(runner.create.release)
			awaitEarlyCreationPhase(t, ctx, runner.ready.entered, result)
			calls = assertEarlyCreationBlocked(t, runner, result)
			assertEarlyCreationOrder(t, calls, call{name: "tmux", args: []string{"new-session"}}, call{name: "sbx", args: []string{"create"}}, call{name: "sbx", args: []string{"exec"}})
			close(runner.ready.release)
			got := awaitEarlyCreationResult(t, result)
			if !errors.Is(got.err, runner.switchErr) || got.workspace.Path != options.Path {
				t.Fatalf("completed creation = %+v, %v", got.workspace, got.err)
			}
			calls, pending, _ := runner.snapshot()
			if pending != 0 {
				t.Fatalf("ready panes remain gated: %d", pending)
			}
			signals := map[string]bool{}
			for _, c := range calls {
				if c.name == "tmux" && len(c.args) == 3 && c.args[0] == "wait-for" && c.args[1] == "-S" {
					if signals[c.args[2]] {
						t.Fatalf("pane gate signaled twice: %+v", c)
					}
					signals[c.args[2]] = true
				}
			}
			if len(signals) != 2 {
				t.Fatalf("ready pane signals = %v", signals)
			}
			assertEarlyCreationOrder(t, calls, call{name: "sbx", args: []string{"exec"}}, call{name: "tmux", args: []string{"wait-for", "-S"}}, call{name: "sbx", args: []string{"ports"}})
			group, err := earlyCreationResources(options.WorkspaceRoot, options.Path, tt.members)
			if err != nil {
				t.Fatal(err)
			}
			for _, member := range group.Members {
				if !member.SetupScheduled {
					t.Fatalf("setup not scheduled after readiness: %+v", member)
				}
				assertCallOrder(t, calls, call{name: "sbx", args: []string{"exec"}}, call{name: "tmux", args: []string{"new-window", "-t", options.SessionName + ":", "-d", "-n", "setup-" + filepath.Base(member.Repository)}})
			}
			if tt.members > 0 {
				assertCalledContains(t, calls, "tmux", "echo setup")
			}
		})
	}
}

func TestEarlyCreationLateFailureRetainsConversationAndResources(t *testing.T) {
	for _, tt := range []struct {
		name   string
		ready  bool
		cancel bool
	}{
		{name: "create failure"},
		{name: "create cancellation", cancel: true},
		{name: "readiness failure", ready: true},
		{name: "readiness cancellation", ready: true, cancel: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner, options := earlyCreationFixture(t, 1)
			barrier := runner.create
			if tt.ready {
				barrier = runner.ready
			}
			barrier.err = errors.New("provider failed")
			ctx, cancel, result := startEarlyCreation(t, runner, options)
			awaitEarlyCreationPhase(t, ctx, runner.create.entered, result)
			assertEarlyCreationBlocked(t, runner, result)
			before, err := earlyCreationResources(options.WorkspaceRoot, options.Path, 1)
			if err != nil {
				t.Fatal(err)
			}
			if tt.ready {
				close(runner.create.release)
				awaitEarlyCreationPhase(t, ctx, runner.ready.entered, result)
				assertEarlyCreationBlocked(t, runner, result)
			}
			if tt.cancel {
				cancel()
			} else {
				close(barrier.release)
			}
			got := awaitEarlyCreationResult(t, result)
			if got.err == nil || got.workspace.Path != options.Path || (tt.ready && !isSandboxReadinessError(got.err)) || (tt.ready && tt.cancel && !errors.Is(got.err, context.Canceled)) {
				t.Fatalf("late failure = %+v, %v", got.workspace, got.err)
			}
			after, err := earlyCreationResources(options.WorkspaceRoot, options.Path, 1)
			if err != nil || !reflect.DeepEqual(before, after) || after.Members[0].SetupScheduled {
				t.Fatalf("late failure altered resources: before=%+v after=%+v error=%v", before, after, err)
			}
			calls, pending, launchErr := runner.snapshot()
			if pending != 2 || launchErr != nil {
				t.Fatalf("late failure lost recoverable layout: pending=%d error=%v", pending, launchErr)
			}
			assertCalledContains(t, calls, "tmux", "new-session")
			assertCalledContains(t, calls, "tmux", "switch-client")
			assertCalledContains(t, calls, "tmux", "display-message")
			assertNotCalledContains(t, calls, "tmux", "kill-session")
			assertNotCalledContains(t, calls, "tmux", "wait-for -S")
			assertNotCalledContains(t, calls, "tmux", "echo setup")
			assertNotCalledContains(t, calls, "sbx", "ports")
			assertNotCalledContains(t, calls, "sbx", "rm")
			assertNotCalledContains(t, calls, "git", "worktree remove")
			if !tt.ready {
				assertNotCalledContains(t, calls, "sbx", "exec")
			}
		})
	}
}

func TestEarlyCreationRejectsExistingUnguardedSession(t *testing.T) {
	runner, options := earlyCreationFixture(t, 1)
	runner.fakeRunner.hasSession = true
	created, err := Create(context.Background(), runner, options)
	if err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "guard") || created.Path != "" {
		t.Fatalf("colliding session reused: %+v, %v", created, err)
	}
	if _, _, found, err := RegisteredWorkspace(options.Path, options.WorkspaceRoot); err != nil || found {
		t.Fatalf("collision persisted session ownership: found=%t err=%v", found, err)
	}
	if _, err := Create(context.Background(), runner, options); err == nil {
		t.Fatal("retry reused colliding session")
	}
	calls, _, _ := runner.snapshot()
	for _, command := range []string{"new-session", "new-window", "switch-client", "kill-session", "echo setup", "wait-for -S"} {
		assertNotCalledContains(t, calls, "tmux", command)
	}
	for _, command := range []string{"create", "exec", "ports", "rm"} {
		assertNotCalledContains(t, calls, "sbx", command)
	}
}
