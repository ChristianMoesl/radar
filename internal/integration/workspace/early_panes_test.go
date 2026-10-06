package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sessionlayout "radar/internal/integration/tmux/layout"
)

type paneGateRunner struct {
	calls   []call
	pending map[string]string
	window  int
	pane    int
	fail    func([]string) error
	output  func([]string) (string, bool)
}

func (*paneGateRunner) LookPath(string) error { return nil }

func (r *paneGateRunner) Run(_ context.Context, cwd, name string, args ...string) (string, error) {
	r.calls = append(r.calls, call{cwd: cwd, name: name, args: args})
	if r.fail != nil {
		if err := r.fail(args); err != nil {
			return "", err
		}
	}
	if r.output != nil {
		if output, ok := r.output(args); ok {
			return output, nil
		}
	}
	switch args[0] {
	case "new-session", "new-window", "split-window":
		if args[0] != "split-window" {
			r.window++
		}
		r.pane++
		return fmt.Sprintf("@%d %%%d", r.window, r.pane), nil
	case "set-option":
		if r.pending == nil {
			r.pending = map[string]string{}
		}
		r.pending[args[3]] = args[5]
	case "list-panes":
		var lines []string
		for pane, gate := range r.pending {
			lines = append(lines, pane+" "+gate)
		}
		sort.Strings(lines)
		return strings.Join(lines, "\n"), nil
	case "wait-for":
		for pane, gate := range r.pending {
			if gate == args[2] {
				delete(r.pending, pane) // The waiting pane consumes the signal.
			}
		}
	}
	return "", nil
}

func TestEarlyTmuxWorkspacePreservesLayoutAndPiPosition(t *testing.T) {
	for _, layout := range []string{"", "horizontal", "vertical", "main-horizontal", "main-vertical", "tiled"} {
		for piIndex := range 5 {
			t.Run(fmt.Sprintf("layout=%s/pi=%d", layout, piIndex), func(t *testing.T) {
				commands := []string{"echo 'first; $x'", "exec editor .", "printf '%s\\n' \"a'b\"", "cat <<'EOF'\nraw $value\nEOF", "echo last # comment"}
				commands[piIndex] = "custom-pi --name \"a'b\" " + sessionlayout.PiArgsPlaceholder
				cfg := sessionlayout.Config{Windows: []sessionlayout.Window{
					{Name: "one", Layout: layout, Panes: []sessionlayout.Pane{{Command: commands[0]}, {Command: commands[1]}, {Command: commands[2]}}},
					{Name: "two", Layout: layout, Panes: []sessionlayout.Pane{{Command: commands[3]}, {Command: commands[4]}}},
				}}
				normal, early := &paneGateRunner{}, &paneGateRunner{}
				ctx := context.Background()
				if err := createTmuxWorkspace(ctx, normal, "/creator", "/workspace", "test", cfg, "--name 'test'", nil); err != nil {
					t.Fatal(err)
				}
				if err := createEarlyTmuxWorkspace(ctx, early, "/creator", "/workspace", "test", cfg, "--name 'test'"); err != nil {
					t.Fatal(err)
				}
				var ungated []call
				paneIndex := 0
				gates := map[string]bool{}
				for i, c := range early.calls {
					if c.cwd != "/creator" || c.name != "tmux" {
						t.Fatalf("wrong execution context: %+v", c)
					}
					if c.args[0] == "set-option" {
						continue
					}
					args := append([]string(nil), c.args...)
					switch args[0] {
					case "new-session", "new-window", "split-window":
						command := args[len(args)-1]
						want := expandPiArgs(commands[paneIndex], "--name 'test'")
						if paneIndex == piIndex {
							if command != "tmux set-option -p -t \"$TMUX_PANE\" remain-on-exit failed || exit\n"+want {
								t.Fatalf("Pi command changed or failure diagnostic not retained: %q, want suffix %q", command, want)
							}
						} else {
							if !strings.HasSuffix(command, "\n"+want) || !strings.HasPrefix(command, "tmux wait-for '") {
								t.Fatalf("auxiliary command not gated intact: %q", command)
							}
							option := early.calls[i+1]
							wantPrefix := []string{"set-option", "-p", "-t", fmt.Sprintf("%%%d", paneIndex+1), workspacePaneGateOption}
							if len(option.args) != 6 || !reflect.DeepEqual(option.args[:5], wantPrefix) {
								t.Fatalf("gate not stored immediately after pane creation: %+v", option)
							}
							gate := option.args[5]
							if gates[gate] || !strings.Contains(command, shellQuote(gate)) {
								t.Fatalf("gate not unique or not used by pane: %q", gate)
							}
							gates[gate] = true
						}
						args[len(args)-1] = want
						paneIndex++
					}
					ungated = append(ungated, call{cwd: c.cwd, name: c.name, args: args})
				}
				if !reflect.DeepEqual(ungated, normal.calls) || len(early.pending) != 4 {
					t.Fatalf("early layout/order differs from normal: early=%+v normal=%+v pending=%v", ungated, normal.calls, early.pending)
				}
			})
		}
	}
}

