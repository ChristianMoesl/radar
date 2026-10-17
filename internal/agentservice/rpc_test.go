package agentservice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"radar/internal/protocol"
	"runtime"
	"strings"
	"testing"
)

func TestSharedProtocolFixturesAndBoundedCorrelation(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../agent-host/test/fixtures/protocol.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Protocol int `json:"protocol"`
		Hello    struct {
			Response json.RawMessage `json:"response"`
		} `json:"hello"`
		Invalid json.RawMessage `json:"invalid"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Protocol != protocol.AgentProtocolVersion {
		t.Fatal("language-neutral protocol version differs")
	}
	cases := []struct {
		name, response string
		code           int
		valid          bool
	}{
		{"hello", string(fixture.Hello.Response), 0, true},
		{"structured error", string(fixture.Invalid), -32602, false},
		{"wrong correlation", `{"jsonrpc":"2.0","id":2,"result":{}}`, 0, false},
		{"wrong protocol", `{"jsonrpc":"1.0","id":1,"result":{}}`, 0, false},
		{"malformed", "{", 0, false},
		{"oversized", strings.Repeat("x", protocol.AgentMaxFrameBytes+1), 0, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_, _ = bufio.NewReader(conn).ReadString('\n')
				_, _ = conn.Write([]byte(tt.response + "\n"))
			}()
			var result json.RawMessage
			err = call(context.Background(), path, "hello", nil, &result)
			if tt.valid && err != nil {
				t.Fatal(err)
			}
			if !tt.valid && err == nil {
				t.Fatal("invalid response accepted")
			}
			if tt.code != 0 {
				var rpc *protocol.AgentRPCError
				if !errors.As(err, &rpc) || rpc.Code != tt.code {
					t.Fatalf("lost structured code: %v", err)
				}
			}
			<-done
		})
	}
}
