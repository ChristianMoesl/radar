package client

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"radar/internal/protocol"
	"testing"
	"time"
)

func TestCallWithTimeout(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(map[bool]string{false: "response", true: "stalled daemon"}[stalled], func(t *testing.T) {
			// Keep Unix socket paths within macOS's short path limit.
			dir, err := os.MkdirTemp("/tmp", "radar-client-")
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
			done := make(chan struct{})
			defer close(done)
			method := make(chan string, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				var req protocol.Request
				if json.NewDecoder(conn).Decode(&req) != nil {
					return
				}
				method <- req.Method
				if stalled {
					<-done
					return
				}
				_ = json.NewEncoder(conn).Encode(protocol.Response{OK: true})
			}()
			start := time.Now()
			res, err := CallWithTimeout(path, "refresh-local", 100*time.Millisecond)
			if stalled {
				if err == nil {
					t.Fatal("stalled request did not time out")
				}
			} else if err != nil || !res.OK {
				t.Fatalf("response = %+v, error = %v", res, err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("request exceeded its deadline")
			}
			select {
			case got := <-method:
				if got != "refresh-local" {
					t.Fatalf("method = %s", got)
				}
			case <-time.After(time.Second):
				t.Fatal("request not received")
			}
		})
	}
}
