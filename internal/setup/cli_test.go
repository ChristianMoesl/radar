package setup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"radar/internal/setup"
	"radar/internal/setup/setuptest"
)

type cliSystem struct {
	setuptest.System
	version      string
	present      bool
	installFails bool
	loggedIn     bool
	loginFails   bool
	afterOutput  func()
}

func (s *cliSystem) LookPath(string) bool { return s.present }
func (s *cliSystem) Output(_ context.Context, c setup.Command) (string, error) {
	s.Calls = append(s.Calls, c.Name+" "+strings.Join(c.Args, " "))
	version := s.version
	if s.afterOutput != nil {
		s.afterOutput()
	}
	return version, nil
}
func (s *cliSystem) Run(_ context.Context, c setup.Command) error {
	s.Calls = append(s.Calls, c.Name+" "+strings.Join(c.Args, " "))
	switch c.Name {
	case "install":
		if s.installFails {
			return errors.New("secret must not appear")
		}
		s.present = true
		s.version = "tool 1.3.8"
	case "tool":
		if s.loginFails {
			return errors.New("secret must not appear")
		}
		s.loggedIn = true
	}
	return nil
}
func connection(s *cliSystem) setup.CLIConnection {
	return setup.CLIConnection{Name: "tool", Service: "Service", MinimumVersion: "1.3.8", Reason: "OAuth requests", InstallInstructions: "official install command", Install: &setup.Command{Name: "install"}, Login: setup.Command{Name: "tool", Args: []string{"login"}}, Verify: func(context.Context) error {
		if !s.loggedIn {
			return errors.New("secret must not appear")
		}
		return nil
	}}
}
func TestConnectionInstallsRechecksAndStartsLogin(t *testing.T) {
	s := &cliSystem{}
	ui := &setuptest.UI{Decisions: []bool{true, true, true}}
	var completed []string
	ok, err := setup.ConnectCLI(context.Background(), ui, s, connection(s), func(v string) { completed = append(completed, v) })
	if err != nil || !ok {
		t.Fatalf("connected=%v err=%v", ok, err)
	}
	if strings.Join(s.Calls, ";") != "install ;tool --version;tool login" {
		t.Fatal(s.Calls)
	}
	if len(completed) != 2 {
		t.Fatal(completed)
	}
	if !strings.Contains(ui.Transcript.String(), "official install command") {
		t.Fatal(ui.Transcript.String())
	}
}
func TestConnectionManualInstallationRechecksPathAndVersion(t *testing.T) {
	s := &cliSystem{}
	ui := &setuptest.UI{Decisions: []bool{false, true}}
	ui.BeforeConfirm = func(title string) {
		if strings.HasPrefix(title, "Recheck") {
			s.present = true
			s.version = "tool 1.3.8"
			s.loggedIn = true
		}
	}
	ok, err := setup.ConnectCLI(context.Background(), ui, s, connection(s), nil)
	if err != nil || !ok {
		t.Fatalf("connected=%v err=%v", ok, err)
	}
	if strings.Join(s.Calls, ";") != "tool --version" {
		t.Fatal(s.Calls)
	}
}
func TestConnectionDoesNotReplaceAnExistingInstallation(t *testing.T) {
	s := &cliSystem{present: true, version: "tool 1.2.0"}
	ui := &setuptest.UI{Decisions: []bool{false}}
	ok, err := setup.ConnectCLI(context.Background(), ui, s, connection(s), nil)
	if err != nil || ok {
		t.Fatalf("connected=%v err=%v", ok, err)
	}
	if strings.Join(s.Calls, ";") != "tool --version" {
		t.Fatal(s.Calls)
	}
}
func TestConnectionFailuresCanBeDeferredWithoutLeakingDiagnostics(t *testing.T) {
	for _, phase := range []string{"install", "login"} {
		t.Run(phase, func(t *testing.T) {
			s := &cliSystem{}
			ui := &setuptest.UI{Decisions: []bool{true, false}}
			if phase == "install" {
				s.installFails = true
			} else {
				s.present = true
				s.version = "tool 1.3.8"
				s.loginFails = true
			}
			ok, err := setup.ConnectCLI(context.Background(), ui, s, connection(s), nil)
			if err != nil || ok {
				t.Fatalf("connected=%v err=%v", ok, err)
			}
			if strings.Contains(ui.Transcript.String(), "secret must not appear") {
				t.Fatal("diagnostic leaked")
			}
		})
	}
}
func TestConnectionRetriesLoginAndVerifiesAgain(t *testing.T) {
	s := &cliSystem{present: true, version: "tool 1.3.8", loginFails: true}
	ui := &setuptest.UI{Decisions: []bool{true, true, true}}
	ui.BeforeConfirm = func(title string) {
		if strings.HasPrefix(title, "Retry") {
			s.loginFails = false
		}
	}
	ok, err := setup.ConnectCLI(context.Background(), ui, s, connection(s), nil)
	if err != nil || !ok {
		t.Fatalf("connected=%v err=%v", ok, err)
	}
	if strings.Join(s.Calls, ";") != "tool --version;tool login;tool login" {
		t.Fatal(s.Calls)
	}
}
func TestConnectionHonorsCancellationBeforeLogin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &cliSystem{present: true, version: "tool 1.3.8", afterOutput: cancel}
	ui := &setuptest.UI{}
	ok, err := setup.ConnectCLI(ctx, ui, s, connection(s), nil)
	if ok || !errors.Is(err, context.Canceled) || len(ui.Titles) > 0 {
		t.Fatalf("connected=%v err=%v prompts=%v", ok, err, ui.Titles)
	}
}
func TestCLIVersionQualification(t *testing.T) {
	for _, tc := range []struct {
		value string
		ready bool
	}{
		{"tool v1.3.8", true}, {"1.4.0", true}, {"2.0.0", true}, {"1.3.7", false}, {"1.3.8-beta", false}, {"unknown", false},
	} {
		if got := setup.VersionAtLeast(tc.value, "1.3.8"); got != tc.ready {
			t.Errorf("%q ready=%v", tc.value, got)
		}
	}
}

func TestConnectionPreparationControlsLogin(t *testing.T) {
	for _, phase := range []string{"ready", "deferred", "failed", "aborted", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &cliSystem{present: true, version: "tool 1.3.8"}
			ui := &setuptest.UI{Decisions: []bool{true, false}}
			cli := connection(s)
			cli.PrepareLogin = func(context.Context) (bool, error) {
				if s.loggedIn {
					t.Fatal("preparation must happen before login")
				}
				switch phase {
				case "ready":
					return true, nil
				case "deferred":
					return false, nil
				case "failed":
					return false, errors.New("private diagnostic")
				case "aborted":
					return false, setup.ErrAborted
				default:
					cancel()
					return true, nil
				}
			}
			ok, err := setup.ConnectCLI(ctx, ui, s, cli, nil)
			var wantErr error
			if phase == "aborted" {
				wantErr = setup.ErrAborted
			} else if phase == "cancelled" {
				wantErr = context.Canceled
			}
			if ok != (phase == "ready") || !errors.Is(err, wantErr) || s.loggedIn != (phase == "ready") {
				t.Fatalf("connected=%v err=%v loggedIn=%v", ok, err, s.loggedIn)
			}
			if strings.Contains(ui.Transcript.String(), "private diagnostic") {
				t.Fatal("preparation diagnostic leaked")
			}
		})
	}
}
