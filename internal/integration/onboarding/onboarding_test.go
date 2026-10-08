package onboarding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/config"
)

type fakeUI struct {
	questions     []question
	defaults      map[string]bool
	inputs        []string
	decisions     []bool
	titles        []string
	transcript    strings.Builder
	cancelAt      string
	beforeConfirm func(string)
	tmuxChoice    bool
}

func (u *fakeUI) input(q question) (string, error) {
	u.titles = append(u.titles, q.title)
	u.questions = append(u.questions, q)
	if q.title == u.cancelAt {
		return "", ErrAborted
	}
	if len(u.inputs) == 0 {
		return "", fmt.Errorf("unexpected input: %s", q.title)
	}
	value := u.inputs[0]
	if value == "<keep>" {
		value = q.initial
	}
	u.inputs = u.inputs[1:]
	if q.validate != nil {
		if err := q.validate(value); err != nil {
			return "", err
		}
	}
	if q.secret {
		u.print("%s: [hidden]\n", q.title)
	} else {
		u.print("%s: %s\n", q.title, value)
	}
	return value, nil
}
func (u *fakeUI) confirm(title string, initial bool) (bool, error) {
	u.titles = append(u.titles, title)
	if u.defaults == nil {
		u.defaults = map[string]bool{}
	}
	u.defaults[title] = initial
	if title == "Add Radar's prefix + r popup binding?" {
		return u.tmuxChoice, nil
	}
	if u.beforeConfirm != nil {
		u.beforeConfirm(title)
	}
	if title == u.cancelAt {
		return false, ErrAborted
	}
	if len(u.decisions) == 0 {
		return false, fmt.Errorf("unexpected confirmation: %s", title)
	}
	result := u.decisions[0]
	u.decisions = u.decisions[1:]
	return result, nil
}
func (u *fakeUI) print(format string, args ...any) { fmt.Fprintf(&u.transcript, format, args...) }

type fakeSystem struct {
	missing      map[string]bool
	calls        []string
	installErr   error
	loginMissing bool
	goos         string
	nodeVersion  string
}

