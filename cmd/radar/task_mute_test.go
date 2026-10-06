package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/notification"
	"radar/internal/protocol"
	"radar/internal/version"
)

func captureTaskCommandOutput(t *testing.T, output **os.File, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := *output
	*output = writer
	defer func() {
		*output = original
		_ = reader.Close()
		_ = writer.Close()
	}()
	run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTaskMuteAndUnmuteCLIRequests(t *testing.T) {
	for _, command := range []string{"mute", "unmute"} {
		t.Run(command, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "radar-mute-cli-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "s")
			t.Setenv("RADAR_SOCKET", path)
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			requests := make(chan protocol.Request, 2)
			finished := make(chan struct{})
			wantTask := protocol.Task{ID: 7, Title: "Ship release", Attention: "attention", Muted: command == "mute"}
			go func() {
				defer close(finished)
				for range 2 {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					var request protocol.Request
					if json.NewDecoder(conn).Decode(&request) == nil {
						requests <- request
						response := protocol.Response{OK: true, Task: &wantTask}
						if request.Method == "version" {
							response = protocol.Response{OK: true, Version: version.Current()}
						}
						_ = json.NewEncoder(conn).Encode(response)
					}
					_ = conn.Close()
				}
			}()
			output := captureTaskCommandOutput(t, &os.Stdout, func() { runTask([]string{command, "7"}) })
			var gotTask protocol.Task
			if err := json.Unmarshal([]byte(output), &gotTask); err != nil || !reflect.DeepEqual(gotTask, wantTask) {
				t.Fatalf("CLI task output = %s, error = %v, want %+v", output, err, wantTask)
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("CLI did not finish its daemon requests")
			}
			if first := <-requests; first.Method != "version" {
				t.Fatalf("initial request = %+v, want version check", first)
			}
			request := <-requests
			wantRequest := protocol.Request{Method: "task-" + command, TaskMutation: &protocol.TaskMutation{TaskID: 7}}
			if !reflect.DeepEqual(request, wantRequest) {
				t.Fatalf("mutation request = %+v, want %+v", request, wantRequest)
			}
		})
	}
}

func TestTaskHelpDocumentsMuteAndUnmuteWithoutRestoreAlias(t *testing.T) {
	for _, usage := range []func(){usage, taskUsage} {
		output := captureTaskCommandOutput(t, &os.Stderr, usage)
		for _, want := range []string{"radar task mute <task-id>", "radar task unmute <task-id>"} {
			if !strings.Contains(output, want) {
				t.Fatalf("help did not include %q: %s", want, output)
			}
		}
		for _, legacy := range []string{"restore", "radar task ignore ", "radar task unignore "} {
			if strings.Contains(output, legacy) {
				t.Fatalf("mute feature advertises legacy command %q", legacy)
			}
		}
	}
}

func TestNotifyActionableTransitionsSuppressesMutedAndUnmuteMutation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sender := &recordingNotificationSender{}
	service := notification.NewWithSender(logger, sender)
	previous := []protocol.Task{{ID: 3, Title: "Explicitly unmuted", Attention: "attention", Muted: true}}
	current := []protocol.Task{
		{ID: 1, Title: "Muted urgent", Attention: "immediate", Muted: true},
		{ID: 2, Title: "Muted done", Attention: "done", Muted: true},
		{ID: 3, Title: "Explicitly unmuted", Attention: "attention"},
		{ID: 4, Title: "Useful", Attention: "attention"},
	}
	notifyActionableTransitions(context.Background(), previous, current, logger, integration.NewRegistry(), service)
	if !reflect.DeepEqual(sender.titles, []string{"Radar: Useful"}) {
		t.Fatalf("notification titles = %#v, want only useful task", sender.titles)
	}
	if current[0].Attention != "immediate" || !current[0].Muted || !previous[0].Muted {
		t.Fatal("notification gating mutated underlying task facts")
	}
}