func TestEarlyTmuxWorkspaceDefaultsAndFreshGates(t *testing.T) {
	var previous string
	for range 2 {
		runner := &paneGateRunner{}
		if err := createEarlyTmuxWorkspace(context.Background(), runner, "", "/workspace", "same-name", sessionlayout.Config{}, "--name test"); err != nil {
			t.Fatal(err)
		}
		if len(runner.pending) != 1 || runner.pending["%2"] == "" {
			t.Fatalf("default auxiliary gate = %v", runner.pending)
		}
		gate := runner.pending["%2"]
		if gate == previous {
			t.Fatal("recreated session reused an old signal identity")
		}
		previous = gate
	}
}

func TestEarlyTmuxWorkspaceCleansUpConstructorFailures(t *testing.T) {
	cfg := sessionlayout.Config{Windows: []sessionlayout.Window{
		{Name: "aux", Layout: "tiled", Panes: []sessionlayout.Pane{{Command: "editor ."}, {Command: "tail -f log"}}},
		{Name: "pi", Panes: []sessionlayout.Pane{{Command: "pi " + sessionlayout.PiArgsPlaceholder}}},
	}}
	for _, failure := range []string{"new-session", "parse", "first-option", "split-option", "split-window", "new-window", "select-layout", "select-window", "select-pane"} {
		t.Run(failure, func(t *testing.T) {
			sentinel := errors.New("constructor failed")
			runner := &paneGateRunner{fail: func(args []string) error {
				if args[0] == failure || (args[0] == "set-option" && ((failure == "first-option" && args[3] == "%1") || (failure == "split-option" && args[3] == "%2"))) {
					return sentinel
				}
				return nil
			}}
			if failure == "parse" {
				runner.output = func(args []string) (string, bool) { return "bad IDs", args[0] == "new-session" }
			}
			err := createEarlyTmuxWorkspace(context.Background(), runner, "", "/workspace", "test", cfg, "")
			if err == nil || (failure != "parse" && !errors.Is(err, sentinel)) {
				t.Fatalf("constructor error = %v", err)
			}
			last := runner.calls[len(runner.calls)-1]
			if failure == "new-session" {
				if len(runner.calls) != 1 {
					t.Fatalf("attempted cleanup of uncreated session: %+v", runner.calls)
				}
			} else if !reflect.DeepEqual(last.args, []string{"kill-session", "-t", "test"}) {
				t.Fatalf("missing constructor cleanup: %+v", runner.calls)
			}
		})
	}
	invalid := sessionlayout.Config{Windows: []sessionlayout.Window{{Name: "invalid", Panes: []sessionlayout.Pane{{Command: "no Pi"}}}}}
	runner := &paneGateRunner{}
	if err := createEarlyTmuxWorkspace(context.Background(), runner, "", "/workspace", "test", invalid, ""); err == nil || len(runner.calls) != 0 {
		t.Fatalf("invalid configuration launched panes: err=%v calls=%+v", err, runner.calls)
	}
}

