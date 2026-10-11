package notification

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSetupDefersWithoutLaunchingOrChangingPermissions(t *testing.T) {
	var out bytes.Buffer
	if err := setup(strings.NewReader("s\n"), &out, "missing", func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("launched after defer")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Notifications · Optional", "Get desktop alerts", "[t/Enter] Set up & test", "[s] Skip for now", "[h] Help", "notification settings left as they are", "radar setup notifications"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal(text)
		}
	}
	for _, text := range []string{"TWO independent approvals", "Open Anyway", "Launch Services", "Companion:", "[p]", "[n]"} {
		if strings.Contains(out.String(), text) {
			t.Fatalf("upfront detail %q: %s", text, out.String())
		}
	}
	if strings.Count(out.String(), "\n") > 10 {
		t.Fatalf("intro is too long: %s", out.String())
	}
}
func TestSetupLinksAreUserInitiatedAndFailuresKeepDirections(t *testing.T) {
	var out bytes.Buffer
	var links []string
	setup(strings.NewReader("h\np\nn\ns\n"), &out, "helper", func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "open" {
			t.Fatal(name)
		}
		links = append(links, args[0])
		return nil, errors.New("unavailable")
	})
	if len(links) != 2 || links[0] != PrivacySettings || links[1] != NotificationSettings {
		t.Fatal(links)
	}
	if !strings.Contains(out.String(), "Open manually: "+privacyDirections) || !strings.Contains(out.String(), "Open manually: "+notificationDirections) {
		t.Fatal(out.String())
	}
}
func TestSetupDoesNotConfuseProcessSuccessWithAuthorizationOrClick(t *testing.T) {
	for _, status := range []string{"authorization=denied delivered=0", "authorization=authorized delivered=1"} {
		var out bytes.Buffer
		setup(strings.NewReader("t\nn\ns\n"), &out, filepath.Join(t.TempDir(), "libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier"), func(_ context.Context, name string, args ...string) ([]byte, error) {
			if args[0] == "--status" {
				return []byte(status), nil
			}
			return nil, nil
		})
		if strings.Contains(out.String(), "✓ Notifications ready") {
			t.Fatal("claimed success without user confirmation")
		}
	}
}

func TestSetupVerifiesAndRegistersBeforeLaunching(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "prefix with spaces", "libexec/radar/RadarNotifier.app")
	helper := filepath.Join(bundle, "Contents/MacOS/radar-notifier")
	var out bytes.Buffer
	var calls []string
	err := setup(strings.NewReader("t\ny\n"), &out, helper, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if _, bounded := ctx.Deadline(); !bounded {
			t.Fatal("unbounded setup subprocess")
		}
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch len(calls) {
		case 1:
			if name != "/usr/bin/codesign" || !reflect.DeepEqual(args, []string{"--verify", "--strict", bundle}) {
				t.Fatalf("signature check: %s %q", name, args)
			}
		case 2:
			if name != launchServicesRegister || !reflect.DeepEqual(args, []string{"-f", bundle}) {
				t.Fatalf("registration: %s %q", name, args)
			}
		case 3:
			if name != helper || len(args) != 2 || args[0] != "--test" {
				t.Fatalf("test launch: %s %q", name, args)
			}
		case 4:
			if name != helper || !reflect.DeepEqual(args, []string{"--status"}) {
				t.Fatalf("status: %s %q", name, args)
			}
			return []byte("authorization=authorized delivered=1"), nil
		default:
			t.Fatalf("unexpected command: %s %q", name, args)
		}
		return nil, nil
	})
	if err != nil || len(calls) != 4 || !strings.Contains(out.String(), "✓ Notifications ready") {
		t.Fatalf("setup: %v; calls=%v; output=%s", err, calls, out.String())
	}
}

