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

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"radar/internal/app"
	"radar/internal/config"
	"radar/internal/setup"
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
	sandboxChoice bool
	reviewed      *setupReview
}

func (u *fakeUI) Input(q question) (string, error) {
	u.titles = append(u.titles, q.Title)
	u.questions = append(u.questions, q)
	if q.Title == u.cancelAt {
		return "", ErrAborted
	}
	if len(u.inputs) == 0 {
		return "", fmt.Errorf("unexpected input: %s", q.Title)
	}
	value := u.inputs[0]
	if value == "<keep>" {
		value = q.Initial
	}
	u.inputs = u.inputs[1:]
	if q.Validate != nil {
		if err := q.Validate(value); err != nil {
			return "", err
		}
	}
	if q.Secret {
		u.Print("%s: [hidden]\n", q.Title)
	} else {
		u.Print("%s: %s\n", q.Title, value)
	}
	return value, nil
}
func (u *fakeUI) MultiSelect(q setup.MultiSelectQuestion) ([]string, error) {
	return nil, fmt.Errorf("unexpected multi-select: %s", q.Title)
}

func (u *fakeUI) Confirm(title string, initial bool) (bool, error) {
	// Existing wizard scenarios leave the separately tested Terraform opt-in off.
	if strings.HasPrefix(title, "Use Jira account ") {
		return true, nil
	}
	if title == "Connect Radar to Terraform?" {
		return false, nil
	}
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
	if title == sandboxQuestion {
		return u.sandboxChoice, nil
	}
	if len(u.decisions) == 0 {
		return false, fmt.Errorf("unexpected confirmation: %s", title)
	}
	result := u.decisions[0]
	u.decisions = u.decisions[1:]
	return result, nil
}
func (u *fakeUI) Print(format string, args ...any) { fmt.Fprintf(&u.transcript, format, args...) }

func (u *fakeUI) Style() lipgloss.Style {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	return r.NewStyle()
}

func (u *fakeUI) review(report setupReview) error {
	u.reviewed = &report
	u.Print("%s\n", report.summary(u.Style()))
	save, err := u.Confirm("Save this configuration?", true)
	if err != nil {
		return err
	}
	if !save {
		return ErrAborted
	}
	return nil
}

type fakeSystem struct {
	missing      map[string]bool
	calls        []string
	installErr   error
	loginMissing bool
	goos         string
	nodeVersion  string
	versions     map[string]string
	outputErrors map[string]error
	sandboxFn    func(context.Context, ...string) (string, error)
}

