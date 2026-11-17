package agentservice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestDiagnosticSupervisionCopiesOnlySelectedNativeMemoryWithoutIO(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "must-not-be-created"))
	// A nil workspace dependency would panic if any status/probe/recovery ran.
	manager := New(nil)
	next := time.Date(2026, 11, 17, 13, 0, 0, 123, time.FixedZone("private-zone", 3600))
	manager.retry["crashing"] = retryState{attempts: 3, next: next, problem: "private-start-error-token-path"}
	manager.retry["unrelated"] = retryState{attempts: 99, next: next, problem: "private-other"}
	manager.scheduleErrors["crashing"] = "private-schedule-error-payload"
	manager.scheduleErrors["stopped"] = "private-delivery"
	beforeRetry, beforeErrors := map[string]retryState{}, map[string]string{}
	for k, v := range manager.retry {
		beforeRetry[k] = v
	}
	for k, v := range manager.scheduleErrors {
		beforeErrors[k] = v
	}
	got := manager.DiagnosticAgentSupervision([]string{"crashing", "stopped", "empty"})
	if len(got) != 3 || got["crashing"].RetryRecordEvidence != "current_record" || got["crashing"].Retry == nil || got["crashing"].Retry.FailedStartAttempts != 3 || got["crashing"].Retry.RetryNotBefore != next.UTC().Format(time.RFC3339Nano) || !got["crashing"].Retry.StartProblem || !got["crashing"].ScheduleDeliveryProblem {
		t.Fatalf("native evidence lost: %+v", got)
	}
	for _, id := range []string{"stopped", "empty"} {
		if got[id].RetryRecordEvidence != "no_current_record" || got[id].Retry != nil {
			t.Fatalf("fabricated retry for %s", id)
		}
	}
	if !got["stopped"].ScheduleDeliveryProblem || got["empty"].ScheduleDeliveryProblem {
		t.Fatal("schedule error presence lost")
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "unrelated") {
		t.Fatalf("private/unselected memory exposed: %s", raw)
	}
	got["crashing"].Retry.FailedStartAttempts = 999
	if !reflect.DeepEqual(manager.retry, beforeRetry) || !reflect.DeepEqual(manager.scheduleErrors, beforeErrors) {
		t.Fatal("inspection mutated or aliased native state")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("inspection created/read filesystem state")
	}
	manager.retry["crashing"] = retryState{attempts: 4, next: next.Add(time.Second)}
	delete(manager.scheduleErrors, "crashing")
	view := manager.DiagnosticAgentSupervision([]string{"crashing"})["crashing"]
	if view.Retry.FailedStartAttempts != 4 || view.Retry.StartProblem || view.ScheduleDeliveryProblem {
		t.Fatal("fresh native memory changes hidden")
	}
	delete(manager.retry, "crashing")
	if view := manager.DiagnosticAgentSupervision([]string{"crashing"})["crashing"]; view.RetryRecordEvidence != "no_current_record" || view.Retry != nil {
		t.Fatal("cleared retry record retained")
	}
	if view := New(nil).DiagnosticAgentSupervision([]string{"crashing"})["crashing"]; view.RetryRecordEvidence != "no_current_record" || view.Retry != nil || view.ScheduleDeliveryProblem {
		t.Fatal("new daemon reconstructed historical evidence")
	}
}

func TestDiagnosticSupervisionBusyIsImmediateUnknownAndInvalidRecordsUnavailable(t *testing.T) {
	manager := New(nil)
	manager.mu.Lock()
	done := make(chan map[string]protocol.DiagnosticAgentSupervision, 1)
	go func() { done <- manager.DiagnosticAgentSupervision([]string{"worker"}) }()
	select {
	case got := <-done:
		if got != nil {
			t.Error("busy manager fabricated evidence")
		}
	case <-time.After(time.Second):
		manager.mu.Unlock()
		<-done
		t.Fatal("inspection waited on supervisor I/O mutex")
	}
	manager.mu.Unlock()
	for _, retry := range []retryState{{attempts: 0, next: time.Now()}, {attempts: -1, next: time.Now()}, {attempts: 1}, {attempts: 1 << 53, next: time.Now()}, {attempts: 1, next: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}} {
		manager.retry["worker"] = retry
		if _, ok := manager.DiagnosticAgentSupervision([]string{"worker"})["worker"]; ok {
			t.Fatal("invalid native record treated as available")
		}
	}
}
