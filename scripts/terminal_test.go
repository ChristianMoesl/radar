package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Initialize both speeds before taking the baseline. A fresh Linux PTY can
// encode the input speed as "same as output"; stty makes it explicit on its first
// write. Start from that normal form so the assertion still compares every bit.
func initializeTerminal(t *testing.T, terminal *os.File) {
	t.Helper()
	cmd := exec.Command(terminalTool(t), "38400")
	cmd.Stdin = terminal
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initialize PTY: %v: %s", err, output)
	}
}

func terminalTool(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"gnustty", "stty"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Fatal("terminal tests require GNU/BSD stty")
	return ""
}

// Preserve both names, including a potentially broken default stty. The scripts
// must select the working tool themselves; the fixture must not fix their PATH.
func linkTerminalTool(t *testing.T, bin string) {
	t.Helper()
	for _, name := range []string{"stty", "gnustty"} {
		if path, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Restrict PATH to fixture tools so the no-gnustty case is deterministic even
// on hosts that provide it. Real termios reads/writes still use the host tool.
func isolateTerminalTools(t *testing.T, cmd *exec.Cmd, mode string) {
	t.Helper()
	var bin string
	for i, entry := range cmd.Env {
		if strings.HasPrefix(entry, "PATH=") {
			bin = strings.Split(strings.TrimPrefix(entry, "PATH="), ":")[0]
			cmd.Env[i] = "PATH=" + bin
		}
	}
	if bin == "" {
		t.Fatal("fixture needs a tool directory")
	}
	for _, name := range []string{"bash", "env", "cat", "cp", "mv", "mkdir", "rm", "chmod", "dirname", "mktemp", "grep", "ln", "install"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"stty", "gnustty"} {
		if err := os.Remove(filepath.Join(bin, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	cmd.Env = append(cmd.Env, "FIXTURE_STTY="+terminalTool(t), "FIXTURE_TERMINAL_MODE="+mode)
	const stty = `#!/bin/sh
case "$1" in
  -g)
    if [ "$FIXTURE_TERMINAL_MODE" = capture ]; then exit 27; fi;;
  *:*)
    case "$FIXTURE_TERMINAL_MODE" in
      named-GNU|restore) echo 'fixture stty rejects saved state' >&2; exit 27;;
    esac;;
esac
"$FIXTURE_STTY" "$@" || exit $?
if [ "$FIXTURE_TERMINAL_MODE" = late ]; then
  if [ "$1" = raw ]; then : > "$HOME/terminal-was-raw"; fi
  case "$1" in
    *:*) if [ -f "$HOME/terminal-was-raw" ]; then exit 27; fi;;
  esac
fi
`
	if err := os.WriteFile(filepath.Join(bin, "stty"), []byte(stty), 0700); err != nil {
		t.Fatal(err)
	}
	if mode == "named-GNU" {
		if err := os.Symlink(terminalTool(t), filepath.Join(bin, "gnustty")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTerminalToolSelectionAndPreflight(t *testing.T) {
	for _, script := range []string{"release", "bootstrap"} {
		for _, mode := range []string{"named-GNU", "standard", "capture", "restore"} {
			for _, headless := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/headless=%t", script, mode, headless), func(t *testing.T) {
					var cmd *exec.Cmd
					var answers string
					if script == "release" {
						cmd = releaseFixture(t, "")
					} else {
						cmd, answers, _ = bootstrapFixture(t, "Linux ready")
						// Bootstrap prepends this directory during dependency setup.
						// Neither capture nor restoration may switch to its tools.
						for _, name := range []string{"stty", "gnustty"} {
							if err := os.WriteFile(filepath.Join(cmd.Dir, "brew", "bin", name), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
								t.Fatal(err)
							}
						}
					}
					isolateTerminalTools(t, cmd, mode)
					var output []byte
					var err error
					if headless {
						cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
						output, err = cmd.CombinedOutput()
					} else {
						output, err = runBootstrapPTY(t, cmd, answers)
					}
					if !headless && (mode == "capture" || mode == "restore") {
						assertReleaseExit(t, err, 1, output)
						if !strings.Contains(string(output), "Cannot save and restore terminal modes") {
							t.Fatalf("missing prerequisite diagnostic: %s", output)
						}
						calls, _ := os.ReadFile(filepath.Join(cmd.Dir, "calls"))
						if len(calls) != 0 {
							t.Fatalf("invalid terminal tool reached install/release work: %s", calls)
						}
					} else {
						assertReleaseExit(t, err, 0, output)
					}
				})
			}
		}
	}
}

func TestReleaseRestorationFailureStopsPublicationAndPreservesFailure(t *testing.T) {
	for _, failure := range []string{"", "pnpm"} {
		t.Run("failure="+failure, func(t *testing.T) {
			cmd := releaseFixture(t, failure)
			work := t.TempDir()
			cmd.Env = append(cmd.Env, "TMPDIR="+work)
			isolateTerminalTools(t, cmd, "late")
			output, err := runBootstrapPTY(t, cmd, "")
			want := 1
			if failure == "pnpm" {
				want = 7
			}
			assertReleaseExit(t, err, want, output)
			if !strings.Contains(string(output), "Failed to restore terminal modes") {
				t.Fatalf("missing restoration diagnostic: %s", output)
			}
			calls, _ := os.ReadFile(filepath.Join(cmd.Dir, "calls"))
			if strings.Contains(string(calls), "git tag ") || strings.Contains(string(calls), "git push ") || strings.Contains(string(calls), "make test") {
				t.Fatalf("restoration failure did not stop release: %s", calls)
			}
			if entries, err := os.ReadDir(work); err != nil || len(entries) != 0 {
				t.Fatalf("restoration failure skipped cleanup: %v, %v", entries, err)
			}
		})
	}
}

func TestBootstrapRestorationFailurePreservesChildFailure(t *testing.T) {
	for _, childExit := range []int{0, 7} {
		t.Run(fmt.Sprint(childExit), func(t *testing.T) {
			cmd, answers, _ := bootstrapFixture(t, "Linux ready")
			isolateTerminalTools(t, cmd, "late")
			launch := filepath.Join(cmd.Dir, "tools", "launch")
			if err := os.WriteFile(launch, []byte(fmt.Sprintf("#!/bin/sh\nstty raw\nexit %d\n", childExit)), 0700); err != nil {
				t.Fatal(err)
			}
			output, err := runBootstrapPTY(t, cmd, answers)
			want := childExit
			if want == 0 {
				want = 1
			}
			assertReleaseExit(t, err, want, output)
			if !strings.Contains(string(output), "Failed to restore terminal modes") {
				t.Fatalf("missing restoration diagnostic: %s", output)
			}
		})
	}
}