func TestSetupVerificationAndRegistrationFailuresDoNotLaunch(t *testing.T) {
	for _, failure := range []string{"signature", "registration"} {
		t.Run(failure, func(t *testing.T) {
			var out bytes.Buffer
			var calls []string
			err := setup(strings.NewReader("t\ns\n"), &out, "/prefix/libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier", func(_ context.Context, name string, _ ...string) ([]byte, error) {
				calls = append(calls, name)
				if name == "/usr/bin/codesign" && failure == "signature" || name == launchServicesRegister && failure == "registration" {
					return []byte("fixture failure"), errors.New("exit status 1")
				}
				if name != "/usr/bin/codesign" {
					t.Fatalf("launched unexpected command after failure: %s", name)
				}
				return nil, nil
			})
			wantCalls := 1
			wantMessage := "Could not verify"
			if failure == "registration" {
				wantCalls, wantMessage = 2, "Could not register"
			}
			if err != nil || len(calls) != wantCalls || !strings.Contains(out.String(), wantMessage) || !strings.Contains(out.String(), "fixture failure") || strings.Contains(out.String(), "✓ Notifications ready") {
				t.Fatalf("setup: %v; calls=%v; output=%s", err, calls, out.String())
			}
		})
	}
}

func TestSetupRetriesRegistrationOnlyWhenRequested(t *testing.T) {
	var out bytes.Buffer
	registrations, launches := 0, 0
	err := setup(strings.NewReader("t\nt\ns\n"), &out, "/prefix/libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier", func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "/usr/bin/codesign":
			return nil, nil
		case launchServicesRegister:
			registrations++
			if registrations == 1 {
				return nil, errors.New("registration unavailable")
			}
		default:
			if args[0] == "--test" {
				launches++
			} else {
				return []byte("authorization=denied delivered=0"), nil
			}
		}
		return nil, nil
	})
	if err != nil || registrations != 2 || launches != 1 {
		t.Fatalf("setup=%v registrations=%d launches=%d", err, registrations, launches)
	}
}

func TestSetupHelpAndEOFNeverLaunch(t *testing.T) {
	for _, input := range []string{"", "h\ns\n"} {
		var out bytes.Buffer
		err := setup(strings.NewReader(input), &out, "/private/companion", func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("help or EOF executed a command")
			return nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if input == "" {
			continue
		}
		for _, want := range []string{"TWO independent approvals", "Open Anyway", "Allow notifications", "not grant", "Radar remains usable", "Never disable Gatekeeper or remove quarantine", "Companion: /private/companion", "[p] Privacy & Security", "[n] Notification Settings"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("help lost %q: %s", want, out.String())
			}
		}
	}
}

func TestSetupEnterTestsAndConfirmationFinishes(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "RadarNotifier.app/Contents/MacOS/radar-notifier")
	marker := failureMarker(helper)
	if err := os.WriteFile(marker, []byte("previous failure"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	calls := 0
	err := setup(strings.NewReader("\ny\n"), &out, helper, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if args[0] == "--status" {
			return []byte("authorization=authorized delivered=1"), nil
		}
		return nil, nil
	})
	if err != nil || calls != 4 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	text := out.String()
	if !strings.HasSuffix(text, "✓ Notifications ready\n") || strings.Count(text, "[t/Enter]") != 1 {
		t.Fatalf("success did not finish immediately: %s", text)
	}
	for _, unwanted := range []string{"Companion:", "macOS status:", "Launch Services", "Open Anyway", "Skipped"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("success includes %q: %s", unwanted, text)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("confirmed success did not clear failure marker")
	}
}

func TestSetupLaunchFailureIsNotAssumedToBePermissionDenial(t *testing.T) {
	var out bytes.Buffer
	calls := 0
	err := setup(strings.NewReader("t\np\ns\n"), &out, "helper", func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name == "helper" {
			if args[0] != "--test" {
				t.Fatal("status check after failed launch")
			}
			return []byte("missing library fixture"), errors.New("exit status 1")
		}
		if name == "open" && args[0] != PrivacySettings {
			t.Fatal("wrong settings pane")
		}
		return nil, nil
	})
	if err != nil || calls != 4 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	text := out.String()
	for _, want := range []string{"missing library fixture", "If macOS blocked RadarNotifier", "Otherwise fix the reported error", "[t/Enter] Retry", "[p] Privacy & Security", "Opening Settings does not grant permission"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	for _, unwanted := range []string{"Notifications are turned off", "Notification permission has not been granted", "✓ Notifications ready", "[n] Notification Settings"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("unjustified guidance %q: %s", unwanted, text)
		}
	}
}

