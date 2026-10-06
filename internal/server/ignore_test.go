package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestIgnoreSocketMethodsUseStructuredTaskMutation(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"task-ignore", "task-unignore"} {
		t.Run(method, func(t *testing.T) {
			for _, mode := range []string{"success", "error", "unconfigured"} {
				t.Run(mode, func(t *testing.T) {
					calls := 0
					server := New(store, logger, nil, nil, nil, integration.NewRegistry(), cleanup.New(nil))
					if mode != "unconfigured" {
						server.SetTaskMutation(func(_ context.Context, gotMethod string, mutation *protocol.TaskMutation) (protocol.Task, error) {
							calls++
							if gotMethod != method || mutation == nil || *mutation != (protocol.TaskMutation{TaskID: 7}) {
								return protocol.Task{}, errors.New("incorrect method or target")
							}
							if mode == "error" {
								return protocol.Task{}, errors.New("cannot persist ignore preference")
							}
							return protocol.Task{ID: 7, Title: "Ship release", Attention: "attention", Ignored: method == "task-ignore"}, nil
						})
					}
					serverConn, clientConn := net.Pipe()
					finished := make(chan struct{})
					go func() {
						defer close(finished)
						server.handle(serverConn)
					}()
					defer func() {
						_ = clientConn.Close()
						<-finished
					}()
					request := protocol.Request{Method: method, TaskMutation: &protocol.TaskMutation{TaskID: 7}}
					if err := json.NewEncoder(clientConn).Encode(request); err != nil {
						t.Fatal(err)
					}
					var response protocol.Response
					if err := json.NewDecoder(clientConn).Decode(&response); err != nil {
						t.Fatal(err)
					}
					if mode == "success" {
						if !response.OK || response.Task == nil || response.Task.ID != 7 || response.Task.Ignored != (method == "task-ignore") || response.Task.Attention != "attention" || response.Tasks == nil || response.Summary == nil || calls != 1 {
							t.Fatalf("mutation response = %+v, calls = %d", response, calls)
						}
					} else if response.OK || response.Task != nil || response.Error == "" {
						t.Fatalf("failed mutation response = %+v", response)
					}
					if mode == "unconfigured" && calls != 0 {
						t.Fatal("unconfigured mutation invoked callback")
					}
				})
			}
		})
	}
}
