package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestDiagnosticAgentRemoveObservationProjectionRejectsProviderPayloads(t *testing.T) {
	started, expires := time.Now().Add(-time.Second), time.Now().Add(29*time.Second)
	valid := integration.AgentRemoveObservation{ObservedAt: time.Now(), Registration: integration.AgentRemoveRegistrationAbsent, Schedules: integration.AgentRemoveSchedulesPaused}
	for _, field := range []string{"zero", "private_registration", "private_schedule", "stale", "future", "expired", "year"} {
		t.Run(field, func(t *testing.T) {
			o := valid
			switch field {
			case "zero":
				o = integration.AgentRemoveObservation{}
			case "private_registration":
				o.Registration = "PRIVATE registration"
			case "private_schedule":
				o.Schedules = "PRIVATE payload"
			case "stale":
				o.ObservedAt = started.Add(-time.Nanosecond)
			case "future":
				o.ObservedAt = time.Now().Add(time.Second)
			case "expired":
				o.ObservedAt = expires.Add(time.Nanosecond)
			case "year":
				o.ObservedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			got := projectAgentRemoveObservation(o, started, expires)
			if got != protocol.UnknownDiagnosticAgentRemoveObservation() {
				t.Fatal("invalid native evidence did not become fixed unknown", got)
			}
		})
	}
	for _, r := range []integration.AgentRemoveRegistrationState{integration.AgentRemoveRegistrationUnknown, integration.AgentRemoveRegistrationPresent, integration.AgentRemoveRegistrationAbsent} {
		for _, s := range []integration.AgentRemoveScheduleState{integration.AgentRemoveSchedulesUnknown, integration.AgentRemoveSchedulesPaused, integration.AgentRemoveSchedulesUnchanged} {
			o := valid
			o.Registration, o.Schedules = r, s
			got := projectAgentRemoveObservation(o, started, expires)
			if got.Evidence != "bounded_native_checkpoint_reads" || got.ObservedAt == "" {
				t.Fatal("finite native evidence lost", got)
			}
		}
	}
}

func TestDiagnosticAgentRemoveObservedCheckpointsSeparateFromHandler(t *testing.T) {
	for _, kind := range []string{"success", "handler_error", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			s, _, provider, q, _, _ := removeAdapterFixture(t, "origin")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider.wrap = func(p integration.AgentRemovePlan) integration.AgentRemovePlan {
				w := removeAdapterWrappedPlan{AgentRemovePlan: p, expires: p.ExpiresAt(), bytes: p.RetainedBytes(), fail: kind == "handler_error"}
				if kind == "cancelled" {
					w.after = cancel
				}
				return w
			}
			preview, err := s.diagnosticAgentRemove(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			q.Mode, q.PlanID = "apply", preview.PlanID
			v, err := s.diagnosticAgentRemove(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(v)
			var frame map[string]any
			_ = json.Unmarshal(data, &frame)
			observation, ok := frame["observation"].(map[string]any)
			if !ok {
				t.Fatal("independent native checkpoint observations missing", string(data))
			}
			if kind == "cancelled" {
				if observation["evidence"] != "unknown_not_observed" || observation["registration"] != "unknown" || observation["schedules"] != "unknown" {
					t.Fatal("cancelled native reply became detached success", observation)
				}
			} else if observation["evidence"] != "bounded_native_checkpoint_reads" || observation["registration"] != "absent" || observation["schedules"] != "paused_or_empty_preserved" || observation["observed_at"] == nil {
				t.Fatal("handler status replaced native checkpoint evidence", observation)
			}
			if kind == "success" && v.Outcome != "native_handler_returned_unverified" || kind != "success" && v.Outcome != "unverified_may_be_partial" || strings.Contains(string(data), "PRIVATE") {
				t.Fatal("postcondition projection rewrote handler evidence/leaked private data", v)
			}
		})
	}
}
