package client

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticAgentStopClientNestedObservationsFailClosed(t *testing.T) {
	var base map[string]any
	_ = json.Unmarshal(agentStopWireFixture(), &base)
	v := base["diagnostic_agent_stop"].(map[string]any)
	v["mode"], v["outcome"], v["operation_id"], v["dispatch_evidence"], v["resource_refresh_pending"] = "apply", "native_handler_returned_unverified", "operation", "native_stop_callback_entered_not_process_outcome", true
	v["observation"] = map[string]any{"evidence": "bounded_native_stop_reads", "registration": "matching_stopped_definition", "runner": "no_listener_and_no_live_observed_owner", "observed_at": time.Now().UTC().Format(time.RFC3339Nano)}
	raw, _ := json.Marshal(base)
	if _, err := decodeDiagnosticAgentStop(raw); err != nil {
		t.Fatal("finite independently observed result rejected", err)
	}
	for _, key := range []string{"evidence", "registration", "runner", "observed_at"} {
		for _, mutation := range []string{"missing", "null", "private", "duplicate", "folded"} {
			t.Run(key+"/"+mutation, func(t *testing.T) {
				var changed map[string]any
				_ = json.Unmarshal(raw, &changed)
				o := changed["diagnostic_agent_stop"].(map[string]any)["observation"].(map[string]any)
				switch mutation {
				case "missing":
					delete(o, key)
				case "null":
					o[key] = nil
				case "private":
					o[key] = "PRIVATE"
				case "folded":
					o[strings.ToUpper(key)] = o[key]
					delete(o, key)
				}
				bad, _ := json.Marshal(changed)
				if mutation == "duplicate" {
					bad = []byte(strings.Replace(string(bad), `"observation":{`, `"observation":{"`+key+`":"PRIVATE",`, 1))
				}
				got, err := decodeDiagnosticAgentStop(bad)
				if err == nil || got.DiagnosticAgentStop != nil || strings.Contains(err.Error(), "PRIVATE") {
					t.Fatal("nested private/malformed observation accepted or echoed", err)
				}
			})
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"observation":{`, `"observation":{"hash":"PRIVATE",`, 1),
		strings.Replace(string(raw), `"observation":{`, `"observation":{"checkpoint":{"payload":"PRIVATE"},`, 1),
		strings.Replace(string(raw), `"registration":"matching_stopped_definition"`, `"registration":"matching_stopped_definition","regist\u0072ation":"original_preserved"`, 1),
		strings.Replace(string(raw), `"problem":"none"`, `"problem":"cancelled_or_timed_out"`, 1),
	} {
		if _, err := decodeDiagnosticAgentStop([]byte(bad)); err == nil {
			t.Fatal("private or inconsistent native observation accepted")
		}
	}
}
