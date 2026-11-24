package agentservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func rawDiagnosticAgentPeer(t *testing.T, reg Registration, response func(string) string) func() []string {
	t.Helper()
	path, _ := socketPath(reg.ID)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := []string{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			var request struct {
				Method string `json:"method"`
			}
			if json.NewDecoder(conn).Decode(&request) == nil {
				mu.Lock()
				calls = append(calls, request.Method)
				mu.Unlock()
				_, _ = conn.Write([]byte(response(request.Method) + "\n"))
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string{}, calls...) }
}

func ambiguousDiagnosticSnapshots(raw string) map[string]string {
	cases := map[string]string{}
	for _, field := range []string{"protocol", "instanceId", "definitionVersion", "pending", "presentation", "attention", "revision"} {
		marker := `"` + field + `":`
		start := strings.Index(raw, marker)
		if start < 0 {
			panic("snapshot fixture member missing")
		}
		// Repeat the original value through a decoded raw-message map. Even equal
		// duplicates are ambiguous evidence, not a license to choose the last one.
		var values map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &values)
		if field == "attention" || field == "revision" {
			_ = json.Unmarshal(values["presentation"], &values)
		}
		repeated := marker + string(values[field]) + "," + marker
		cases[field+"_duplicate"] = strings.Replace(raw, marker, repeated, 1)
		cases[field+"_folded"] = strings.Replace(raw, marker, `"`+strings.ToUpper(field)+`":`, 1)
	}
	cases["escaped_identity_duplicate"] = strings.Replace(raw, `"instanceId":`, `"\u0069nstanceId":"other","instanceId":`, 1)
	cases["conflicting_identity_duplicate"] = strings.Replace(raw, `"instanceId":`, `"instanceId":"other","instanceId":`, 1)
	cases["conflicting_pending_duplicate"] = strings.Replace(raw, `"pending":`, `"pending":-1,"pending":`, 1)
	cases["conflicting_attention_duplicate"] = strings.Replace(raw, `"attention":true`, `"attention":false,"attention":true`, 1)
	return cases
}

func TestDiagnosticAgentRejectsAmbiguousSnapshotMembers(t *testing.T) {
	fixture := probeSnapshot(Registration{ID: "worker"})
	encoded, _ := json.Marshal(fixture)
	for name, raw := range ambiguousDiagnosticSnapshots(string(encoded)) {
		t.Run(name, func(t *testing.T) {
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			path := filepath.Join(dir, "snapshot.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			calls := rawDiagnosticAgentPeer(t, reg, func(method string) string {
				if method == "hello" {
					return `{"jsonrpc":"2.0","id":1,"result":{"protocol":1,"instanceId":"worker"}}`
				}
				return `{"jsonrpc":"2.0","id":1,"result":` + raw + `}`
			})
			items, err := DiagnosticAgents(reg.Workspace.Path, reg.ID)
			if err != nil || len(items) != 1 {
				t.Fatal("registration lost", err)
			}
			if items[0].Snapshot != nil || items[0].SnapshotEvidence != "invalid_or_incompatible" || items[0].SnapshotSavedAt != "" {
				t.Error("ambiguous cached snapshot became usable evidence")
			}
			if len(calls()) != 0 {
				t.Fatal("cached refusal contacted runner")
			}
			live, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
			if err != nil || live.Snapshot != nil || live.SnapshotEvidence != "unavailable" || live.Problem == "none" {
				t.Error("ambiguous live snapshot became usable evidence")
			}
			output, _ := json.Marshal(items)
			if strings.Contains(string(output), "private-") {
				t.Fatal("private snapshot content escaped")
			}
		})
	}
}

func TestDiagnosticAgentRejectsAmbiguousHelloBeforeSnapshot(t *testing.T) {
	for name, hello := range map[string]string{
		"identity_duplicate":         `{"protocol":1,"instanceId":"other","instanceId":"worker"}`,
		"equal_identity_duplicate":   `{"protocol":1,"instanceId":"worker","instanceId":"worker"}`,
		"escaped_identity_duplicate": `{"protocol":1,"\u0069nstanceId":"other","instanceId":"worker"}`,
		"protocol_duplicate":         `{"protocol":99,"protocol":1,"instanceId":"worker"}`,
		"folded_protocol":            `{"PROTOCOL":1,"instanceId":"worker"}`,
		"folded_identity":            `{"protocol":1,"INSTANCEID":"worker"}`,
	} {
		t.Run(name, func(t *testing.T) {
			reg := probeFixture(t)
			called := rawDiagnosticAgentPeer(t, reg, func(method string) string {
				if method == "hello" {
					return `{"jsonrpc":"2.0","id":1,"result":` + hello + `}`
				}
				raw, _ := json.Marshal(probeSnapshot(reg))
				return `{"jsonrpc":"2.0","id":1,"result":` + string(raw) + `}`
			})
			view, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
			if err != nil || view.Snapshot != nil || view.SnapshotEvidence != "unavailable" || view.RunnerEvidence != "unknown_not_reached" || len(called()) != 1 {
				t.Fatal("ambiguous hello reached usable snapshot evidence or another native method")
			}
		})
	}
}

