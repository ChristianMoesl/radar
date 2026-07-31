//go:build darwin

package notification

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlatformSenderIsDisabledWhenNotifierIsNotInstalled(t *testing.T) {
	radar := filepath.Join(t.TempDir(), "bin", "radar")
	if sender := newPlatformSenderForExecutable(radar); sender != nil {
		t.Fatalf("sender = %#v, want nil without installed notifier", sender)
	}
}

func TestPlatformSenderFindsInstalledNotifier(t *testing.T) {
	prefix := t.TempDir()
	radar := filepath.Join(prefix, "bin", "radar")
	notifier := notifierPath(radar)
	if err := os.MkdirAll(filepath.Dir(notifier), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notifier, []byte("helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if sender := newPlatformSenderForExecutable(radar); sender == nil {
		t.Fatal("sender is nil with installed notifier")
	}
}

func TestNotifierPathUsesInstalledPrefixLayout(t *testing.T) {
	radar := filepath.Join(string(filepath.Separator), "Users", "me", ".local", "bin", "radar")
	want := filepath.Join(string(filepath.Separator), "Users", "me", ".local", "libexec", "radar", "RadarNotifier.app", "Contents", "MacOS", "radar-notifier")
	if got := notifierPath(radar); got != want {
		t.Fatalf("notifierPath(%q) = %q, want %q", radar, got, want)
	}
}
