package agentservice

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/protocol"
)

func invalidDiagnosticAgentSnapshots() map[string]func(map[string]any) {
	cases := map[string]func(map[string]any){
		"missing_protocol":     func(v map[string]any) { delete(v, "protocol") },
		"wrong_protocol":       func(v map[string]any) { v["protocol"] = 99 },
		"missing_identity":     func(v map[string]any) { delete(v, "instanceId") },
		"wrong_identity":       func(v map[string]any) { v["instanceId"] = "other" },
		"missing_presentation": func(v map[string]any) { delete(v, "presentation") },
		"null_presentation":    func(v map[string]any) { v["presentation"] = nil },
		"missing_attention":    func(v map[string]any) { delete(v["presentation"].(map[string]any), "attention") },
		"null_attention":       func(v map[string]any) { v["presentation"].(map[string]any)["attention"] = nil },
		"wrong_attention_type": func(v map[string]any) { v["presentation"].(map[string]any)["attention"] = "false" },
	}
	for _, field := range []string{"definitionVersion", "pending", "revision"} {
		for _, value := range []struct {
			name  string
			value any
		}{
			{"missing", nil}, {"null", nil}, {"negative", -1}, {"unsafe_integer", int64(9007199254740992)}, {"string", "0"}, {"fraction", 0.5},
		} {
			field, value := field, value
			cases[field+"_"+value.name] = func(v map[string]any) {
				target := v
				if field == "revision" {
					target = v["presentation"].(map[string]any)
				}
				if value.name == "missing" {
					delete(target, field)
				} else {
					target[field] = value.value
				}
			}
		}
	}
	cases["zero_definition"] = func(v map[string]any) { v["definitionVersion"] = 0 }
	return cases
}

func TestDiagnosticAgentCheckpointRequiresCompleteFiniteSnapshot(t *testing.T) {
	for name, mutate := range invalidDiagnosticAgentSnapshots() {
		t.Run(name, func(t *testing.T) {
			reg := probeFixture(t)
			value := probeSnapshot(reg)
			mutate(value)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			dir, _ := instanceDir(reg.ID)
			path := filepath.Join(dir, "snapshot.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			methods := probeServer(t, reg, func(method string) any {
				if method == "hello" {
					return map[string]any{"protocol": protocol.AgentProtocolVersion, "instanceId": reg.ID}
				}
				return value
			})
			items, err := DiagnosticAgents(reg.Workspace.Path, reg.ID)
			if err != nil || len(items) != 1 {
				t.Fatal("registration lost", err)
			}
			cached := items[0]
			if cached.SnapshotEvidence != "invalid_or_incompatible" || cached.Snapshot != nil || cached.SnapshotSavedAt != "" {
				t.Error("malformed checkpoint became usable cached counter evidence")
			}
			if cached.RunnerEvidence != "unknown_not_probed" || cached.OwnershipHold != "registered_owner_even_when_stopped" || len(methods()) != 0 {
				t.Fatal("cached refusal changed runtime/ownership claims or contacted runner")
			}
			live, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
			if err != nil || live.Snapshot != nil || live.SnapshotEvidence != "unavailable" || (live.Problem != "invalid_snapshot" && live.Problem != "snapshot_unavailable") || live.RunnerEvidence != "reachable_matching_protocol_identity" {
				t.Fatal("live finite snapshot contract diverged", err)
			}
			encoded, _ := json.Marshal(items)
			after, _ := os.ReadFile(path)
			if strings.Contains(string(encoded), "private-") || !bytes.Equal(data, after) {
				t.Fatal("checkpoint inspection leaked private data or changed checkpoint")
			}
		})
	}
}

func TestDiagnosticAgentSnapshotValidCountersAgreeAcrossCachedAndLive(t *testing.T) {
	for _, mode := range []string{"native", "zero_counts", "safe_integer_maximum", "optional_problem_absent"} {
		t.Run(mode, func(t *testing.T) {
			reg := probeFixture(t)
			value := probeSnapshot(reg)
			switch mode {
			case "zero_counts":
				value["pending"] = 0
				value["presentation"].(map[string]any)["revision"] = 0
				value["presentation"].(map[string]any)["attention"] = false
			case "safe_integer_maximum":
				value["definitionVersion"], value["pending"] = int64(9007199254740991), int64(9007199254740991)
				value["presentation"].(map[string]any)["revision"] = int64(9007199254740991)
			case "optional_problem_absent":
				delete(value, "problem")
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			dir, _ := instanceDir(reg.ID)
			path := filepath.Join(dir, "snapshot.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			// Private token and independent checkpoints must not be fallbacks.
			if err := os.WriteFile(filepath.Join(dir, "ingress.token"), []byte("private-token"), 0600); err != nil {
				t.Fatal(err)
			}
			methods := probeServer(t, reg, func(method string) any {
				if method == "hello" {
					return map[string]any{"protocol": protocol.AgentProtocolVersion, "instanceId": reg.ID}
				}
				return value
			})
			items, err := DiagnosticAgents(reg.Workspace.Path, reg.ID)
			if err != nil || len(items) != 1 || items[0].SnapshotEvidence != "cached_runner_snapshot" || items[0].SnapshotSavedAt == "" || items[0].Snapshot == nil || len(methods()) != 0 {
				t.Fatal("valid cached counters lost", err)
			}
			live, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
			if err != nil || live.SnapshotEvidence != "fresh_runner_snapshot" || live.Problem != "none" || !reflect.DeepEqual(live.Snapshot, items[0].Snapshot) {
				t.Fatal("valid native cached/live projections disagree", err)
			}
			encoded, _ := json.Marshal(items)
			if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "987654") {
				t.Fatal("private payload leaked")
			}
		})
	}
}