func TestSetupStatusAndDeliveryRequireConfirmation(t *testing.T) {
	for _, tc := range []struct {
		status, answer, want string
		confirmed            bool
	}{
		{"denied", "", "Notifications are turned off", false},
		{"not_determined", "", "Notification permission has not been granted", false},
		{"unknown", "", "Could not determine notification permission", false},
		{"authorized-unexpected", "", "Could not determine notification permission", false},
		{"authorized", "n\n", "Check Notification Center, Focus and banner settings", false},
		{"authorized", "\n", "Not confirmed", false},
		{"authorized", "y\n", "✓ Notifications ready", true},
		{"provisional", "y\n", "✓ Notifications ready", true},
		{"ephemeral", "y\n", "✓ Notifications ready", true},
	} {
		t.Run(tc.status+"/"+tc.answer, func(t *testing.T) {
			helper := filepath.Join(t.TempDir(), "RadarNotifier.app/Contents/MacOS/radar-notifier")
			marker := failureMarker(helper)
			if err := os.WriteFile(marker, []byte("previous failure"), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := setup(strings.NewReader("t\n"+tc.answer+"s\n"), &out, helper, func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "--status" {
					return []byte("authorization=" + tc.status + " delivered=1"), nil
				}
				return nil, nil
			})
			if err != nil || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("err=%v output=%s", err, out.String())
			}
			if strings.Contains(out.String(), "✓ Notifications ready") != tc.confirmed {
				t.Fatalf("false success: %s", out.String())
			}
			_, err = os.Stat(marker)
			if os.IsNotExist(err) != tc.confirmed {
				t.Fatalf("failure marker removal disagrees with confirmation: %v", err)
			}
			if !tc.confirmed && !strings.Contains(out.String(), "[n] Notification Settings") {
				t.Fatalf("missing contextual settings: %s", out.String())
			}
		})
	}
}

func TestSetupStatusFailureDoesNotClaimDenial(t *testing.T) {
	var out bytes.Buffer
	err := setup(strings.NewReader("t\ns\n"), &out, "helper", func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "--status" {
			return []byte("status fixture"), errors.New("exit status 1")
		}
		return nil, nil
	})
	if err != nil || !strings.Contains(out.String(), "Could not check notification permission") || !strings.Contains(out.String(), "status fixture") || strings.Contains(out.String(), "Notifications are turned off") {
		t.Fatalf("err=%v output=%s", err, out.String())
	}
}

func TestSetupKeepsLongDiagnosticsInHelp(t *testing.T) {
	var out bytes.Buffer
	detail := strings.Repeat("long diagnostic ", 50) + "\ntail detail"
	err := setup(strings.NewReader("t\nh\ns\n"), &out, "helper", func(context.Context, string, ...string) ([]byte, error) {
		return []byte(detail), errors.New("signature failure")
	})
	before, after, ok := strings.Cut(out.String(), "Last check:")
	if err != nil || !ok || !strings.Contains(before, "Could not verify") || !strings.Contains(before, "details in Help") || strings.Contains(before, detail) || !strings.Contains(after, detail) {
		t.Fatalf("err=%v output=%s", err, out.String())
	}
}

func TestNotificationAuthorizationIsExact(t *testing.T) {
	for _, output := range []string{"", "authorization=", "authorization=authorized authorization=denied", "authorization= authorization=authorized"} {
		if got := notificationAuthorization([]byte(output)); got != "" {
			t.Fatalf("accepted ambiguous output %q as %q", output, got)
		}
	}
}