func TestAgentRPCRejectsAmbiguousOrMissingResultWithoutRetry(t *testing.T) {
	for name, frame := range map[string]string{
		"duplicate_result":      `{"jsonrpc":"2.0","id":1,"result":{"private":"private-first"},"result":{}}`,
		"duplicate_correlation": `{"jsonrpc":"2.0","id":2,"id":1,"result":{}}`,
		"equal_correlation":     `{"jsonrpc":"2.0","id":1,"id":1,"result":{}}`,
		"escaped_correlation":   `{"jsonrpc":"2.0","\u0069d":2,"id":1,"result":{}}`,
		"duplicate_protocol":    `{"jsonrpc":"1.0","jsonrpc":"2.0","id":1,"result":{}}`,
		"folded_result":         `{"jsonrpc":"2.0","id":1,"RESULT":{}}`,
		"folded_id":             `{"jsonrpc":"2.0","ID":1,"result":{}}`,
		"folded_protocol":       `{"JSONRPC":"2.0","id":1,"result":{}}`,
		"missing_result":        `{"jsonrpc":"2.0","id":1}`,
		"null_error":            `{"jsonrpc":"2.0","id":1,"error":null}`,
		"both_result_and_error": `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-1,"message":"private-error"}}`,
		"result_and_null_error": `{"jsonrpc":"2.0","id":1,"result":{},"error":null}`,
		"duplicate_error_code":  `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"code":-2,"message":"private-error"}}`,
		"folded_error_message":  `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"MESSAGE":"private-error"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			reg := probeFixture(t)
			called := rawDiagnosticAgentPeer(t, reg, func(string) string { return frame })
			path, _ := socketPath(reg.ID)
			target := json.RawMessage(`{"unchanged":true}`)
			err := call(context.Background(), path, "hello", nil, &target)
			if err == nil || string(target) != `{"unchanged":true}` || len(called()) != 1 {
				t.Fatal("ambiguous/missing result mutated target, succeeded or retried")
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("ambiguous private frame leaked in error")
			}
		})
	}
}

func TestAgentDiagnosticMemberValidationPreservesNativePrivateExtensions(t *testing.T) {
	for _, mode := range []string{"native", "unique_escaped_identity", "private_case_distinct_siblings", "null_result"} {
		t.Run(mode, func(t *testing.T) {
			reg := probeFixture(t)
			snapshot, _ := json.Marshal(probeSnapshot(reg))
			raw := string(snapshot)
			if mode == "unique_escaped_identity" {
				raw = strings.Replace(raw, `"instanceId":`, `"\u0069nstanceId":`, 1)
			}
			if mode == "private_case_distinct_siblings" {
				raw = strings.TrimSuffix(raw, "}") + `,"privateA":{"same":1,"SAME":2},"PRIVATEA":[{"other":3}]}`
			}
			called := rawDiagnosticAgentPeer(t, reg, func(method string) string {
				result := raw
				if method == "hello" {
					result = `{"protocol":1,"instanceId":"worker","private":"private-hello"}`
				}
				if mode == "null_result" {
					result = "null"
				}
				return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":%s,"private":"private-envelope"}`, result)
			})
			if mode == "null_result" {
				path, _ := socketPath(reg.ID)
				var target json.RawMessage
				if err := call(context.Background(), path, "hello", nil, &target); err != nil || string(target) != "null" || len(called()) != 1 {
					t.Fatal("explicit null native result rejected", err)
				}
				return
			}
			dir, _ := instanceDir(reg.ID)
			if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cached, err := DiagnosticAgents(reg.Workspace.Path, reg.ID)
			if err != nil || len(cached) != 1 || cached[0].Snapshot == nil {
				t.Fatal("valid cached extension rejected", err)
			}
			live, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
			if err != nil || live.Snapshot == nil || live.Problem != "none" || len(called()) != 2 {
				t.Fatal("valid native extension rejected", err)
			}
		})
	}
}

func TestAgentRPCRejectsMissingOutcomeForVoidCall(t *testing.T) {
	for _, frame := range []string{
		`{"jsonrpc":"2.0","id":1}`,
		`{"jsonrpc":"2.0","id":1,"error":null}`,
		`{"jsonrpc":"2.0","id":1,"result":null,"error":null}`,
	} {
		reg := probeFixture(t)
		called := rawDiagnosticAgentPeer(t, reg, func(string) string { return frame })
		path, _ := socketPath(reg.ID)
		if err := call(context.Background(), path, "fixture-void", nil, nil); err == nil || len(called()) != 1 {
			t.Fatal("missing/ambiguous outcome became successful void call or retried")
		}
	}
}

func TestAgentResponseObjectBoundsAndPrivateExtensionIsolation(t *testing.T) {
	for name, raw := range map[string]string{
		"empty": "", "null": "null", "array": "[]", "scalar": "1",
		"trailing": `{} {}`, "syntax": `{"private":}`, "duplicate_private": `{"private":1,"private":2}`,
		"escaped_duplicate": `{"id":1,"\u0069d":1}`, "folded_known": `{"ID":1}`,
		"oversized": strings.Repeat(" ", 1024*1024+1),
	} {
		t.Run(name, func(t *testing.T) {
			members, err := agentResponseObject([]byte(raw), "id")
			if err == nil || members != nil || err.Error() != "invalid agent response object" {
				t.Fatal("invalid object returned partial/private evidence")
			}
		})
	}
	for _, count := range []int{64, 65} {
		fields := make([]string, count)
		for i := range fields {
			fields[i] = fmt.Sprintf(`"private-%d":{"nested":1,"NESTED":2}`, i)
		}
		members, err := agentResponseObject([]byte("{"+strings.Join(fields, ",")+"}"), "id")
		if (count == 64 && (err != nil || len(members) != count)) || (count == 65 && (err == nil || members != nil)) {
			t.Fatal("selected-object bound or unrelated private subtree semantics changed")
		}
	}
}