func (s *fakeSystem) platform() string {
	if s.goos != "" {
		return s.goos
	}
	return "darwin"
}
func (s *fakeSystem) lookPath(name string) bool { return !s.missing[name] }
func (s *fakeSystem) output(_ context.Context, name string, args ...string) (string, error) {
	s.calls = append(s.calls, name+" "+strings.Join(args, " "))
	if name == "tmux" {
		return "tmux 3.6", nil
	}
	if name == "node" {
		if s.nodeVersion != "" {
			return s.nodeVersion, nil
		}
		return "v24.8.0", nil
	}
	if name == "pi" {
		return "1.0.0", nil
	}
	if name == "gh" && len(args) > 0 && args[0] == "auth" && s.loginMissing {
		return "", errors.New("not authenticated")
	}
	return "", nil
}
func (s *fakeSystem) run(_ context.Context, name string, args ...string) error {
	s.calls = append(s.calls, name+" "+strings.Join(args, " "))
	if s.installErr != nil {
		return s.installErr
	}
	if name == "brew" {
		delete(s.missing, args[1])
		if args[1] == "node" {
			s.nodeVersion = "v24.8.0"
		}
	}
	if name == "gh" {
		s.loginMissing = false
	}
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T) (wizard, *fakeUI, *fakeSystem, string) {
	t.Helper()
	for _, key := range []string{"RADAR_JIRA_BASE_URL", "RADAR_JIRA_EMAIL", "RADAR_JIRA_CLOUD_ID", "RADAR_JIRA_API_BASE_URL", "RADAR_JIRA_API_TOKEN", "RADAR_DATADOG_SITE", "RADAR_DATADOG_API_KEY", "RADAR_DATADOG_APP_KEY"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("PI_CODING_AGENT_DIR", "")
	piDir := filepath.Join(home, ".pi", "agent")
	installRadarFixture(t, piDir)
	repos := filepath.Join(home, "repos")
	if err := os.Mkdir(repos, 0755); err != nil {
		t.Fatal(err)
	}
	ui := &fakeUI{inputs: []string{repos, filepath.Join(home, "workspaces"), filepath.Join(home, "notes")}}
	sys := &fakeSystem{missing: map[string]bool{"sbx": true}}
	w := newWizard(ui, sys)
	w.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected HTTP request"); return nil, nil })
	return w, ui, sys, home
}
func installRadarFixture(t *testing.T, dir string) {
	t.Helper()
	pkg := filepath.Join(dir, "npm", "node_modules", "@christianmoesl", "pi-radar")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@christianmoesl/pi-radar"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"packages":["npm:@christianmoesl/pi-radar"]}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWizardSavesSettingsAndSeparateSecretsAfterReview(t *testing.T) {
	w, ui, sys, home := fixture(t)
	ui.inputs = append(ui.inputs, "https://example.atlassian.net/", "you@example.com", "jira-fixture-secret", "abc, XYZ abc", "https://api.datadoghq.eu", "dd-api-fixture-secret", "dd-app-fixture-secret", "tag:team:platform")
	ui.decisions = []bool{true, true, true, true}
	requests := []string{}
	w.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.Host+req.URL.Path)
		body := `{}`
		switch req.URL.Path {
		case "/_edge/tenant_info":
			if req.Header.Get("Authorization") != "" {
				t.Fatal("discovery must not receive the API token")
			}
			body = `{"cloudId":"cloud-fixture"}`
		case "/ex/jira/cloud-fixture/rest/api/3/myself":
			email, token, ok := req.BasicAuth()
			if !ok || email != "you@example.com" || token != "jira-fixture-secret" {
				t.Fatal("missing Jira authentication")
			}
		case "/api/v1/monitor/search":
			if req.Header.Get("DD-API-KEY") != "dd-api-fixture-secret" || req.Header.Get("DD-APPLICATION-KEY") != "dd-app-fixture-secret" {
				t.Fatal("missing Datadog authentication")
			}
			if req.URL.Query().Get("query") != "tag:team:platform" {
				t.Fatal("query scope was not sent")
			}
		default:
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	ui.beforeConfirm = func(title string) {
		if title != "Save this configuration?" {
			return
		}
		needed, err := Needed()
		if err != nil || !needed {
			t.Fatalf("config written before confirmation: %v", err)
		}
		path, _ := config.SecretsPath()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("secrets written before confirmation")
		}
		if !strings.Contains(ui.transcript.String(), `cloud_id: cloud-fixture`) {
			t.Fatal("config preview not shown before confirmation")
		}
	}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Jira.CloudID != "cloud-fixture" || cfg.Datadog.Site != "datadoghq.eu" || !reflect.DeepEqual(cfg.LinkingMarkPrefixes, []string{"ABC", "XYZ"}) {
		t.Fatalf("settings = %+v", cfg)
	}
	if cfg.GitHub.Enabled == nil || !*cfg.GitHub.Enabled || cfg.Jira.Enabled == nil || !*cfg.Jira.Enabled || cfg.Datadog.Enabled == nil || !*cfg.Datadog.Enabled {
		t.Fatal("integrations were not enabled")
	}
	if len(cfg.Tmux.Windows) != 1 || cfg.Tmux.Windows[0].Name != "pi" {
		t.Fatal("setup must not require Neovim")
	}
	secrets, err := config.LoadSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if secrets["jira"]["api_token"] != "jira-fixture-secret" || secrets["datadog"]["app_key"] != "dd-app-fixture-secret" {
		t.Fatal("secrets were not saved")
	}
	configPath, _ := config.Path()
	data, _ := os.ReadFile(configPath)
	for _, secret := range []string{"jira-fixture-secret", "dd-api-fixture-secret", "dd-app-fixture-secret"} {
		if strings.Contains(ui.transcript.String(), secret) || strings.Contains(string(data), secret) || strings.Contains(strings.Join(sys.calls, "\n"), secret) {
			t.Fatal("secret leaked outside secrets.yaml")
		}
	}
	if len(requests) != 3 {
		t.Fatalf("requests: %v", requests)
	}
	if _, err := os.Stat(filepath.Join(home, "notes", "Tasks")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "notes", ".obsidian")); !os.IsNotExist(err) {
		t.Fatal("must not require/create an Obsidian vault")
	}
}

