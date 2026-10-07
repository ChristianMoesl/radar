package notification

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
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
