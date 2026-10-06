package client

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestMuteTaskRequestsAndResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		call   func(string, int) (protocol.Response, error)
	}{
		{name: "mute", method: "task-mute", call: MuteTask},
		{name: "unmute", method: "task-unmute", call: UnmuteTask},
	} {
		for _, ok := range []bool{true, false} {
			t.Run(tc.name+map[bool]string{true: "/success", false: "/daemon-error"}[ok], func(t *testing.T) {
				// Use a short isolated Unix socket path on both Linux and macOS.
				dir, err := os.MkdirTemp("/tmp", "radar-mute-client-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(dir) })
				path := filepath.Join(dir, "s")
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				requests := make(chan protocol.Request, 1)
				want := protocol.Response{OK: ok, Revision: 42}
				if ok {
					want.Task = &protocol.Task{ID: 7, Title: "Ship release", Attention: "attention", Muted: tc.method == "task-mute"}
					want.Tasks = []protocol.Task{*want.Task}
				} else {
					want.Error = "task 7 not found"
				}
				go func() {
					connection, err := listener.Accept()
					if err != nil {
						return
					}
					defer connection.Close()
					var request protocol.Request
					if json.NewDecoder(connection).Decode(&request) != nil {
						return
					}
					requests <- request
					_ = json.NewEncoder(connection).Encode(want)
				}()
				got, err := tc.call(path, 7)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("response = %+v, error = %v, want %+v", got, err, want)
				}
				select {
				case request := <-requests:
					wantRequest := protocol.Request{Method: tc.method, TaskMutation: &protocol.TaskMutation{TaskID: 7}}
					if !reflect.DeepEqual(request, wantRequest) {
						t.Fatalf("request = %+v, want %+v", request, wantRequest)
					}
				case <-time.After(time.Second):
					t.Fatal("mute request was not received")
				}
			})
		}
	}
}
