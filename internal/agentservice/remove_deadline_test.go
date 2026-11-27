package agentservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"radar/internal/integration"
)

func TestNativeRemovalAbsoluteDeadlineAtEveryEffectBoundary(t *testing.T) {
	for _, kind := range []string{"before_pause", "after_pause", "during_unlink", "pause_error", "unlink_error", "allowed"} {
		t.Run(kind, func(t *testing.T) {
			reg, _ := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			expected, err := loadSchedules(reg)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(20 * time.Millisecond)
			if kind == "before_pause" {
				deadline = time.Now().Add(-time.Second)
			} else if kind == "allowed" {
				deadline = time.Now().Add(time.Hour)
			}
			ctx := delayedDeadlineContext{Context: context.Background(), deadline: deadline}
			expire := func() { time.Sleep(max(time.Until(deadline), 0) + time.Millisecond) }
			fault := errors.New("private-fixture-fault")
			var calls []string
			err = removeRegistration(ctx, reg, func(r Registration) error {
				calls = append(calls, "pause")
				if kind == "pause_error" {
					expire()
					return fault
				}
				if err := pauseSchedules(r); err != nil {
					return err
				}
				if kind == "after_pause" {
					expire()
				}
				return nil
			}, func(path string) error {
				calls = append(calls, "unlink")
				if kind == "unlink_error" {
					expire()
					return fault
				}
				if err := os.Remove(path); err != nil {
					return err
				}
				if kind == "during_unlink" {
					expire()
				}
				return nil
			})
			wantCalls, wantErr := []string{"pause", "unlink"}, error(context.DeadlineExceeded)
			paused, removed := true, true
			switch kind {
			case "before_pause":
				wantCalls, paused, removed = nil, false, false
			case "after_pause":
				wantCalls, removed = []string{"pause"}, false
			case "pause_error":
				wantCalls, wantErr, paused, removed = []string{"pause"}, fault, false, false
			case "unlink_error":
				wantErr, removed = fault, false
			case "allowed":
				wantErr = nil
			}
			if !errors.Is(err, wantErr) || !reflect.DeepEqual(calls, wantCalls) || ctx.Err() != nil {
				t.Fatal("absolute expiry ignored before timer notification", err, calls, wantErr, wantCalls)
			}
			if paused {
				expected.Schedules[0].Enabled = false
			}
			actual, err := loadSchedules(reg)
			if err != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatal("expiry rolled back or changed schedule payloads", err)
			}
			dir, _ := instanceDir(reg.ID)
			_, err = os.Lstat(filepath.Join(dir, "registration.json"))
			if removed != os.IsNotExist(err) || !removed && CheckWorkspaceRemoval(reg.Workspace.Path) == nil {
				t.Fatal("registration effect or retained ownership disagrees with boundary", err)
			}
		})
	}
}

func TestNativeRemovePlanExecutionKeepsEarliestDeadline(t *testing.T) {
	for _, kind := range []string{"original_expiry", "earlier_caller", "native_cap"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			ctx := context.Background()
			want := time.Now().Add(time.Second)
			if kind == "earlier_caller" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, want)
				defer cancel()
			}
			var deadline time.Time
			plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, func(c context.Context, execute func() error) error {
				var ok bool
				deadline, ok = c.Deadline()
				if !ok {
					t.Error("native execution has no deadline")
				}
				return execute()
			})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "original_expiry" {
				plan.capture.expires = want
			}
			start := time.Now()
			if err := plan.Execute(ctx); err != nil {
				t.Fatal(err)
			}
			if kind == "native_cap" {
				if deadline.Before(start.Add(4*time.Second)) || deadline.After(time.Now().Add(5*time.Second)) {
					t.Fatal("existing five-second execution cap changed", deadline)
				}
			} else if !deadline.Equal(want) {
				t.Fatal("execution extended original/caller expiry", deadline, want)
			}
		})
	}
}

func TestNativeRemovePlanLateAbsoluteExpiryWithholdsObservation(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "expired_after_effects"}[expired], func(t *testing.T) {
			reg, m := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			deadline := time.Now().Add(time.Hour)
			plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, func(_ context.Context, execute func() error) error {
				err := execute()
				if expired {
					time.Sleep(max(time.Until(deadline), 0) + time.Millisecond)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if expired {
				deadline = time.Now().Add(20 * time.Millisecond)
			}
			ctx := delayedDeadlineContext{Context: context.Background(), deadline: deadline}
			err = plan.Execute(ctx)
			if expired {
				requireRemovalRefusal(t, err)
				if plan.Observation() != (integration.AgentRemoveObservation{}) {
					t.Fatal("expired caller retained usable postconditions", plan.Observation())
				}
			} else if err != nil || plan.Observation().Registration != integration.AgentRemoveRegistrationAbsent {
				t.Fatal("own deferred cancellation discarded healthy observations", err, plan.Observation())
			}
			if ctx.Err() != nil {
				t.Fatal("fixture did not isolate absolute deadline")
			}
			requireRemovalRefusal(t, plan.Execute(context.Background()))
			if _, err := load(reg.ID); !os.IsNotExist(err) || savedSchedule(t, reg).Enabled {
				t.Fatal("expiry restored registration, schedules or admission", err)
			}
		})
	}
}

func TestNativeRemoveObservationAbsoluteExpiryStopsReads(t *testing.T) {
	for _, boundary := range []int{0, 1, 2, -1} {
		t.Run(map[int]string{0: "at_entry", 1: "after_registration", 2: "after_schedules", -1: "allowed"}[boundary], func(t *testing.T) {
			reg, m := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(20 * time.Millisecond)
			if boundary == 0 {
				deadline = time.Now().Add(-time.Second)
			} else if boundary == -1 {
				deadline = time.Now().Add(time.Hour)
			}
			ctx := delayedDeadlineContext{Context: context.Background(), deadline: deadline}
			reads := 0
			got := plan.capture.observeRemoval(ctx, func(path string) ([]byte, os.FileInfo, error) {
				reads++
				data, info, err := diagnosticCheckpoint(path)
				if reads == boundary {
					time.Sleep(max(time.Until(deadline), 0) + time.Millisecond)
				}
				return data, info, err
			})
			if boundary == -1 {
				if reads != 2 || got.Registration != integration.AgentRemoveRegistrationPresent || got.Schedules != integration.AgentRemoveSchedulesUnchanged || got.ObservedAt.IsZero() {
					t.Fatal("healthy read lost independent evidence", reads, got)
				}
			} else if reads != boundary || got != (integration.AgentRemoveObservation{}) {
				t.Fatal("expired observation kept evidence or continued checkpoint reads", reads, got)
			}
			if ctx.Err() != nil || savedSchedule(t, reg).Enabled != true {
				t.Fatal("observation mutated native state or used a notified timer")
			}
		})
	}
}
