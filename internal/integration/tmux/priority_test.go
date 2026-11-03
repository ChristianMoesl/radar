package tmux

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func TestRealTmuxPromptLifetimesResurfaceLowWithoutChurn(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	dir, err := os.MkdirTemp(".", ".prompt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "s")
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(binary, append([]string{"-S", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("isolated tmux %v: %v, %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("new-session", "-d", "-s", "low-prompts")
	t.Cleanup(func() { _ = exec.Command(binary, "-S", socket, "kill-server").Run() })
	pane := run("display-message", "-p", "-t", "low-prompts", "#{pane_id}")
	wrapper := "#!/bin/sh\nexec \"$PROMPT_TEST_TMUX\" -S \"$PROMPT_TEST_SOCKET\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPT_TEST_TMUX", binary)
	t.Setenv("PROMPT_TEST_SOCKET", socket)
	t.Setenv("TMUX_PANE", pane)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	note := protocol.SourceRef{ID: "note:one", Role: protocol.SourceRefRoleAuthoritative, Authored: true, Authority: protocol.SourceRefAuthorityPrimary, Signal: "low_priority", Metadata: map[string]string{"priority": "low"}}
	task := protocol.Task{SourceRefs: []protocol.SourceRef{note}}
	publish := func(activity protocol.Activity, ids ...string) {
		t.Helper()
		if err := (Source{}).PublishActivity(ctx, integration.ActivityPublication{Activity: activity, PromptIDs: ids}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		refs, status := FetchSessions(ctx, logger, linking.MarkMatcher{})
		if status.Status != "ok" || len(refs) != 1 {
			t.Fatalf("isolated collection: %+v %v", status, refs)
		}
		refs[0].Signal = "in_progress"
		task.SourceRefs = []protocol.SourceRef{task.SourceRefs[0], refs[0]}
		view, _ := protocol.ProjectAttention(task, nil)
		if view.Attention != want || view.Priority != "low" {
			t.Fatalf("projection = %+v, want %s with retained Low", view, want)
		}
	}
	publish(protocol.ActivityWaiting, "parent-one")
	check("attention")
	task.SourceRefs[0].LowPriorityBaseline = protocol.CaptureLowPriorityBaseline(task)
	check("low_priority")
	first := append([]string(nil), task.SourceRefs[1].ActionableSignals...)
	publish(protocol.ActivityWaiting, "parent-one")
	check("low_priority")
	if !reflect.DeepEqual(first, task.SourceRefs[1].ActionableSignals) {
		t.Fatal("polling/re-publication manufactured a prompt occurrence")
	}
	publish(protocol.ActivityWaiting, "parent-one", "child-one")
	check("attention")
	task.SourceRefs[0].LowPriorityBaseline = protocol.CaptureLowPriorityBaseline(task)
	publish(protocol.ActivityWaiting, "parent-one")
	check("low_priority")
	publish(protocol.ActivityBusy)
	check("low_priority")
	publish(protocol.ActivityIdle)
	check("low_priority")
	publish(protocol.ActivityWaiting, "parent-two")
	check("attention")
}

func TestPromptPublicationRejectsInvalidIdentitiesWithoutWriting(t *testing.T) {
	t.Setenv("TMUX_PANE", "%7")
	t.Setenv("PATH", t.TempDir())
	for _, publication := range []integration.ActivityPublication{
		{Activity: protocol.ActivityWaiting, PromptIDs: []string{"contains,separator"}},
		{Activity: protocol.ActivityWaiting, PromptIDs: []string{"line\nbreak"}},
		{Activity: protocol.ActivityWaiting, PromptIDs: []string{";"}},
		{Activity: protocol.ActivityWaiting, PromptIDs: []string{"#(not-a-prompt)"}},
		{Activity: protocol.ActivityWaiting, PromptIDs: []string{""}},
		{Activity: protocol.ActivityBusy, PromptIDs: []string{"not-a-prompt"}},
	} {
		if err := (Source{}).PublishActivity(context.Background(), publication); err == nil || strings.Contains(err.Error(), "not found") {
			t.Fatalf("invalid publication reached tmux: %v", err)
		}
	}
}
