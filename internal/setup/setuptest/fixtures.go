// Package setuptest supplies in-memory foreground interactions for setup tests.
package setuptest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"radar/internal/setup"
)

type UI struct {
	Completed     []string
	Questions     []setup.Question
	Defaults      map[string]bool
	Inputs        []string
	Decisions     []bool
	Titles        []string
	Transcript    strings.Builder
	CancelAt      string
	BeforeConfirm func(string)
	TmuxChoice    bool
	SandboxChoice bool
}

func (u *UI) Input(q setup.Question) (string, error) {
	u.Titles = append(u.Titles, q.Title)
	u.Questions = append(u.Questions, q)
	if q.Title == u.CancelAt {
		return "", setup.ErrAborted
	}
	if len(u.Inputs) == 0 {
		return "", fmt.Errorf("unexpected input: %s", q.Title)
	}
	value := u.Inputs[0]
	if value == "<keep>" {
		value = q.Initial
	}
	u.Inputs = u.Inputs[1:]
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
func (u *UI) Confirm(title string, initial bool) (bool, error) {
	u.Titles = append(u.Titles, title)
	if u.Defaults == nil {
		u.Defaults = map[string]bool{}
	}
	u.Defaults[title] = initial
	if title == "Add Radar's prefix + r popup binding?" {
		return u.TmuxChoice, nil
	}
	if u.BeforeConfirm != nil {
		u.BeforeConfirm(title)
	}
	if title == u.CancelAt {
		return false, setup.ErrAborted
	}
	if title == "Enable SBX for new workspaces?" {
		return u.SandboxChoice, nil
	}
	if len(u.Decisions) == 0 {
		return false, fmt.Errorf("unexpected confirmation: %s", title)
	}
	result := u.Decisions[0]
	u.Decisions = u.Decisions[1:]
	return result, nil
}
func (u *UI) Print(format string, args ...any) { fmt.Fprintf(&u.Transcript, format, args...) }

func (u *UI) Style() lipgloss.Style {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	return r.NewStyle()
}

type System struct {
	Missing      map[string]bool
	Calls        []string
	InstallErr   error
	LoginMissing bool
	Goos         string
	NodeVersion  string
	Versions     map[string]string
	OutputErrors map[string]error
	SandboxFn    func(context.Context, ...string) (string, error)
}

func (s *System) sandbox(ctx context.Context, args ...string) (string, error) {
	s.Calls = append(s.Calls, "sbx "+strings.Join(args, " "))
	if s.SandboxFn != nil {
		return s.SandboxFn(ctx, args...)
	}
	if len(args) > 0 && args[0] == "version" {
		return `{"client":{"version":"v0.48.0"}}`, nil
	}
	return `[]`, nil
}
func (s *System) Platform() string {
	if s.Goos != "" {
		return s.Goos
	}
	return "darwin"
}
func (s *System) LookPath(name string) bool { return !s.Missing[name] }
func (s *System) Output(ctx context.Context, cmd setup.Command) (string, error) {
	name, args := cmd.Name, cmd.Args
	if name == "sbx" {
		return s.sandbox(ctx, args...)
	}
	s.Calls = append(s.Calls, name+" "+strings.Join(args, " "))
	if err := s.OutputErrors[name]; err != nil {
		return "", err
	}
	if output, ok := s.Versions[name]; ok {
		return output, nil
	}
	if name == "tmux" {
		return "tmux 3.6", nil
	}
	if name == "node" {
		if s.NodeVersion != "" {
			return s.NodeVersion, nil
		}
		return "v24.8.0", nil
	}
	if name == "pi" {
		return "1.0.0", nil
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
	if name == "gh" && len(args) > 0 && args[0] == "auth" && s.LoginMissing {
		return "", errors.New("not authenticated")
	}
	return "", nil
}
func (s *System) Run(_ context.Context, cmd setup.Command) error {
	name, args := cmd.Name, cmd.Args
	s.Calls = append(s.Calls, name+" "+strings.Join(args, " "))
	if s.InstallErr != nil {
		return s.InstallErr
	}
	if name == "brew" {
		delete(s.Missing, args[1])
		if args[1] == "node" {
			s.NodeVersion = "v24.8.0"
		}
	}
	if name == "gh" {
		s.LoginMissing = false
	}
	return nil
}

func Home(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("PI_CODING_AGENT_DIR", "")
	InstallRadar(t, filepath.Join(home, ".pi", "agent"))
	return home
}
func InstallRadar(t *testing.T, dir string) {
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
