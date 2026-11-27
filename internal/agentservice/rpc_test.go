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
	"time"
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
			path := filepath.Join(agentRuntimeTestDir(t), "agent.sock")
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

type cancelRPCMarshal struct{ cancel context.CancelFunc }

func (v cancelRPCMarshal) MarshalJSON() ([]byte, error) {
	v.cancel()
	return []byte(`{}`), nil
}

type cancelRPCDecode struct{ cancel context.CancelFunc }

func (v *cancelRPCDecode) UnmarshalJSON([]byte) error {
	v.cancel()
	return nil
}

func TestAgentRPCCancellationBoundariesNeverCertifySuccess(t *testing.T) {
	for _, boundary := range []string{"before_dial", "expired_deadline", "during_marshal", "during_decode", "before_reply", "before_void_reply"} {
		t.Run(boundary, func(t *testing.T) {
			reg := probeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var params any
			result := json.RawMessage(`{"unchanged":true}`)
			var target any = &result
			if boundary == "before_dial" {
				cancel()
			}
			if boundary == "expired_deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer deadlineCancel()
			}
			if boundary == "during_marshal" {
				params = cancelRPCMarshal{cancel}
			}
			if boundary == "during_decode" {
				target = &cancelRPCDecode{cancel}
			}
			if boundary == "before_void_reply" {
				target = nil
			}
			calls := rawDiagnosticAgentPeer(t, reg, func(string) string {
				if boundary == "before_reply" || boundary == "before_void_reply" {
					cancel()
				}
				return `{"jsonrpc":"2.0","id":1,"result":{"accepted":true}}`
			})
			path, _ := socketPath(reg.ID)
			wantErr := error(context.Canceled)
			if boundary == "expired_deadline" {
				wantErr = context.DeadlineExceeded
			}
			if err := call(ctx, path, "shutdown", params, target); !errors.Is(err, wantErr) {
				t.Errorf("cancelled native RPC certified success or lost context: %v", err)
			}
			wantCalls := 1
			if boundary == "before_dial" || boundary == "expired_deadline" || boundary == "during_marshal" {
				wantCalls = 0
			}
			if len(calls()) != wantCalls {
				t.Error("cancelled RPC dispatched after its pre-write boundary or retried", calls())
			}
			if boundary != "during_decode" && string(result) != `{"unchanged":true}` {
				t.Error("cancelled RPC decoded its target")
			}
		})
	}
}

func TestAgentRPCCancellationInterruptsConnectedReadWithoutRetry(t *testing.T) {
	reg := probeFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	calls := rawDiagnosticAgentPeer(t, reg, func(string) string {
		close(started)
		<-release
		return `{"jsonrpc":"2.0","id":1,"result":{"accepted":true}}`
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path, _ := socketPath(reg.ID)
	done := make(chan error, 1)
	go func() { done <- call(ctx, path, "shutdown", nil, nil) }()
	// Synchronize on real request receipt, not sleeps or a test-only executor.
	select {
	case <-started:
	case err := <-done:
		close(release)
		t.Fatal("RPC ended before peer received its request", err)
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("peer did not receive RPC within the fixture bound")
	}
	cancel()
	var err error
	select {
	case err = <-done:
		close(release)
	case <-time.After(time.Second):
		t.Error("caller cancellation did not interrupt connected RPC read")
		close(release) // Drain the old behavior without waiting for its 5s cap.
		err = <-done
	}
	if !errors.Is(err, context.Canceled) || len(calls()) != 1 {
		t.Error("cancelled in-flight RPC certified success, lost context or retried", err, calls())
	}
}