func TestReleaseWorkspacePanesRecoveryNeverReplaysCommands(t *testing.T) {
	runner := &paneGateRunner{pending: map[string]string{"%1": "radar-pane-first", "%3": "radar-pane-second"}}
	sentinel := errors.New("interrupted release")
	runner.fail = func(args []string) error {
		if args[0] == "wait-for" && args[2] == "radar-pane-second" {
			return sentinel
		}
		return nil
	}
	err := releaseWorkspacePanes(context.Background(), runner, "/workspace", "test")
	if !isWorkspacePaneError(fmt.Errorf("runtime: %w", err)) || !errors.Is(err, sentinel) {
		t.Fatalf("missing late-release classification/cause: %v", err)
	}
	if len(runner.pending) != 1 || runner.pending["%3"] != "radar-pane-second" {
		t.Fatalf("failed release lost recoverable gate: %v", runner.pending)
	}
	runner.fail = nil
	if err := releaseWorkspacePanes(context.Background(), runner, "/workspace", "test"); err != nil {
		t.Fatal(err)
	}
	before := len(runner.calls)
	if err := releaseWorkspacePanes(context.Background(), runner, "/workspace", "test"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != before+1 || len(runner.pending) != 0 {
		t.Fatalf("repeat release did more than inspect pending options: %+v", runner.calls[before:])
	}
	for _, c := range runner.calls {
		if c.cwd != "/workspace" || (c.args[0] != "list-panes" && c.args[0] != "wait-for") {
			t.Fatalf("release mutated layout, replayed or respawned a command: %+v", c)
		}
	}
	wantList := []string{"list-panes", "-s", "-t", "test", "-f", "#{!=:#{" + workspacePaneGateOption + "},}", "-F", "#{pane_id} #{" + workspacePaneGateOption + "}"}
	if !reflect.DeepEqual(runner.calls[0].args, wantList) {
		t.Fatalf("release didn't list only pending pane options: %+v", runner.calls[0])
	}
	if isWorkspacePaneError(nil) || isWorkspacePaneError(sentinel) {
		t.Fatal("unrelated errors classified as pane-release failures")
	}
}

func TestReleaseWorkspacePanesInspectionFailures(t *testing.T) {
	for _, output := range []string{"", "%1", "%1 unrelated", "bad radar-pane-gate", "%1 radar-pane-gate extra"} {
		t.Run(output, func(t *testing.T) {
			runner := &paneGateRunner{output: func([]string) (string, bool) { return output, true }}
			err := releaseWorkspacePanes(context.Background(), runner, "/workspace", "test")
			if output == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !isWorkspacePaneError(err) {
				t.Fatalf("malformed option output not classified: %v", err)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("malformed output signalled a gate: %+v", runner.calls)
			}
		})
	}
	runner := &paneGateRunner{fail: func([]string) error { return context.Canceled }}
	if err := releaseWorkspacePanes(context.Background(), runner, "/workspace", "test"); !isWorkspacePaneError(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("inspection cancellation = %v", err)
	}
}

// Every tmux command targets a fresh socket and ignores user configuration.
// Inner pane clients discover this socket through tmux's own TMUX environment.
// No test command, including cleanup, can contact the user's tmux server.
type isolatedPaneTmux struct{ socket string }

func (isolatedPaneTmux) LookPath(name string) error { return ExecRunner{}.LookPath(name) }
func (r isolatedPaneTmux) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name != "tmux" {
		return "", fmt.Errorf("isolated tmux fixture cannot run %q", name)
	}
	return ExecRunner{}.Run(ctx, cwd, name, append([]string{"-S", r.socket, "-f", "/dev/null"}, args...)...)
}

func newIsolatedPaneTmux(t *testing.T) isolatedPaneTmux {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available for original-shell proof")
	}
	// Keep the Unix socket path short regardless of test names or host TMPDIR.
	dir, err := os.MkdirTemp("/tmp", "radar-tmux-")
	if err != nil {
		t.Fatal(err)
	}
	runner := isolatedPaneTmux{socket: filepath.Join(dir, "s")}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = runner.Run(ctx, "", "tmux", "kill-server")
		_ = os.RemoveAll(dir)
	})
	ctx := context.Background()
	if _, err := runner.Run(ctx, "", "tmux", "new-session", "-d", "-s", "fixture", "-x", "180", "-y", "60", "sleep 60"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"set-option", "-g", "default-shell", bash}, {"set-option", "-g", "remain-on-exit", "on"}} {
		if _, err := runner.Run(ctx, "", "tmux", args...); err != nil {
			t.Fatal(err)
		}
	}
	return runner
}

func waitPaneCondition(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for isolated tmux pane")
}