func TestWizardSkipsDeclinedIntegrations(t *testing.T) {
	w, ui, sys, _ := fixture(t)
	ui.decisions = []bool{false, false, false, true}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if *cfg.GitHub.Enabled || *cfg.Jira.Enabled || *cfg.Datadog.Enabled {
		t.Fatal("declined integrations must be explicitly disabled")
	}
	for _, call := range sys.calls {
		if strings.HasPrefix(call, "gh auth ") {
			t.Fatal("declined GitHub must not authenticate")
		}
	}
	path, _ := config.SecretsPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("no secrets should have been written")
	}
}

func TestWizardCancellationLeavesNoConfigurationOrDirectories(t *testing.T) {
	for _, cancel := range []string{"Where do you check out your repositories?", "Jira API token", "Save this configuration?", "decline save", "decline extension install"} {
		t.Run(cancel, func(t *testing.T) {
			w, ui, _, home := fixture(t)
			ui.cancelAt = cancel
			ui.decisions = []bool{false, false, false, false}
			if cancel == "Jira API token" {
				ui.decisions = []bool{false, true}
				ui.inputs = append(ui.inputs, "https://example.atlassian.net", "you@example.com")
			}
			if cancel == "decline extension install" {
				_ = os.Remove(filepath.Join(home, ".pi", "agent", "settings.json"))
				ui.decisions = []bool{false}
			}
			if err := w.run(); !errors.Is(err, ErrAborted) {
				t.Fatalf("error = %v", err)
			}
			for _, path := range []string{filepath.Join(home, "config", "radar", "config.yaml"), filepath.Join(home, "config", "radar", "secrets.yaml"), filepath.Join(home, "workspaces"), filepath.Join(home, "notes")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("cancellation left %s", path)
				}
			}
		})
	}
}

func TestWizardReportsAllMissingToolsWithoutInstalling(t *testing.T) {
	w, ui, sys, home := fixture(t)
	sys.missing["tmux"] = true
	sys.missing["gh"] = true
	sys.nodeVersion = "v22.0.0"
	err := w.run()
	if err == nil {
		t.Fatal("incomplete installation accepted")
	}
	for _, want := range []string{"tmux", "gh", "node", "make install", "radar setup"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %s: %v", want, err)
		}
	}
	if len(ui.titles) != 0 {
		t.Fatalf("incomplete setup prompted: %v", ui.titles)
	}
	for _, call := range sys.calls {
		if strings.Contains(call, "install") || strings.HasPrefix(call, "brew ") {
			t.Fatal(call)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "config", "radar", "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("setup saved")
	}
}

func TestWizardAbortsFailedInstallationAndAuthentication(t *testing.T) {
	for _, mode := range []string{"install", "login", "jira"} {
		t.Run(mode, func(t *testing.T) {
			w, ui, sys, home := fixture(t)
			switch mode {
			case "install":
				_ = os.Remove(filepath.Join(home, ".pi", "agent", "settings.json"))
				sys.installErr = errors.New("fixture failure")
				ui.decisions = []bool{true}
			case "login":
				sys.loginMissing = true
				sys.installErr = errors.New("fixture failure")
				ui.decisions = []bool{true, true}
			case "jira":
				ui.decisions = []bool{false, true}
				ui.inputs = append(ui.inputs, "https://example.atlassian.net", "you@example.com", "secret-do-not-echo")
				w.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("secret-do-not-echo"))}, nil
				})
			}
			err := w.run()
			if err == nil || strings.Contains(err.Error(), "secret-do-not-echo") {
				t.Fatalf("error: %v", err)
			}
			if needed, _ := Needed(); !needed {
				t.Fatal("failure wrote config")
			}
		})
	}
}

