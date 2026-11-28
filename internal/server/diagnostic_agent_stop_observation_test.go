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

func TestDiagnosticAgentStopObservationProjectionRejectsProviderPayloads(t *testing.T) {
	started, expires := time.Now().Add(-time.Second), time.Now().Add(29*time.Second)
	valid := integration.AgentStopObservation{ObservedAt: time.Now(), Registration: integration.AgentStopRegistrationStopped, Runner: integration.AgentStopRunnerAbsent}
	for _, field := range []string{"zero", "private_registration", "private_runner", "stale", "future", "expired", "year"} {
		t.Run(field, func(t *testing.T) {
			o := valid
			switch field {
			case "zero":
				o = integration.AgentStopObservation{}
			case "private_registration":
				o.Registration = "PRIVATE registration"
			case "private_runner":
				o.Runner = "PRIVATE payload"
			case "stale":
				o.ObservedAt = started.Add(-time.Nanosecond)
			case "future":
				o.ObservedAt = time.Now().Add(time.Second)
			case "expired":
				o.ObservedAt = expires.Add(time.Nanosecond)
			case "year":
				o.ObservedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			if got := projectAgentStopObservation(o, started, expires); got != protocol.UnknownDiagnosticAgentStopObservation() {
				t.Fatal("invalid evidence escaped", got)
			}
		})
	}
	for _, r := range []integration.AgentStopRegistrationState{integration.AgentStopRegistrationUnknown, integration.AgentStopRegistrationStopped, integration.AgentStopRegistrationOriginal} {
		for _, s := range []integration.AgentStopRunnerState{integration.AgentStopRunnerUnknown, integration.AgentStopRunnerPresent, integration.AgentStopRunnerAbsent} {
			o := valid
			o.Registration, o.Runner = r, s
			got := projectAgentStopObservation(o, started, expires)
			if got.Evidence != "bounded_native_stop_reads" || got.ObservedAt == "" {
				t.Fatal("finite native observation lost", got)
			}
		}
	}
}

type stopObservationWrappedPlan struct {
	integration.AgentStopPlan
	inspect func(integration.AgentStopObservation) integration.AgentStopObservation
}

func (p stopObservationWrappedPlan) Observation() integration.AgentStopObservation {
	return p.inspect(p.AgentStopPlan.Observation())
}

func TestDiagnosticAgentStopObservedStateSeparateFromHandler(t *testing.T) {
	for _, kind := range []string{"success", "error_after_execute", "cancelled_after_execute", "cancelled_during_inspection", "private_provider", "stale_provider", "unknown_provider"} {
		t.Run(kind, func(t *testing.T) {
			s, _, provider, q, file, pending := stopAdapterFixture(t, "origin")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inspections := 0
			provider.wrap = func(p integration.AgentStopPlan) integration.AgentStopPlan {
				w := stopAdapterWrappedPlan{AgentStopPlan: p, expires: p.ExpiresAt(), bytes: p.RetainedBytes(), fail: kind == "error_after_execute"}
				if kind == "cancelled_after_execute" {
					w.after = cancel
				}
				return stopObservationWrappedPlan{AgentStopPlan: w, inspect: func(o integration.AgentStopObservation) integration.AgentStopObservation {
					inspections++
					switch kind {
					case "cancelled_during_inspection":
						cancel()
					case "private_provider":
						o.Runner = "PRIVATE payload"
					case "stale_provider":
						o.ObservedAt = time.Now().Add(-time.Minute)
					case "unknown_provider":
						return integration.AgentStopObservation{}
					}
					return o
				}}
			}
			preview, err := s.diagnosticAgentStop(context.Background(), q)
			if err != nil || preview.Observation != protocol.UnknownDiagnosticAgentStopObservation() || inspections != 0 {
				t.Fatal("preview fabricated/read outcome", preview, err)
			}
			q.Mode, q.PlanID = "apply", preview.PlanID
			v, err := s.diagnosticAgentStop(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(v)
			if strings.Contains(string(raw), "PRIVATE") || stopDesired(t, file) || !*pending {
				t.Fatal("private leak or existing stop behavior changed", v)
			}
			unknown := kind != "success" && kind != "error_after_execute"
			if unknown {
				if v.Observation != protocol.UnknownDiagnosticAgentStopObservation() {
					t.Fatal("unsafe/cancelled observation published", v)
				}
			} else if v.Observation.Evidence != "bounded_native_stop_reads" || v.Observation.Registration != "matching_stopped_definition" || v.Observation.Runner != "no_listener_and_no_live_observed_owner" || v.Observation.ObservedAt == "" {
				t.Fatal("real independent native observation lost", v)
			}
			partial := kind == "error_after_execute" || kind == "cancelled_after_execute" || kind == "cancelled_during_inspection"
			if (v.Outcome == "unverified_may_be_partial") != partial {
				t.Fatal("observation rewrote handler evidence", v)
			}
			if strings.HasPrefix(kind, "cancelled") && v.Problem != "cancelled_or_timed_out" {
				t.Fatal("late cancellation certified handler success", v)
			}
			wantInspections := 1
			if kind == "cancelled_after_execute" {
				wantInspections = 0
			}
			if inspections != wantInspections {
				t.Fatal("unbounded/cancelled inspection", inspections)
			}
			if _, err := s.diagnosticAgentStop(context.Background(), q); err == nil || inspections != wantInspections || provider.prepares != 1 {
				t.Fatal("observation restored admission or replanned")
			}
		})
	}
}

// An inherited expired absolute deadline can precede Err/timer notification.
type stopObservationExpiredContext struct{ context.Context }

func (stopObservationExpiredContext) Deadline() (time.Time, bool) {
	return time.Now().Add(-time.Second), true
}
func TestDiagnosticAgentStopObservationAbsoluteDeadline(t *testing.T) {
	if stopObservationInputsLive(stopObservationExpiredContext{context.Background()}, time.Now().Add(time.Second)) {
		t.Fatal("expired caller admitted before timer")
	}
	if stopObservationInputsLive(context.Background(), time.Now().Add(-time.Nanosecond)) {
		t.Fatal("expired native capture admitted")
	}
	if !stopObservationInputsLive(context.Background(), time.Now().Add(time.Second)) {
		t.Fatal("live evidence refused")
	}
}
