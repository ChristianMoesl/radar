package sbx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	sbxclient "radar/internal/integration/sbx/client"
	"radar/internal/protocol"
)

func TestAuthenticationRequired(t *testing.T) {
	for _, test := range []struct {
		name string
		req  integration.AuthenticationRequest
		want bool
	}{
		{name: "normal startup", req: integration.AuthenticationRequest{Operation: "startup"}},
		{name: "create", req: integration.AuthenticationRequest{Operation: "create"}, want: true},
		{name: "fork", req: integration.AuthenticationRequest{Operation: "fork"}, want: true},
		{name: "expired session", req: integration.AuthenticationRequest{Operation: "startup", SourceStatuses: []protocol.SourceStatus{{Name: "sbx", Status: "error", Detail: "not signed in; run sbx login"}}}, want: true},
		{name: "expired Windows session", req: integration.AuthenticationRequest{Operation: "startup", SourceStatuses: []protocol.SourceStatus{{Name: "sbx", Status: "error", Detail: "run sbx.exe login"}}}, want: true},
		{name: "disabled", req: integration.AuthenticationRequest{Operation: "startup", SourceStatuses: []protocol.SourceStatus{{Name: "sbx", Status: "disabled", Detail: "not signed in"}}}},
		{name: "other provider", req: integration.AuthenticationRequest{Operation: "startup", SourceStatuses: []protocol.SourceStatus{{Name: "github", Status: "error", Detail: "not signed in"}}}},
		{name: "background refresh", req: integration.AuthenticationRequest{Operation: "refresh", SourceStatuses: []protocol.SourceStatus{{Name: "sbx", Status: "error", Detail: "not signed in"}}}},
		{name: "cleanup without sandbox", req: integration.AuthenticationRequest{Operation: "cleanup", CleanupTargets: []protocol.CleanupTarget{{Source: "git"}}}},
		{name: "empty cleanup", req: integration.AuthenticationRequest{Operation: "cleanup"}},
		{name: "unrelated failure", req: integration.AuthenticationRequest{Operation: "startup", SourceStatuses: []protocol.SourceStatus{{Name: "sbx", Status: "error", Detail: "sbx daemon is unavailable"}}}},
		{name: "cleanup", req: integration.AuthenticationRequest{Operation: "cleanup", CleanupTargets: []protocol.CleanupTarget{{Source: "sbx"}}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := authenticationRequired(test.req); got != test.want {
				t.Fatalf("authenticationRequired() = %t, want %t", got, test.want)
			}
		})
	}
}