func TestWizardRecoversGitHubLogin(t *testing.T) {
	w, ui, sys, _ := fixture(t)
	sys.loginMissing = true
	ui.decisions = []bool{true, true, false, false, true}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(sys.calls, "\n")
	if strings.Count(calls, "gh auth status") != 2 || !strings.Contains(calls, "gh auth login --hostname github.com") {
		t.Fatalf("calls: %s", calls)
	}
}

func TestWizardRejectsMalformedExistingConfig(t *testing.T) {
	for _, data := range []string{`null`, `[]`, `broken`, ""} {
		t.Run(data, func(t *testing.T) {
			w, ui, _, _ := fixture(t)
			path, _ := config.EnsureFile()
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if needed, err := Needed(); needed || err != nil {
				t.Fatalf("needed: %v %v", needed, err)
			}
			if err := w.run(); err == nil {
				t.Fatal("expected existing-config error")
			}
			got, _ := os.ReadFile(path)
			if string(got) != data || len(ui.titles) > 0 {
				t.Fatal("existing configuration changed or wizard prompted")
			}
		})
	}
}

func TestWizardDetectsConcurrentConfigBeforeWriting(t *testing.T) {
	w, ui, _, home := fixture(t)
	ui.decisions = []bool{false, false, false, true}
	ui.beforeConfirm = func(title string) {
		if title == "Save this configuration?" {
			if err := config.Create(config.Default()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.run(); err == nil {
		t.Fatal("expected conflict")
	}
	if _, err := os.Stat(filepath.Join(home, "notes")); !os.IsNotExist(err) {
		t.Fatal("conflict created directories")
	}
}

func TestValidation(t *testing.T) {
	for _, value := range []string{"", "123", "ABC-123", "a/b"} {
		if _, err := parsePrefixes(value); err == nil {
			t.Errorf("accepted prefix %q", value)
		}
	}
	for _, value := range []string{"http://example.atlassian.net", "https://u:p@example.atlassian.net", "https://example.atlassian.net/path", "https://example.atlassian.net?secret=x"} {
		if validateSiteURL(value) == nil {
			t.Errorf("accepted site %q", value)
		}
	}
	for _, value := range []string{"v22.1.0", "24.0", "v24.0.0-rc.1", "nonsense"} {
		if versionAtLeast(value, [3]int{24, 0, 0}) {
			t.Errorf("accepted version %q", value)
		}
	}
	if !versionAtLeast("v24.1.0\n", [3]int{24, 0, 0}) {
		t.Fatal("rejected Node 24")
	}
	if _, err := directoryPath("relative/path"); err == nil {
		t.Fatal("accepted relative directory")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0600)
	if _, err := directoryPath(filepath.Join(file, "child")); err == nil {
		t.Fatal("accepted file parent")
	}
}

func TestWizardRejectsWorkspaceRootContainingRepositories(t *testing.T) {
	w, ui, _, home := fixture(t)
	ui.inputs[1] = home
	if err := w.run(); err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("error = %v", err)
	}
	if needed, _ := Needed(); !needed {
		t.Fatal("invalid directory setup was saved")
	}
}

func TestWizardDecliningFinalReviewPreservesExistingSecrets(t *testing.T) {
	w, ui, _, _ := fixture(t)
	if err := config.SaveSecrets(config.Secrets{"other": {"key": "keep"}}); err != nil {
		t.Fatal(err)
	}
	path, _ := config.SecretsPath()
	before, _ := os.ReadFile(path)
	ui.inputs = append(ui.inputs, "https://example.atlassian.net", "you@example.com", "new-secret", "ABC", "datadoghq.eu", "dd-api", "dd-app", "tag:team:platform")
	ui.decisions = []bool{false, true, true, false}
	w.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"cloudId":"fixture"}`))}, nil
	})
	if err := w.run(); !errors.Is(err, ErrAborted) {
		t.Fatalf("error = %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("declining review changed secrets")
	}
	if needed, _ := Needed(); !needed {
		t.Fatal("declining review wrote config")
	}
}

func TestRepeatSetupRetainsOrEditsExistingSettingsAndSecrets(t *testing.T) {
	for _, mode := range []string{"retain", "replace", "disable", "cancel", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			w, ui, _, home := fixture(t)
			cfg := config.Default()
			cfg.RepositoryDirs = []string{filepath.Join(home, "repos"), filepath.Join(home, "extra")}
			if err := os.Mkdir(cfg.RepositoryDirs[1], 0755); err != nil {
				t.Fatal(err)
			}
			cfg.Workspace.RootDir = filepath.Join(home, "workspaces")
			cfg.Obsidian.VaultPath = filepath.Join(home, "notes")
			cfg.Model = "custom-model"
			cfg.Thinking = "high"
			cfg.Tmux.Windows[0].Name = "custom-pi"
			yes, no := true, false
			cfg.GitHub.Enabled = &no
			cfg.Jira.Enabled = &yes
			cfg.Datadog.Enabled = &yes
			cfg.Jira.BaseURL = "https://example.atlassian.net"
			cfg.Jira.Email = "you@example.com"
			cfg.Jira.CloudID = "cloud-fixture"
			cfg.Jira.APIBaseURL = "https://api.example.test/rest/api/3"
			cfg.LinkingMarkPrefixes = []string{"ABC", "XYZ"}
			cfg.Datadog.Site = "datadoghq.eu"
			cfg.Datadog.MonitorQuery = "tag:team:platform"
			if err := config.Create(cfg); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveSecrets(config.Secrets{"jira": {"api_token": "old-jira"}, "datadog": {"api_key": "old-api", "app_key": "old-app"}, "other": {"token": "untouched"}}); err != nil {
				t.Fatal(err)
			}
			path, _ := config.Path()
			secretPath, _ := config.SecretsPath()
			before, _ := os.ReadFile(path)
			secretBefore, _ := os.ReadFile(secretPath)
			ui.inputs = []string{"<keep>", "<keep>", "<keep>"}
			ui.decisions = []bool{false, true, true, mode != "cancel"}
			token := ""
			wantToken := "old-jira"
			if mode == "replace" {
				token = "new-jira"
				wantToken = token
			}
			if mode == "disable" {
				ui.decisions = []bool{false, false, false, true}
			} else {
				ui.inputs = append(ui.inputs, "<keep>", "<keep>", token, "<keep>", "<keep>", "<keep>", "", "", "<keep>")
			}
			w.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/rest/api/3/myself":
					_, value, _ := req.BasicAuth()
					if value != wantToken {
						t.Error("Jira verification did not use selected secret")
					}
				case "/api/v1/monitor/search":
					if req.Header.Get("DD-API-KEY") != "old-api" || req.Header.Get("DD-APPLICATION-KEY") != "old-app" {
						t.Error("Datadog secrets were not retained")
					}
				default:
					t.Fatalf("unexpected discovery/request on repeat setup: %s", req.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})
			if mode == "concurrent" {
				ui.beforeConfirm = func(title string) {
					if title == "Save this configuration?" {
						if err := os.WriteFile(path, []byte(`model: changed`), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			err := w.run()
			if mode == "cancel" || mode == "concurrent" {
				if err == nil {
					t.Fatal("expected cancellation/conflict")
				}
				if mode == "cancel" {
					after, _ := os.ReadFile(path)
					if string(after) != string(before) {
						t.Fatal("cancel changed config")
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				loaded, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(loaded.RepositoryDirs, cfg.RepositoryDirs) || !reflect.DeepEqual(loaded.Tmux, cfg.Tmux) || loaded.Model != cfg.Model || loaded.Thinking != cfg.Thinking || !reflect.DeepEqual(loaded.Jira.StatusMapping, cfg.Jira.StatusMapping) {
					t.Fatal("unmanaged settings lost")
				}
				if *loaded.Jira.Enabled != (mode != "disable") || *loaded.Datadog.Enabled != (mode != "disable") {
					t.Fatal("wrong enablement")
				}
				secrets, err := config.LoadSecrets()
				if err != nil {
					t.Fatal(err)
				}
				if secrets["jira"]["api_token"] != wantToken || secrets["datadog"]["app_key"] != "old-app" || secrets["other"]["token"] != "untouched" {
					t.Fatal("secrets not preserved/updated correctly")
				}
			}
			if mode != "replace" {
				after, _ := os.ReadFile(secretPath)
				if string(after) != string(secretBefore) {
					t.Fatal("unchanged secrets were rewritten")
				}
			}
			for _, q := range ui.questions {
				if q.secret && q.initial != "" {
					t.Fatal("secret was prefilled")
				}
			}
			for _, value := range []string{"old-jira", "new-jira", "old-api", "old-app", "untouched"} {
				if strings.Contains(ui.transcript.String(), value) {
					t.Fatal("secret leaked")
				}
			}
			if !ui.defaults["Connect Radar to Jira?"] || !ui.defaults["Connect Radar to Datadog?"] || ui.defaults["Connect Radar to GitHub?"] {
				t.Fatal("existing integration selections not preselected")
			}
		})
	}
}

func TestSetupUsesEnvironmentSecretWithoutPersistingIt(t *testing.T) {
	w, ui, _, _ := fixture(t)
	t.Setenv("RADAR_JIRA_API_TOKEN", "environment-secret")
	updates := config.Secrets{}
	got, err := w.secret("Jira API token", "", "RADAR_JIRA_API_TOKEN", "jira", "api_token", updates)
	if err != nil || got != "environment-secret" || len(updates) != 0 || len(ui.questions) != 0 || strings.Contains(ui.transcript.String(), got) {
		t.Fatal("environment secret was prompted, exposed or copied")
	}
}

func TestMissingDirectoriesArePreviewedThenCreatedOnlyOnSave(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(fmt.Sprint(save), func(t *testing.T) {
			w, ui, _, home := fixture(t)
			repo := filepath.Join(home, "new parent", "repos")
			ui.inputs[0] = repo
			ui.decisions = []bool{false, false, false, save}
			ui.beforeConfirm = func(title string) {
				if title != "Save this configuration?" {
					return
				}
				for _, path := range []string{repo, filepath.Join(home, "workspaces"), filepath.Join(home, "notes")} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("preview created %s", path)
					}
				}
				for _, want := range []string{"Radar settings · CREATE", "Credentials · NOT NEEDED", "Radar tmux settings · CREATE", "User tmux configuration · CREATE", "── Directories", "[CREATE] " + repo, "Tasks", "Already completed"} {
					if !strings.Contains(ui.transcript.String(), want) {
						t.Fatalf("missing review %q: %s", want, ui.transcript.String())
					}
				}
			}
			err := w.run()
			if save && err != nil || !save && !errors.Is(err, ErrAborted) {
				t.Fatal(err)
			}
			for _, path := range []string{repo, filepath.Join(home, "workspaces"), filepath.Join(home, "notes", "Tasks")} {
				_, err := os.Stat(path)
				if save && err != nil || !save && !os.IsNotExist(err) {
					t.Fatalf("path %s: %v", path, err)
				}
			}
		})
	}
}

func TestInstallConsentDefaultsToYesButSaveDoesNot(t *testing.T) {
	w, ui, _, _ := fixture(t)
	ui.decisions = []bool{true, true}
	if err := w.allowInstall("Install extension?"); err != nil {
		t.Fatal(err)
	}
	if err := w.allow("Save this configuration?"); err != nil {
		t.Fatal(err)
	}
	if !ui.defaults["Install extension?"] || ui.defaults["Save this configuration?"] {
		t.Fatal(ui.defaults)
	}
}