func (s *fakeSystem) sandbox(ctx context.Context, args ...string) (string, error) {
	s.calls = append(s.calls, "sbx "+strings.Join(args, " "))
	if s.sandboxFn != nil {
		return s.sandboxFn(ctx, args...)
	}
	if len(args) > 0 && args[0] == "version" {
		return `{"client":{"version":"v0.48.0"}}`, nil
	}
	return `[]`, nil
}
func (s *fakeSystem) Platform() string {
	if s.goos != "" {
		return s.goos
	}
	return "darwin"
}
func (s *fakeSystem) LookPath(name string) bool { return !s.missing[name] }
func (s *fakeSystem) Output(ctx context.Context, cmd setup.Command) (string, error) {
	name, args := cmd.Name, cmd.Args
	if name == "sbx" {
		return s.sandbox(ctx, args...)
	}
	s.calls = append(s.calls, name+" "+strings.Join(args, " "))
	if err := s.outputErrors[name]; err != nil {
		return "", err
	}
	if output, ok := s.versions[name]; ok {
		return output, nil
	}
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
	if name == "twg" {
		if len(args) > 0 && args[0] == "--version" {
			return "1.3.8", nil
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "auth profile list") {
			profile := "radar"
			for i, a := range args {
				if a == "--profile" && i+1 < len(args) {
					profile = args[i+1]
				}
			}
			return fmt.Sprintf(`{"apiVersion":"v2","data":{"profiles":[{"name":%q,"authMethod":"oauth","cloudId":"cloud-fixture","userId":"fixture-account","user":"fixture@example.invalid","domain":"example.atlassian.net","environment":"prod"}]}}`, profile), nil
		}
		if strings.Contains(joined, "doctor") {
			return `{"data":{"auth":{"resolved":{"authMethod":"oauth"}},"connectivity":{"attempted":true,"ok":true,"userId":"ari:cloud:identity::user/fixture-account"}}}`, nil
		}
		return `{"apiVersion":"v2","command":"jira.workitem.query","data":{"issues":[]}}`, nil
	}
	if name == "pup" {
		if len(args) > 0 && args[0] == "--version" {
			return "pup 2.0.0", nil
		}
		if strings.Contains(strings.Join(args, " "), "auth status") {
			return `{"authenticated":true,"has_refresh":true,"token_type":"Bearer","site":"datadoghq.eu","org":"radar","status":"valid","scopes":["monitors_read"]}`, nil
		}
		return `{"metadata":{"total_count":0,"page_count":0},"monitors":[]}`, nil
	}
	if name == "gh" && len(args) > 0 {
		if args[0] == "--version" {
			return "gh version 2.88.0", nil
		}
		if args[0] == "api" {
			return `{"login":"fixture-user"}`, nil
		}
	}
	if name == "gh" && len(args) > 0 && args[0] == "auth" && s.loginMissing {
		return "", errors.New("not authenticated")
	}
	return "", nil
}
func (s *fakeSystem) Run(_ context.Context, cmd setup.Command) error {
	name, args := cmd.Name, cmd.Args
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
		os.Unsetenv(key)
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
	w := testWizard(ui, sys)
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
	ui.inputs = append(ui.inputs, "https://example.atlassian.net/", "radar", "abc, XYZ abc", "https://api.datadoghq.eu", "radar", "tag:team:platform")
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
		if ui.reviewed == nil || !strings.Contains(ui.reviewed.files[0].details, `cloud_id: cloud-fixture`) {
			t.Fatal("config preview not available before confirmation")
		}
		if strings.Contains(ui.transcript.String(), `cloud_id: cloud-fixture`) {
			t.Fatal("config body was dumped into the summary")
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
	if len(secrets["jira"]) != 0 || len(secrets["datadog"]) != 0 {
		t.Fatal("secrets were not saved")
	}
	configPath, _ := config.Path()
	data, _ := os.ReadFile(configPath)
	for _, secret := range []string{"jira-fixture-secret", "dd-api-fixture-secret", "dd-app-fixture-secret"} {
		if strings.Contains(ui.transcript.String(), secret) || strings.Contains(string(data), secret) || strings.Contains(strings.Join(sys.calls, "\n"), secret) {
			t.Fatal("secret leaked outside secrets.yaml")
		}
	}
	if len(requests) != 1 {
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
	for _, cancel := range []string{"Where do you check out your repositories?", "TWG OAuth profile name", "Save this configuration?", "decline save", "decline extension install"} {
		t.Run(cancel, func(t *testing.T) {
			w, ui, _, home := fixture(t)
			ui.cancelAt = cancel
			ui.decisions = []bool{false, false, false, false}
			if cancel == "TWG OAuth profile name" {
				ui.decisions = []bool{false, true}
				ui.inputs = append(ui.inputs, "https://example.atlassian.net")
				w.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"cloudId":"cloud-fixture"}`))}, nil
				})
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
	if !errors.Is(err, ErrToolsNotReady) {
		t.Fatalf("expected reported tool failure: %v", err)
	}
	for _, want := range []string{"tmux", "Node.js", "brew install tmux", "existing Node.js installation", "run radar again"} {
		if !strings.Contains(ui.transcript.String(), want) {
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
	if !strings.Contains(ui.transcript.String(), "Already done: GitHub login. Cancelling will not undo these.") {
		t.Fatal("review did not identify the completed login")
	}
	if !ui.defaults["GitHub CLI is not authenticated. Run `gh auth login --hostname github.com`?"] {
		t.Fatal("GitHub login must default to Yes")
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
	ui.inputs = append(ui.inputs, "https://example.atlassian.net", "radar", "ABC", "datadoghq.eu", "radar", "tag:team:platform")
	ui.decisions = []bool{false, true, true, false}
	w.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"cloudId":"cloud-fixture"}`))}, nil
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
			cfg.Jira.AccountID = "fixture-account"
			cfg.Jira.CloudID = "cloud-fixture"
			cfg.Jira.Profile = "radar"
			cfg.LinkingMarkPrefixes = []string{"ABC", "XYZ"}
			cfg.Datadog.Site = "datadoghq.eu"
			cfg.Datadog.MonitorQuery = "tag:team:platform"
			if err := config.Create(cfg); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveSecrets(config.Secrets{"other": {"token": "untouched"}}); err != nil {
				t.Fatal(err)
			}
			path, _ := config.Path()
			secretPath, _ := config.SecretsPath()
			before, _ := os.ReadFile(path)
			secretBefore, _ := os.ReadFile(secretPath)
			ui.inputs = []string{"<keep>", "<keep>", "<keep>"}
			ui.decisions = []bool{false, true, true, mode != "cancel"}
			profile := "<keep>"
			if mode == "replace" {
				profile = "radar-alt"
			}
			if mode == "disable" {
				ui.decisions = []bool{false, false, false, true}
			} else {
				ui.inputs = append(ui.inputs, "<keep>", profile, "<keep>", "<keep>", "<keep>", "<keep>")
			}
			w.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/_edge/tenant_info" {
					t.Fatalf("unexpected credential-bearing request: %s", req.URL.Path)
				}
				if req.Header.Get("Authorization") != "" {
					t.Fatal("discovery must be unauthenticated")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"cloudId":"cloud-fixture"}`))}, nil
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
				if len(secrets["jira"]) != 0 || len(secrets["datadog"]) != 0 || secrets["other"]["token"] != "untouched" {
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
				if q.Secret && q.Initial != "" {
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
	t.Setenv("RADAR_TEST_SECRET", "environment-secret")
	updates := config.Secrets{}
	got, err := w.secret("Custom secret", "", "RADAR_TEST_SECRET", "custom", "token", updates)
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
				for _, want := range []string{"CREATE", "Radar settings", "Agent instructions", "Popup shortcut and extended keys", "Load Radar's tmux settings", "New directories", "~/new parent/repos", "Tasks"} {
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

func TestInstallAndReviewConsentDefaultToYes(t *testing.T) {
	w, ui, _, _ := fixture(t)
	ui.decisions = []bool{true, true}
	if err := func() error {
		yes, err := w.ui.Confirm("Install extension?", true)
		if err != nil {
			return err
		}
		if !yes {
			return ErrAborted
		}
		return nil
	}(); err != nil {
		t.Fatal(err)
	}
	if err := ui.review(setupReview{}); err != nil {
		t.Fatal(err)
	}
	if !ui.defaults["Install extension?"] || !ui.defaults["Save this configuration?"] {
		t.Fatal(ui.defaults)
	}
}

func TestWizardCreatesAgentInstructionsOnlyAfterConsent(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(fmt.Sprint(save), func(t *testing.T) {
			w, ui, _, home := fixture(t)
			path := filepath.Join(home, "config", "radar", "AGENTS.md")
			ui.decisions = []bool{false, false, false, save}
			ui.beforeConfirm = func(title string) {
				if title != "Save this configuration?" {
					return
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("instructions written before consent")
				}
				if !strings.Contains(ui.transcript.String(), "Agent instructions") || strings.Contains(ui.transcript.String(), "# Radar agent instructions") {
					t.Fatal("instructions should have a summary, not an upfront body dump")
				}
				found := false
				for _, file := range ui.reviewed.files {
					if file.path == path && file.action == "CREATE" && strings.Contains(file.details, "# Radar agent instructions") {
						found = true
					}
				}
				if !found {
					t.Fatal("instruction contents unavailable for review")
				}
			}
			err := w.run()
			if !save {
				if !errors.Is(err, ErrAborted) {
					t.Fatalf("expected cancellation, got %v", err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("cancelled setup wrote instructions")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), "# Radar agent instructions") {
				t.Fatalf("missing instructions: %v", err)
			}
		})
	}
}

func TestWizardPreservesInstructionsCreatedDuringReview(t *testing.T) {
	w, ui, _, home := fixture(t)
	path := filepath.Join(home, "config", "radar", "AGENTS.md")
	ui.decisions = []bool{false, false, false, true}
	ui.beforeConfirm = func(title string) {
		if title == "Save this configuration?" {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("my instructions"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "my instructions" {
		t.Fatalf("replaced user instructions: %q, %v", data, err)
	}
}

func testWizard(ui prompter, sys setup.System) wizard {
	return newWizard(ui, sys, app.DefaultIntegrations())
}

const sandboxQuestion = "Enable SBX for new workspaces?"
const sandboxPolicyQuestion = "Initialize SBX's global network policy?"
