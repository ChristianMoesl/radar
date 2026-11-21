package server

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/protocol"
)

func rawMemberRPC(t *testing.T, s *Server, frame string) protocol.Response {
	t.Helper()
	daemon, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); s.handle(daemon) }()
	defer func() { client.Close(); <-done }()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(client, frame+"\n"); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestAmbiguousCleanupMembersNeverDispatchOrConsume(t *testing.T) {
	for _, mode := range []string{"method", "method_alias", "method_escape", "method_changes_route", "bundle", "plan", "plan_alias", "plan_escape", "targets", "source", "presentation", "safety", "operation", "operation_escape"} {
		t.Run(mode, func(t *testing.T) {
			s, provider, resource := cleanupPlanFixture(t)
			preview := requireCleanupPlan(t, s)
			raw, err := json.Marshal(preview)
			if err != nil {
				t.Fatal(err)
			}
			frame := `{"method":"cleanup","cleanup":` + string(raw) + `}`
			original := frame
			switch mode {
			case "method":
				frame = strings.Replace(frame, `"method":`, `"method":"cleanup","method":`, 1)
			case "method_alias":
				frame = strings.Replace(frame, `"method":`, `"METHOD":"summary","method":`, 1)
			case "method_escape":
				frame = strings.Replace(frame, `"method":`, `"\u006dethod":"summary","method":`, 1)
			case "method_changes_route":
				frame = strings.TrimSuffix(frame, "}") + `,"method":"gc"}`
			case "bundle":
				frame = strings.Replace(frame, `"cleanup":`, `"cleanup":null,"cleanup":`, 1)
			case "plan":
				frame = strings.Replace(frame, `"plan_id":`, `"plan_id":"foreign","plan_id":`, 1)
			case "plan_alias":
				frame = strings.Replace(frame, `"plan_id":`, `"PLAN_ID":"foreign","plan_id":`, 1)
			case "plan_escape":
				frame = strings.Replace(frame, `"plan_id":`, `"plan_\u0069d":"foreign","plan_id":`, 1)
			case "targets":
				frame = strings.Replace(frame, `"targets":`, `"targets":[],"targets":`, 1)
			case "source":
				frame = strings.Replace(frame, `"source":`, `"source":"other","source":`, 1)
			case "presentation":
				frame = strings.Replace(frame, `"presentation":`, `"presentation":{},"presentation":`, 1)
			case "safety":
				frame = strings.Replace(frame, `"blocks_automatic":`, `"blocks_automatic":false,"blocks_automatic":`, 1)
			case "operation":
				frame = strings.Replace(frame, `"private":`, `"private":"foreign","private":`, 1)
			case "operation_escape":
				frame = strings.Replace(frame, `"private":`, `"\u0070rivate":"foreign","private":`, 1)
			}
			if frame == original {
				t.Fatal("fixture did not introduce duplicate members")
			}
			before := s.store.Revision()
			var gcCalls atomic.Int32
			s.garbageCollect = func() (protocol.GarbageCollectionResult, error) {
				gcCalls.Add(1)
				return protocol.GarbageCollectionResult{}, nil
			}
			response := rawMemberRPC(t, s, frame)
			if response.OK || response.CleanupResult != nil || response.CleanupPreview != nil || response.Maintenance != nil || provider.admissions.Load() != 0 || provider.checks.Load() != 0 || provider.calls.Load() != 0 || gcCalls.Load() != 0 || s.store.Revision() != before {
				t.Fatalf("ambiguous frame reached effects: response=%+v admissions=%d checks=%d calls=%d gc=%d", response, provider.admissions.Load(), provider.checks.Load(), provider.calls.Load(), gcCalls.Load())
			}
			if got, err := os.ReadFile(resource); err != nil || string(got) != "intact" {
				t.Fatal("rejected frame changed resource", err)
			}
			if plan, ok := s.cleanupPlans[preview.TaskID]; !ok || plan.id != preview.PlanID {
				t.Fatal("ambiguous frame consumed the issued plan")
			}
			valid := refreshRPC(t, s, protocol.Request{Method: "cleanup", Cleanup: &preview})
			if !valid.OK || provider.calls.Load() != 1 {
				t.Fatal("valid plan no longer usable after rejection", valid)
			}
		})
	}
}

func TestAmbiguousCleanupPreviewMembersNeverPlan(t *testing.T) {
	for _, field := range []string{"task_id", "TASK_ID", `ta\u0073k_id`, "taſk_id"} {
		t.Run(field, func(t *testing.T) {
			s, provider, resource := cleanupPlanFixture(t)
			before := s.store.Revision()
			response := rawMemberRPC(t, s, `{"method":"cleanup-preview","`+field+`":1,"task_id":1}`)
			if response.OK || provider.previews.Load() != 0 || len(s.cleanupPlans) != 0 || s.store.Revision() != before {
				t.Fatal("ambiguous preview reached planner", response)
			}
			if _, err := os.Stat(resource); err != nil {
				t.Fatal(err)
			}
			valid := refreshRPC(t, s, protocol.Request{Method: "cleanup-preview", TaskID: 1})
			if !valid.OK || valid.CleanupPreview == nil || provider.previews.Load() != 1 {
				t.Fatal("valid preview rejected", valid)
			}
		})
	}
}

