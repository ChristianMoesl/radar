package onboarding

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/datadog"
	"radar/internal/integration/github"
	"radar/internal/integration/jira"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/workspace"
)

// The subprocess uses the real terminal prompts, PATH lookup, child processes,
// JSON persistence and HTTPS request construction. Only installers/services are
// fixtures: no test can install software or send credentials to a real service.
func TestWizardTerminalHelper(t *testing.T) {
	endpoint := os.Getenv("RADAR_ONBOARDING_TEST_HTTP")
	if endpoint == "" {
		return
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		panic(err)
	}
	w := newWizard(terminalUI{in: os.Stdin, out: os.Stdout}, realSystem{})
	transport := http.DefaultTransport.(*http.Transport).Clone()
	w.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme, req.URL.Host = base.Scheme, base.Host
		return transport.RoundTrip(req)
	})
	if err := w.run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type terminalSession struct {
	file   *os.File
	done   chan error
	mu     sync.Mutex
	output strings.Builder
	offset int
}

func (s *terminalSession) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ansi.Strip(s.output.String())
}
func (s *terminalSession) answer(t *testing.T, prompt, answer string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		text := s.text()
		if strings.Contains(text[s.offset:], prompt) {
			s.offset = len(text)
			// Replace a prefilled text field rather than accepting its default.
			if _, err := s.file.WriteString(answer); err != nil {
				t.Fatal(err)
			}
			return
		}
		select {
		case err := <-s.done:
			t.Fatalf("process exited before %q: %v\n%s", prompt, err, text)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waiting for %q\n%s", prompt, s.text())
}
func startTerminal(t *testing.T, env []string) *terminalSession {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestWizardTerminalHelper$")
	cmd.Env = env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 160})
	if err != nil {
		t.Fatal(err)
	}
	session := &terminalSession{file: f, done: make(chan error, 1)}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 4096)
		for {
			n, err := f.Read(buffer)
			if n > 0 {
				session.mu.Lock()
				session.output.Write(buffer[:n])
				session.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { err := cmd.Wait(); <-readDone; session.done <- err }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = f.Close() })
	return session
}

