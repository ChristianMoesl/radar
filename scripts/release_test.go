package scripts_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

// Run the real release script, but replace all repository, package-manager and
// build boundaries. No test can create a real tag, push, or publish anything.
func releaseFixture(t *testing.T, failure string) *exec.Cmd {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	const git = `#!/bin/sh
printf 'git %s\n' "$*" >> "$RELEASE_CALLS"
case "$*" in
  'rev-parse --show-toplevel') printf '%s\n' "$RELEASE_ROOT";;
  'branch --show-current') printf 'main\n';;
  'status --porcelain') ;;
  'rev-parse HEAD'|'rev-parse origin/main'|'rev-parse --short=12 HEAD') printf 'fixture-commit\n';;
  'rev-parse -q --verify refs/tags/v0.1.1'|'ls-remote --exit-code --tags origin refs/tags/v0.1.1') exit 1;;
  'fetch origin main --tags'|'tag -s v0.1.1 -m v0.1.1'|'push origin main'|'push origin v0.1.1') ;;
  *) echo 'unexpected git command' >&2; exit 99;;
esac
`
	const pnpm = `#!/bin/sh
printf 'pnpm %s\n' "$*" >> "$RELEASE_CALLS"
if [ "$1" = check ]; then
  if [ -t 0 ]; then stty raw; fi
  case "$RELEASE_FAILURE" in
    pnpm) exit 7;;
    signal) kill -TERM "$PPID"; exit 0;;
  esac
fi
`
	const make = `#!/bin/sh
printf 'make %s\n' "$*" >> "$RELEASE_CALLS"
# A successful earlier validation must restore modes BEFORE this output.
printf 'line one\nline two\n'
if [ "$RELEASE_FAILURE" = "$1" ]; then
  if [ -t 0 ]; then stty raw; fi
  exit 9
fi
`
	for name, script := range map[string]string{"git": git, "pnpm": pnpm, "make": make} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script, err := filepath.Abs("release.sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "/bin/bash", script, "v0.1.1")
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + root, "RELEASE_ROOT=" + root, "RELEASE_CALLS=" + filepath.Join(root, "calls"), "RELEASE_FAILURE=" + failure}
	cmd.Dir = root
	return cmd
}

func TestReleaseRestoresTerminalAndPreservesValidationResult(t *testing.T) {
	for _, failure := range []string{"", "pnpm", "test", "dist", "signal"} {
		t.Run("failure="+failure, func(t *testing.T) {
			cmd := releaseFixture(t, failure)
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			before, err := term.GetState(master.Fd())
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var output []byte
			readDone := make(chan struct{})
			go func() { output, _ = io.ReadAll(master); close(readDone) }()
			err = cmd.Wait()
			after, stateErr := term.GetState(master.Fd())
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			_ = slave.Close()
			<-readDone
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("release changed the caller's terminal modes: before=%+v after=%+v", before, after)
			}
			wantExit := 0
			switch failure {
			case "pnpm":
				wantExit = 7
			case "test", "dist":
				wantExit = 9
			case "signal":
				wantExit = 143
			}
			assertReleaseExit(t, err, wantExit, output)
			calls, err := os.ReadFile(filepath.Join(cmd.Dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			published := strings.Contains(string(calls), "git tag ") || strings.Contains(string(calls), "git push ")
			if published != (failure == "") {
				t.Fatalf("tag/push boundary violated: %s", calls)
			}
			if failure == "" || failure == "test" || failure == "dist" {
				if !strings.Contains(string(output), "line one\r\nline two\r\n") {
					t.Fatalf("validation did not restore newline processing before the next stage: %q", output)
				}
			}
		})
	}
}

func TestMakeTestBoundsPackageParallelism(t *testing.T) {
	cmd := exec.Command("make", "--no-print-directory", "-n", "test", "GO=go")
	cmd.Dir = ".."
	output, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "go test -p 2 ./..." {
		t.Fatalf("make test must bound package concurrency: %v; %s", err, output)
	}
}

func TestReleaseValidationFailureWithoutTerminal(t *testing.T) {
	cmd := releaseFixture(t, "test")
	output, err := cmd.CombinedOutput()
	assertReleaseExit(t, err, 9, output)
	if strings.Contains(string(output), "stty:") {
		t.Fatalf("nonterminal release attempted terminal control: %s", output)
	}
}

func assertReleaseExit(t *testing.T, err error, want int, output []byte) {
	t.Helper()
	got := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("release: %v; %s", err, output)
		}
		got = exit.ExitCode()
	}
	if got != want {
		t.Fatalf("release exit=%d, want %d; %s", got, want, output)
	}
}