func TestAmbiguousMaintenanceMembersNeverCollectOrConsume(t *testing.T) {
	for _, mode := range []string{"method", "bundle", "mode", "mode_alias", "scope", "schema", "plan", "plan_escape"} {
		t.Run(mode, func(t *testing.T) {
			source := &refreshFixtureSource{name: "one"}
			s, store := refreshFixture(t, source)
			preview := mustRefreshPreview(t, s)
			raw, err := json.Marshal(refreshRequest("apply", preview.PlanID))
			if err != nil {
				t.Fatal(err)
			}
			frame := `{"method":"diagnostics-maintenance","maintenance":` + string(raw) + `}`
			original := frame
			switch mode {
			case "method":
				frame = strings.Replace(frame, `"method":`, `"method":"summary","method":`, 1)
			case "bundle":
				frame = strings.Replace(frame, `"maintenance":`, `"maintenance":{},"maintenance":`, 1)
			case "mode":
				frame = strings.Replace(frame, `"mode":`, `"mode":"preview","mode":`, 1)
			case "mode_alias":
				frame = strings.Replace(frame, `"mode":`, `"MODE":"preview","mode":`, 1)
			case "scope":
				frame = strings.Replace(frame, `"scope":`, `"scope":"workspace","scope":`, 1)
			case "schema":
				frame = strings.Replace(frame, `"diagnostics_schema":`, `"diagnostics_schema":0,"diagnostics_schema":`, 1)
			case "plan":
				frame = strings.Replace(frame, `"plan_id":`, `"plan_id":"foreign","plan_id":`, 1)
			case "plan_escape":
				frame = strings.Replace(frame, `"plan_id":`, `"plan_\u0069d":"foreign","plan_id":`, 1)
			}
			if frame == original {
				t.Fatal("fixture did not introduce duplicate members")
			}
			before := store.Revision()
			response := rawMemberRPC(t, s, frame)
			if response.OK || response.Maintenance != nil || source.calls.Load() != 0 || store.Revision() != before || s.refreshPlan.consumed {
				t.Fatal("ambiguous maintenance reached collection", response)
			}
			valid := refreshRPC(t, s, protocol.Request{Method: "diagnostics-maintenance", Maintenance: ptrRefresh(refreshRequest("apply", preview.PlanID))})
			if !valid.OK || valid.Maintenance == nil || source.calls.Load() != 1 {
				t.Fatal("valid refresh no longer usable", valid)
			}
		})
	}
}

func TestUniqueRequestMembersPreserveMapCaseAndSiblingKeys(t *testing.T) {
	s, provider, _ := cleanupPlanFixture(t)
	provider.targets[0].Operation["PRIVATE"] = "case-sensitive-provider-value"
	provider.targets = append(provider.targets, protocol.CleanupTarget{Source: "fixture", Kind: "thing", ResourceID: "two", Operation: map[string]string{"private": "second-object"}})
	preview := requireCleanupPlan(t, s)
	response := refreshRPC(t, s, protocol.Request{Method: "cleanup", Cleanup: &preview})
	if !response.OK || provider.calls.Load() != 2 || response.CleanupResult == nil || len(response.CleanupResult.Targets) != 2 {
		t.Fatal("distinct objects or case-sensitive map keys conflated", response)
	}
}

func TestRequestMemberTraversalBoundsAndWireSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		valid       bool
	}{
		{"ordinary", `{"method":"version"}`, true},
		{"single_case_alias", `{"METHOD":"version"}`, true},
		{"null_optional", `{"method":"version","maintenance":null}`, true},
		{"empty_collections", `{"method":"version","cleanup":{"targets":[]}}`, true},
		{"opaque_case_sensitive", `{"method":"version","unknown":{"Key":1,"key":2}}`, true},
		{"opaque_duplicate", `{"method":"version","unknown":{"key":1,"key":2}}`, false},
		{"opaque_escaped_duplicate", `{"method":"version","unknown":{"k\u0065y":1,"key":2}}`, false},
		{"opaque_siblings", `{"method":"version","unknown":[{"key":1},{"key":2}]}`, true},
		{"large_opaque_number", `{"method":"version","unknown":1e400}`, true},
		{"shallow_opaque", `{"method":"version","unknown":` + strings.Repeat("[", 60) + "0" + strings.Repeat("]", 60) + `}`, true},
		{"excessively_nested", `{"method":"version","unknown":` + strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65) + `}`, false},
		{"truncated", `{"method":`, false},
		{"trailing_document", `{"method":"version"}{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := uniqueRequestMembers([]byte(tc.frame)); got != tc.valid {
				t.Fatalf("unique=%v want=%v", got, tc.valid)
			}
		})
	}
}

func TestRejectedMemberFrameKeepsConnectionUsableAndErrorsPrivate(t *testing.T) {
	s, provider, _ := cleanupPlanFixture(t)
	daemon, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); s.handle(daemon) }()
	defer func() { client.Close(); <-done }()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(client)
	for i, frame := range []string{
		`{"method":"cleanup-preview","task_id":1,"TASK_ID":1,"private-token-marker":"private-value"}`,
		`{"method":"cleanup-preview","task_id":1}`,
	} {
		if _, err := io.WriteString(client, frame+"\n"); err != nil {
			t.Fatal(err)
		}
		var response protocol.Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if response.OK || provider.previews.Load() != 0 || strings.Contains(response.Error, "private") || response.Error != "ambiguous or excessively nested request members" {
				t.Fatal("ambiguous frame exposed input or planned", response)
			}
		} else if !response.OK || response.CleanupPreview == nil || provider.previews.Load() != 1 {
			t.Fatal("connection not usable after rejection", response)
		}
	}
}
