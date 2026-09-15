package tmux

import (
	"context"
	"os"
	"path/filepath"
	"radar/internal/protocol"
	"strings"
	"testing"
)

func TestPublishActivityUsesOnlyCurrentPane(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "args")
	executable := filepath.Join(dir, "tmux")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ACTIVITY_TEST_OUTPUT\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ACTIVITY_TEST_OUTPUT", output)
	t.Setenv("TMUX_PANE", "%7")
	for _, activity := range []protocol.Activity{protocol.ActivityBusy, protocol.ActivityWaiting, protocol.ActivityIdle} {
		if err := (Source{}).PublishActivity(context.Background(), activity); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		want := "set-option\n-p\n-t\n%7\n@radar_activity\n" + activity.String() + "\n"
		if activity == protocol.ActivityIdle {
			want = "set-option\n-p\n-u\n-t\n%7\n@radar_activity\n"
		}
		if string(data) != want {
			t.Fatalf("arguments = %q, want %q", data, want)
		}
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_PANE", "")
	if err := (Source{}).PublishActivity(context.Background(), protocol.ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("published without a pane")
	}
	if err := (Source{}).PublishActivity(context.Background(), protocol.Activity("invalid")); err == nil {
		t.Fatal("accepted invalid activity")
	}
	t.Setenv("TMUX_PANE", "%7")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\necho unavailable >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := (Source{}).PublishActivity(context.Background(), protocol.ActivityWaiting); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("publication failure = %v", err)
	}
}