// Simulate each platform's executable discovery, then run real subprocesses.
// This covers Windows command/cwd/stdio behavior without requiring Windows SBX
// (or using the developer's credentials) on the host running the test suite.
func TestAuthenticateSelectedExecutable(t *testing.T) {
	for _, platform := range []struct {
		name, goos string
		wsl        bool
		installed  []string
		want       string
	}{
		{"macOS", "darwin", false, []string{"sbx"}, "sbx"},
		{"WSL2 Windows", "linux", true, []string{"sbx.exe", "wslpath"}, "sbx.exe"},
		{"WSL2 native preferred", "linux", true, []string{"sbx", "sbx.exe", "wslpath"}, "sbx"},
	} {
		t.Run(platform.name, func(t *testing.T) {
			for _, scenario := range []struct {
				name, probeError string
				loginFails       bool
				wantLogin        bool
			}{
				{name: "healthy"},
				{name: "expired", probeError: "Sign-in required", wantLogin: true},
				{name: "Windows login hint", probeError: "run 'sbx.exe login' to refresh", wantLogin: true},
				{name: "login failure", probeError: "401 Unauthorized", loginFails: true, wantLogin: true},
				{name: "unrelated failure", probeError: "daemon unavailable"},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					dir := t.TempDir()
					t.Setenv("PATH", dir)
					t.Setenv("RADAR_TEST_AUTH_LOG", filepath.Join(dir, "calls"))
					t.Setenv("RADAR_TEST_PROBE_ERROR", scenario.probeError)
					t.Setenv("RADAR_TEST_LOGIN_EXIT", "0")
					if scenario.loginFails {
						t.Setenv("RADAR_TEST_LOGIN_EXIT", "1")
					}
					const script = `#!/bin/sh
printf '%s|%s|%s\n' "${0##*/}" "$*" "$PWD" >> "$RADAR_TEST_AUTH_LOG"
case "$1" in
  ls)
    if [ -n "$RADAR_TEST_PROBE_ERROR" ]; then
      printf '%s\n' "$RADAR_TEST_PROBE_ERROR" >&2
      exit 1
    fi
    printf '{"sandboxes":[]}\n'
    ;;
  login)
    IFS= read -r answer
    printf 'login stdout: %s\n' "$answer"
    printf 'login stderr\n' >&2
    exit "$RADAR_TEST_LOGIN_EXIT"
    ;;
  *) exit 99;;
esac
`
					for _, name := range platform.installed {
						if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
							t.Fatal(err)
						}
					}
					executable, err := sbxclient.SelectExecutable(platform.goos, platform.wsl, func(name string) error {
						_, err := exec.LookPath(name)
						return err
					})
					if err != nil || executable != platform.want {
						t.Fatalf("executable = %q, %v; want %q", executable, err, platform.want)
					}
					t.Chdir(dir)
					cwd, err := os.Getwd()
					if err != nil {
						t.Fatal(err)
					}
					if executable == "sbx.exe" {
						cwd = "/"
					}
					stdout, stderr := authenticationTestStdio(t, dir)
					result, err := authenticate(context.Background(), executable)
					if (err != nil) != scenario.loginFails || result.Changed != (scenario.wantLogin && !scenario.loginFails) {
						t.Fatalf("authenticate = %+v, %v", result, err)
					}
					if scenario.loginFails && !strings.Contains(err.Error(), executable+" login failed") {
						t.Fatalf("login error = %v", err)
					}
					calls, err := os.ReadFile(os.Getenv("RADAR_TEST_AUTH_LOG"))
					if err != nil {
						t.Fatal(err)
					}
					wantCalls := executable + "|ls --json|" + cwd + "\n"
					if scenario.wantLogin {
						wantCalls += executable + "|login|" + cwd + "\n"
					}
					if string(calls) != wantCalls {
						t.Fatalf("calls = %q, want %q", calls, wantCalls)
					}
					out, _ := os.ReadFile(stdout)
					errors, _ := os.ReadFile(stderr)
					if scenario.wantLogin {
						if string(out) != "login stdout: user input\n" || !strings.Contains(string(errors), "starting "+executable+" login\nlogin stderr\n") {
							t.Fatalf("stdio not forwarded: stdout=%q stderr=%q", out, errors)
						}
					} else if len(out) != 0 || len(errors) != 0 {
						t.Fatalf("unexpected interactive output: stdout=%q stderr=%q", out, errors)
					}
				})
			}
		})
	}
}

func authenticationTestStdio(t *testing.T, dir string) (string, string) {
	t.Helper()
	previous := []*os.File{os.Stdin, os.Stdout, os.Stderr}
	var files []*os.File
	for _, name := range []string{"stdin", "stdout", "stderr"} {
		file, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		files = append(files, file)
	}
	if _, err := files[0].WriteString("user input\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := files[0].Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	os.Stdin, os.Stdout, os.Stderr = files[0], files[1], files[2]
	t.Cleanup(func() { os.Stdin, os.Stdout, os.Stderr = previous[0], previous[1], previous[2] })
	return files[1].Name(), files[2].Name()
}

func TestEnsureAuthenticationWithoutExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	result, err := (Source{}).EnsureAuthentication(context.Background(), integration.AuthenticationRequest{Operation: "create"})
	if err != nil || result.Changed {
		t.Fatalf("authentication without SBX = %+v, %v", result, err)
	}
}
