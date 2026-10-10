package notification

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSetupDefersWithoutLaunchingOrChangingPermissions(t *testing.T) {
	var out bytes.Buffer
	if err := setup(strings.NewReader("d\n"), &out, "missing", func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("launched after defer")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "N in Radar") {
		t.Fatal("advertised removed shortcut")
	}
	for _, text := range []string{"TWO independent approvals", "Open Anyway", "Allow notifications", "not grant", "Radar remains usable", "radar setup notifications"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal(text)
		}
	}
}
func TestSetupLinksAreUserInitiatedAndFailuresKeepDirections(t *testing.T) {
	var out bytes.Buffer
	var links []string
	setup(strings.NewReader("p\nn\nd\n"), &out, "helper", func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "open" {
			t.Fatal(name)
		}
		links = append(links, args[0])
		return nil, errors.New("unavailable")
	})
	if len(links) != 2 || links[0] != PrivacySettings || links[1] != NotificationSettings {
		t.Fatal(links)
	}
	if !strings.Contains(out.String(), "written directions") {
		t.Fatal(out.String())
	}
}
func TestSetupDoesNotConfuseProcessSuccessWithAuthorizationOrClick(t *testing.T) {
	for _, status := range []string{"authorization=denied delivered=0", "authorization=authorized delivered=1"} {
		var out bytes.Buffer
		setup(strings.NewReader("t\nn\nd\n"), &out, filepath.Join(t.TempDir(), "libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier"), func(_ context.Context, name string, args ...string) ([]byte, error) {
			if args[0] == "--status" {
				return []byte(status), nil
			}
			return nil, nil
		})
		if strings.Contains(out.String(), "Notifications are ready") {
			t.Fatal("claimed success without user confirmation")
		}
	}
}

func TestSetupVerifiesAndRegistersBeforeLaunching(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "prefix with spaces", "libexec/radar/RadarNotifier.app")
	helper := filepath.Join(bundle, "Contents/MacOS/radar-notifier")
	var out bytes.Buffer
	var calls []string
	err := setup(strings.NewReader("t\ny\nd\n"), &out, helper, func(ctx context.Context, name string, args ...string) ([]byte, error) {
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
	if err != nil || len(calls) != 4 || !strings.Contains(out.String(), "Notifications are ready") {
		t.Fatalf("setup: %v; calls=%v; output=%s", err, calls, out.String())
	}
}

func TestSetupVerificationAndRegistrationFailuresDoNotLaunch(t *testing.T) {
	for _, failure := range []string{"signature", "registration"} {
		t.Run(failure, func(t *testing.T) {
			var out bytes.Buffer
			var calls []string
			err := setup(strings.NewReader("t\nd\n"), &out, "/prefix/libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier", func(_ context.Context, name string, _ ...string) ([]byte, error) {
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
			if err != nil || len(calls) != wantCalls || !strings.Contains(out.String(), wantMessage) || !strings.Contains(out.String(), "fixture failure") || strings.Contains(out.String(), "Notifications are ready") {
				t.Fatalf("setup: %v; calls=%v; output=%s", err, calls, out.String())
			}
		})
	}
}

func TestSetupRetriesRegistrationOnlyWhenRequested(t *testing.T) {
	var out bytes.Buffer
	registrations, launches := 0, 0
	err := setup(strings.NewReader("t\nt\nd\n"), &out, "/prefix/libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier", func(_ context.Context, name string, args ...string) ([]byte, error) {
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
