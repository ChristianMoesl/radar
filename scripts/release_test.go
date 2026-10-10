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
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for notifier release preflight")
	}
	if err := os.Symlink(node, filepath.Join(bin, "node")); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"macos/RadarNotifier/VERSION":        []byte("1.2.3\n"),
		"package.json":                       []byte(`{"version":"0.1.0"}`),
		"extensions/pi-radar/version.ts":     []byte("export const radarPackageVersion = \"0.1.0\";\n"),
		"scripts/release-version.mjs":        mustReadScript(t, "release-version.mjs"),
		"scripts/notifier-release-state.mjs": mustReadScript(t, "notifier-release-state.mjs"),
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	const git = `#!/bin/sh
printf 'git %s\n' "$*" >> "$RELEASE_CALLS"
case "$*" in
  'rev-parse --show-toplevel') printf '%s\n' "$RELEASE_ROOT";;
  'branch --show-current') printf 'main\n';;
  'status --porcelain'|'status --porcelain -- . :!package.json :!extensions/pi-radar/version.ts'|'merge-base --is-ancestor origin/main HEAD') ;;
  'rev-parse HEAD'|'rev-parse origin/main'|'rev-parse --short=12 HEAD') printf 'fixture-commit\n';;
  'rev-parse -q --verify refs/tags/v0.1.1'|'diff --quiet HEAD -- package.json extensions/pi-radar/version.ts') exit 1;;
  'ls-remote --exit-code --tags origin refs/tags/v0.1.1') exit 2;;
  'commit --only -m chore: release v0.1.1 -- package.json extensions/pi-radar/version.ts') ;;
  'rev-parse -q --verify refs/tags/notifier-v1.2.3')
    case "$RELEASE_MODE" in
      existing) printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n';;
      mismatch) printf 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n';;
      *) exit 1;;
    esac;;
  'push --atomic origin fixture-commit:refs/heads/main refs/tags/notifier-v1.2.3 refs/tags/v0.1.1')
    if [ "$RELEASE_FAILURE" = push ]; then exit 11; fi;;
  'fetch origin main --tags'|'tag -s v0.1.1 fixture-commit -m v0.1.1'|'tag -s notifier-v1.2.3 fixture-commit -m Radar notifier 1.2.3') ;;
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
	const gh = `#!/bin/sh
printf 'gh %s\n' "$*" >> "$RELEASE_CALLS"
case "$*" in
  'api repos/ChristianMoesl/radar/releases --paginate --slurp')
    case "$RELEASE_MODE" in
      'inventory failure') echo 'HTTP 503' >&2; exit 7;;
      retired) printf '[[{"id":42,"tag_name":"notifier-v1.2.3","draft":true,"published_at":"2026-01-01T00:00:00Z"}]]\n';;
      existing|missing|mismatch) printf '[[{"id":42,"tag_name":"notifier-v1.2.3","draft":false,"published_at":"2026-01-01T00:00:00Z"}]]\n';;
      *) printf '[[]]\n';;
    esac;;
  'api repos/ChristianMoesl/radar/git/ref/tags/notifier-v1.2.3')
    case "$RELEASE_MODE" in
      existing|missing|mismatch) printf '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}\n';;
      'tag auth failure') echo 'HTTP 401' >&2; exit 7;;
      *) echo 'HTTP 404' >&2; exit 1;;
    esac;;
  *) echo 'unexpected gh command' >&2; exit 99;;
esac
`
	for name, script := range map[string]string{"git": git, "gh": gh, "pnpm": pnpm, "make": make} {
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
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + root, "RELEASE_ROOT=" + root, "RELEASE_CALLS=" + filepath.Join(root, "calls"), "RELEASE_FAILURE=" + failure, "RELEASE_MODE=fresh"}
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

func TestReleaseOwnsNotifierRefsBeforeMirroring(t *testing.T) {
	for _, mode := range []string{"fresh", "existing", "missing", "mismatch", "retired", "inventory failure", "tag auth failure"} {
		t.Run(mode, func(t *testing.T) {
			cmd := releaseFixture(t, "")
			cmd.Env = append(cmd.Env, "RELEASE_MODE="+mode)
			output, err := cmd.CombinedOutput()
			want := 1
			if mode == "fresh" || mode == "existing" {
				want = 0
			} else if mode == "inventory failure" {
				want = 7
			}
			assertReleaseExit(t, err, want, output)
			data, err := os.ReadFile(filepath.Join(cmd.Dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			calls := string(data)
			if want != 0 {
				if strings.Contains(calls, "git tag ") || strings.Contains(calls, "git push ") || strings.Contains(calls, "make dist ") {
					t.Fatalf("unsafe notifier preflight reached build/publication: %s", calls)
				}
				return
			}
			componentTag := strings.Index(calls, "git tag -s notifier-v1.2.3")
			if (componentTag >= 0) != (mode == "fresh") {
				t.Fatalf("existing notifier ref must not be recreated: %s", calls)
			}
			cliTag := strings.Index(calls, "git tag -s v0.1.1")
			dist := strings.Index(calls, "make dist ")
			push := strings.Index(calls, "git push --atomic origin fixture-commit:refs/heads/main refs/tags/notifier-v1.2.3 refs/tags/v0.1.1")
			if cliTag <= dist || push <= cliTag || mode == "fresh" && (componentTag <= dist || componentTag >= cliTag) || strings.Count(calls, "git push ") != 1 {
				t.Fatalf("refs must be published atomically only after validation: %s", calls)
			}
		})
	}
}

func TestReleaseAtomicPushFailureHasNoSeparatePushFallback(t *testing.T) {
	cmd := releaseFixture(t, "push")
	output, err := cmd.CombinedOutput()
	assertReleaseExit(t, err, 11, output)
	calls, err := os.ReadFile(filepath.Join(cmd.Dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "git push ") != 1 || !strings.Contains(string(calls), "git push --atomic origin fixture-commit:refs/heads/main refs/tags/notifier-v1.2.3 refs/tags/v0.1.1") {
		t.Fatalf("failed atomic publication must not fall back to separate pushes: %s", calls)
	}
}

func TestBuildVersionIgnoresIndependentNotifierTags(t *testing.T) {
	root := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + root, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_COMMITTER_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE=2020-01-01T12:01:23Z", "GIT_COMMITTER_DATE=2020-01-01T12:01:23Z"}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture"}, {"tag", "v1.2.3"}, {"-c", "tag.gpgsign=false", "tag", "-a", "notifier-v9.9.9", "-m", "independent component"}} {
		cmd := exec.Command(git, args...)
		cmd.Dir, cmd.Env = root, env
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git command failed: %v; %s", err, output)
		}
	}
	makefile, err := filepath.Abs("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []string{"", "VERSION=v3.4.5"} {
		args := []string{"--no-print-directory", "-sf", makefile, "-n", "build-radar"}
		want := "Number=v1.2.3"
		if explicit != "" {
			args, want = append(args, explicit), "Number=v3.4.5"
		}
		cmd := exec.Command("make", args...)
		cmd.Dir, cmd.Env = root, env
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), want) || strings.Contains(string(output), "Number=notifier-") {
			t.Fatalf("independent component tag contaminated CLI version: %v; %s", err, output)
		}
	}
}

func mustReadScript(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
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