func writeTool(t *testing.T, bin, name, body string) {
	t.Helper()
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalOnboardingMatrixProducesUsableState(t *testing.T) {
	// All eight opt-in combinations, each on a fresh machine and an existing
	// tmux setup (including an existing conflicting r binding and custom prefix).
	for _, initial := range []string{"existing tools", "install tmux and node"} {
		for choices := 0; choices < 8; choices++ {
			t.Run(fmt.Sprintf("%s/github=%t/jira=%t/datadog=%t", initial, choices&1 != 0, choices&2 != 0, choices&4 != 0), func(t *testing.T) {
				_, _, _, home := fixture(t)
				for _, key := range []string{"RADAR_JIRA_BASE_URL", "RADAR_JIRA_API_BASE_URL", "RADAR_JIRA_EMAIL", "RADAR_JIRA_CLOUD_ID", "RADAR_JIRA_API_TOKEN", "RADAR_DATADOG_API_KEY", "RADAR_DATADOG_APP_KEY", "RADAR_DATADOG_SITE", "TMUX", "GH_TOKEN", "GITHUB_TOKEN"} {
					t.Setenv(key, "")
				}
				bin := filepath.Join(home, "bin")
				t.Setenv("PATH", bin)
				t.Setenv("TERM", "xterm-256color")
				t.Setenv("NO_COLOR", "1")
				tools := map[string]string{
					"node": "echo v24.8.0",
					"pi":   "echo 1.0.0",
					"git":  "exit 0", "fd": "exit 0", "npm": "exit 0",
					"tmux": `case "$1" in -V) echo 'tmux 3.6';; has-session) exit 1;; display-message) printf '%%0\n';; new-session|new-window|split-window) printf '@1 %%1\n';; *) exit 0;; esac`,
					"gh": `case "$1 $2" in
       'auth status') test -f "$HOME/github-authenticated";;
       'auth login') printf ready > "$HOME/github-authenticated";;
       'auth token') printf fixture-token;;
       'api rate_limit') printf '%s' '{"resources":{"core":{"limit":5000,"remaining":4999},"search":{"limit":30,"remaining":30},"graphql":{"limit":5000,"remaining":4999}}}';;
       *) exit 0;; esac`,
				}
				for name, body := range tools {
					writeTool(t, bin, name, body)
				}
				existing := initial == "existing tools"
				custom := "set -g prefix C-a\nbind-key r display-message 'my reload'\n"
				if existing {
					if err := os.WriteFile(filepath.Join(home, ".tmux.conf"), []byte(custom), 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(home, "github-authenticated"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					for _, name := range []string{"tmux", "node"} {
						if err := os.Rename(filepath.Join(bin, name), filepath.Join(home, name+"-fixture")); err != nil {
							t.Fatal(err)
						}
					}
					writeTool(t, bin, "brew", `case "$2" in tmux|node) /bin/cp "$HOME/$2-fixture" "$HOME/bin/$2";; *) exit 99;; esac`)
				}
				// Pre-existing unrelated secrets must survive every option combination.
				if err := config.SaveSecrets(config.Secrets{"other": {"token": "keep-other-secret"}}); err != nil {
					t.Fatal(err)
				}
				var requestsMu sync.Mutex
				requests := []string{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestsMu.Lock()
					requests = append(requests, r.URL.Path)
					requestsMu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/_edge/tenant_info":
						fmt.Fprint(w, `{"cloudId":"cloud-fixture"}`)
					case "/ex/jira/cloud-fixture/rest/api/3/myself":
						email, token, _ := r.BasicAuth()
						if email != "you@example.com" || token != "jira-terminal-secret" {
							w.WriteHeader(401)
							return
						}
						fmt.Fprint(w, `{"accountId":"fixture"}`)
					case "/ex/jira/cloud-fixture/rest/api/3/search/jql":
						_, token, _ := r.BasicAuth()
						if token != "jira-terminal-secret" {
							w.WriteHeader(401)
							return
						}
						fmt.Fprint(w, `{"issues":[]}`)
					case "/api/v1/monitor/search":
						if r.Header.Get("DD-API-KEY") != "dd-api-terminal-secret" || r.Header.Get("DD-APPLICATION-KEY") != "dd-app-terminal-secret" {
							w.WriteHeader(403)
							return
						}
						fmt.Fprint(w, `{"monitors":[],"metadata":{"total_count":0,"page_count":1}}`)
					default:
						w.WriteHeader(404)
					}
				}))
				defer server.Close()
				terminal := startTerminal(t, append(os.Environ(), "RADAR_ONBOARDING_TEST_HTTP="+server.URL))
				if !existing {
					terminal.answer(t, "Install or update tmux now?", "y")
					terminal.answer(t, "Install or update node now?", "y")
				} else {
					// Exercise both accepting and declining the proposal for existing tmux.
					answer := "n"
					if choices%2 == 0 {
						answer = "y"
					}
					terminal.answer(t, "Add Radar's prefix + r popup binding?", answer)
				}
				terminal.answer(t, "Where do you check out your repositories?", "\x15"+filepath.Join(home, "repos")+"\r")
				terminal.answer(t, "Where should Radar put its workspaces and worktrees?", "\x15"+filepath.Join(home, "workspaces")+"\r")
				terminal.answer(t, "Where should Radar store your task notes?", "\x15"+filepath.Join(home, "notes")+"\r")
				answer := func(enabled bool) string {
					if enabled {
						return "y"
					}
					return "n"
				}
				terminal.answer(t, "Connect Radar to GitHub?", answer(choices&1 != 0))
				if choices&1 != 0 && !existing {
					terminal.answer(t, "Run `gh auth login`?", "y")
				}
				terminal.answer(t, "Connect Radar to Jira?", answer(choices&2 != 0))
				if choices&2 != 0 {
					terminal.answer(t, "Jira site URL", "https://example.atlassian.net\r")
					terminal.answer(t, "Jira email", "you@example.com\r")
					terminal.answer(t, "Jira API token", "jira-terminal-secret\r")
					terminal.answer(t, "Jira ticket prefixes", "abc, XYZ\r")
				}
				terminal.answer(t, "Connect Radar to Datadog?", answer(choices&4 != 0))
				if choices&4 != 0 {
					terminal.answer(t, "Datadog site or API endpoint", "\x15https://api.datadoghq.eu/\r")
					terminal.answer(t, "Datadog API key", "dd-api-terminal-secret\r")
					terminal.answer(t, "Datadog application key", "dd-app-terminal-secret\r")
					terminal.answer(t, "Datadog monitor query", "tag:team:platform\r")
				}
				terminal.answer(t, "Generate this configuration?", "y")
				select {
				case err := <-terminal.done:
					if err != nil {
						t.Fatalf("wizard failed: %v\n%s", err, terminal.text())
					}
				case <-time.After(10 * time.Second):
					t.Fatal("wizard did not exit")
				}
				for _, secret := range []string{"jira-terminal-secret", "dd-api-terminal-secret", "dd-app-terminal-secret", "keep-other-secret"} {
					if strings.Contains(terminal.text(), secret) {
						t.Fatal("terminal echoed a secret")
					}
				}
				cfg, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				secrets, err := config.LoadSecrets()
				if err != nil {
					t.Fatal(err)
				}
				if secrets["other"]["token"] != "keep-other-secret" {
					t.Fatal("unrelated secret lost")
				}
				// Verify real collectors resolve the persisted connection and secrets,
				// not just that the wizard happened to write parseable JSON.
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				for i, source := range []integration.Source{github.NewSource(), jira.NewSource(), datadog.NewSource()} {
					status := source.(integration.StatusReporter).Status(context.Background(), logger)
					if status.CanRun != (choices&(1<<i) != 0) || status.Status.Status == "error" {
						t.Fatalf("source %s unusable: %+v", source.Descriptor().Name, status)
					}
				}
				original := http.DefaultClient.Transport
				target, _ := url.Parse(server.URL)
				transport := http.DefaultTransport.(*http.Transport).Clone()
				http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					req.URL.Scheme, req.URL.Host = target.Scheme, target.Host
					return transport.RoundTrip(req)
				})
				for i, source := range []integration.Source{jira.NewSource(), datadog.NewSource()} {
					if choices&(1<<(i+1)) == 0 {
						continue
					}
					result := source.Collect(context.Background(), integration.CollectRequest{Logger: logger})
					if !result.Complete || result.SourceStatus == nil || result.SourceStatus.Status != "ok" {
						t.Errorf("persisted %s collection failed: %+v", source.Descriptor().Name, result)
					}
				}
				http.DefaultClient.Transport = original
				// Prove the saved layout and plain notes directory can provision a real
				// note-only workspace using child-process stubs, without Neovim or Obsidian.
				created, err := workspace.Create(context.Background(), workspace.ExecRunner{}, workspace.CreateOptions{Name: "First task", WorkspaceRoot: cfg.Workspace.RootDir, Tmux: cfg.Tmux, NoteAuthor: obsidian.NewSourceAt(cfg.Obsidian.VaultPath)})
				if err != nil {
					t.Fatalf("generated config cannot create workspace: %v", err)
				}
				if _, err := os.ReadFile(filepath.Join(created.Path, "notes.md")); err != nil {
					t.Fatal(err)
				}
				tmuxData, _ := os.ReadFile(filepath.Join(home, ".tmux.conf"))
				if existing && !strings.HasPrefix(string(tmuxData), custom) {
					t.Fatal("existing tmux config was changed")
				}
				if existing && choices%2 != 0 && string(tmuxData) != custom {
					t.Fatal("declining tmux proposal changed config")
				}
			})
		}
	}
}