func TestEarlyPanesRealTmuxOriginalShellQuotingAndOneShotRecovery(t *testing.T) {
	runner := newIsolatedPaneTmux(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cwd with 'quotes'")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(path, "aux result")
	piOutput := filepath.Join(path, "pi result")
	original := "test -n \"$BASH_VERSION\" || exit 9\n" +
		"printf '%s\\n' \"$PWD\" >> " + shellQuote(output) + "\n" +
		"cat <<'EOF' >> " + shellQuote(output) + "\n" +
		"single ' double \" dollar $VALUE backtick `nothing` ; && |\nEOF\n" +
		"printf '%s\\n' done >> " + shellQuote(output) + " # trailing comment\nsleep 60"
	cfg := sessionlayout.Config{Windows: []sessionlayout.Window{
		{Name: "aux-first", Layout: "horizontal", Panes: []sessionlayout.Pane{{Command: original}, {Command: "printf '%s\\n' pi >> " + shellQuote(piOutput) + "; sleep 60 # " + sessionlayout.PiArgsPlaceholder}}},
	}}
	if err := createEarlyTmuxWorkspace(ctx, runner, path, path, "workspace", cfg, "--name 'test'"); err != nil {
		t.Fatal(err)
	}
	waitPaneCondition(t, func() bool { _, err := os.Stat(piOutput); return err == nil })
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("auxiliary command ran before readiness: %v", err)
	}
	auxPaneID, err := runner.Run(ctx, path, "tmux", "list-panes", "-s", "-t", "workspace", "-f", "#{!=:#{"+workspacePaneGateOption+"},}", "-F", "#{pane_id}")
	if err != nil || !strings.HasPrefix(auxPaneID, "%") {
		t.Fatalf("auxiliary gate wasn't persisted: pane=%q err=%v", auxPaneID, err)
	}
	// No creator state is passed to release: tmux owns recovery after creation.
	if err := releaseWorkspacePanes(ctx, runner, path, "workspace"); err != nil {
		t.Fatal(err)
	}
	want := path + "\nsingle ' double \" dollar $VALUE backtick `nothing` ; && |\ndone\n"
	waitPaneCondition(t, func() bool { data, _ := os.ReadFile(output); return string(data) == want })
	for range 3 {
		if err := releaseWorkspacePanes(ctx, runner, path, "workspace"); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := runner.Run(ctx, path, "tmux", "list-panes", "-s", "-t", "workspace", "-f", "#{!=:#{"+workspacePaneGateOption+"},}", "-F", "#{pane_id}")
	if err != nil || pending != "" {
		t.Fatalf("consumed gates remain pending: output=%q err=%v", pending, err)
	}
	dead, err := runner.Run(ctx, path, "tmux", "display-message", "-p", "-t", auxPaneID, "#{pane_dead}")
	if err != nil || dead != "0" {
		t.Fatalf("release replaced or terminated running pane: dead=%q err=%v", dead, err)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != want {
		t.Fatalf("release replayed running command: output=%q err=%v", data, err)
	}
}

func TestEarlyPanesRealTmuxSignalBeforeWait(t *testing.T) {
	runner := newIsolatedPaneTmux(t)
	ctx := context.Background()
	path := t.TempDir()
	output := filepath.Join(path, "runs")
	command, gate := workspacePaneCommand("printf '%s\\n' once >> "+shellQuote(output), "", true)
	// Persist the signal before any process can wait for this unique gate.
	if _, err := runner.Run(ctx, path, "tmux", "wait-for", "-S", gate); err != nil {
		t.Fatal(err)
	}
	ids, err := runner.Run(ctx, path, "tmux", "new-window", "-d", "-t", "fixture:", "-c", path, "-P", "-F", "#{window_id} #{pane_id}", command)
	if err != nil {
		t.Fatal(err)
	}
	_, paneID, err := parseTmuxIDs(ids)
	if err != nil {
		t.Fatal(err)
	}
	waitPaneCondition(t, func() bool { data, _ := os.ReadFile(output); return string(data) == "once\n" })
	if _, err := runner.Run(ctx, path, "tmux", "wait-for", "-S", gate); err != nil {
		t.Fatal(err)
	}
	waitPaneCondition(t, func() bool {
		dead, err := runner.Run(ctx, path, "tmux", "display-message", "-p", "-t", paneID, "#{pane_dead}")
		return err == nil && dead == "1"
	})
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "once\n" {
		t.Fatalf("second signal replayed command: output=%q err=%v", data, err)
	}
}

// Cancel precisely after the waiting shell exists but before its gate option is
// stored. Cleanup must not reuse that canceled context and strand the shell.
type canceledPaneConstructorRunner struct {
	paneGateRunner
	cancel  context.CancelFunc
	cleaned bool
}

func (r *canceledPaneConstructorRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if args[0] == "kill-session" {
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) > 2*time.Second {
			return "", errors.New("constructor cleanup must be bounded")
		}
		r.cleaned = true
	}
	output, err := r.paneGateRunner.Run(ctx, cwd, name, args...)
	if args[0] == "new-window" {
		r.cancel()
	}
	return output, err
}

func TestEarlyPaneConstructorCancellationCleansUnrecordedGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &canceledPaneConstructorRunner{cancel: cancel}
	err := createEarlyTmuxWorkspace(ctx, runner, "", "/workspace", "fixture", sessionlayout.Config{}, "--name fixture")
	if !errors.Is(err, context.Canceled) || !runner.cleaned {
		t.Fatalf("canceled constructor stranded an unrecorded pane: err=%v cleaned=%t", err, runner.cleaned)
	}
}
