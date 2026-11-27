package agentservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestNativeRemoveObservationIndependentOfHandlerAndSharedByCopies(t *testing.T) {
	for _, kind := range []string{"schedules", "missing", "empty", "already_paused", "guard_error_after_effects", "cancel_after_effects", "expire_after_effects", "registration_reappears", "schedules_changed"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			switch kind {
			case "missing":
			case "empty":
				if err := atomicJSON(schedulePath(reg), scheduleState{Version: 1, Schedules: []calendarSchedule{}}); err != nil {
					t.Fatal(err)
				}
			default:
				writeRemovalSchedule(t, reg)
				if kind == "already_paused" {
					if err := pauseSchedules(reg); err != nil {
						t.Fatal(err)
					}
				}
			}
			expected, err := loadSchedules(reg)
			if err != nil {
				t.Fatal(err)
			}
			for i := range expected.Schedules {
				expected.Schedules[i].Enabled = false
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var plan RemovePlan
			guard := func(_ context.Context, execute func() error) error {
				err := execute()
				switch kind {
				case "guard_error_after_effects":
					return errors.New("private-lost-response")
				case "cancel_after_effects":
					cancel()
				case "expire_after_effects":
					plan.capture.expires = time.Now().Add(-time.Second)
				case "registration_reappears":
					if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
						t.Fatal(err)
					}
				case "schedules_changed":
					writeRemovalSchedule(t, reg)
				}
				return err
			}
			plan, err = m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, guard)
			if err != nil || plan.Observation() != (integration.AgentRemoveObservation{}) {
				t.Fatal("capture fabricated postconditions", err, plan.Observation())
			}
			copy := plan
			start := time.Now()
			err = plan.Execute(ctx)
			if kind == "guard_error_after_effects" || kind == "cancel_after_effects" || kind == "expire_after_effects" {
				requireRemovalRefusal(t, err)
			} else if err != nil {
				t.Fatal(err)
			}
			got := plan.Observation()
			if kind == "cancel_after_effects" || kind == "expire_after_effects" {
				if got != (integration.AgentRemoveObservation{}) {
					t.Fatal("cancelled request acquired detached observations", got)
				}
			} else {
				registration, schedules := integration.AgentRemoveRegistrationAbsent, integration.AgentRemoveSchedulesPaused
				if kind == "registration_reappears" {
					registration = integration.AgentRemoveRegistrationUnknown
				}
				if kind == "schedules_changed" {
					schedules = integration.AgentRemoveSchedulesUnknown
				}
				if got.Registration != registration || got.Schedules != schedules || got.ObservedAt.Before(start) || got.ObservedAt.After(time.Now()) {
					t.Fatal("missing/false independent native observation", got)
				}
			}
			if copy.Observation() != got {
				t.Fatal("copies lost shared observed result")
			}
			got.Registration, got.Schedules = integration.AgentRemoveRegistrationPresent, integration.AgentRemoveSchedulesUnchanged
			frozen := plan.Observation()
			requireRemovalRefusal(t, copy.Execute(context.Background()))
			cancel()
			if copy.Observation() != frozen {
				t.Fatal("replay/caller mutation/later cancellation changed immutable result")
			}
			data, err := json.Marshal(plan)
			if err != nil || string(data) != "{}" {
				t.Fatal("retained native evidence serialized", string(data), err)
			}
			data, err = json.Marshal(frozen)
			if err != nil || strings.Contains(string(data), "private") || strings.Contains(string(data), reg.ID) || strings.Contains(string(data), dir) {
				t.Fatal("private postcondition evidence leaked", string(data), err)
			}
			if kind != "schedules_changed" {
				after, err := loadSchedules(reg)
				if err != nil || !reflect.DeepEqual(after, expected) {
					t.Fatal("postcondition capture changed retained state", err)
				}
			}
			if kind == "missing" {
				if _, err := os.Lstat(schedulePath(reg)); !os.IsNotExist(err) {
					t.Fatal("empty/missing semantics created schedules", err)
				}
			}
		})
	}
}

func TestNativeRemoveObservationZeroRefusedAndConcurrent(t *testing.T) {
	if (RemovePlan{}).Observation() != (integration.AgentRemoveObservation{}) {
		t.Fatal("zero plan fabricated evidence")
	}
	for _, kind := range []string{"cancelled", "expired", "busy", "guard_denied", "guard_skipped", "registration_drift", "concurrent"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			entered, release := make(chan struct{}), make(chan struct{})
			guard := func(_ context.Context, execute func() error) error {
				if kind == "guard_denied" {
					return errors.New("private-denied")
				}
				if kind == "guard_skipped" {
					return nil
				}
				if kind == "concurrent" {
					close(entered)
					<-release
				}
				return execute()
			}
			plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, guard)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "cancelled":
				cancel()
			case "expired":
				plan.capture.expires = time.Now().Add(-time.Second)
			case "busy":
				m.mu.Lock()
				defer m.mu.Unlock()
			case "registration_drift":
				dir, _ := instanceDir(reg.ID)
				if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
					t.Fatal(err)
				}
			case "concurrent":
				done := make(chan error, 1)
				go func() { done <- plan.Execute(ctx) }()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("guard never entered")
				}
				var wg sync.WaitGroup
				for range 8 {
					wg.Go(func() {
						copy := plan
						if copy.Observation() != (integration.AgentRemoveObservation{}) || copy.Execute(ctx) == nil {
							t.Error("in-flight copy invented evidence/replayed")
						}
					})
				}
				wg.Wait()
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				want := plan.Observation()
				if want.Registration != integration.AgentRemoveRegistrationAbsent || want.Schedules != integration.AgentRemoveSchedulesPaused || want.ObservedAt.IsZero() {
					t.Fatal("first executor did not publish observed result", want)
				}
				// Destroy the path AFTER publication: inspection must perform no
				// new I/O or silently turn an old result into fresh evidence.
				dir, _ := instanceDir(reg.ID)
				if err := os.Rename(dir, dir+".retained"); err != nil {
					t.Fatal(err)
				}
				for range 8 {
					wg.Go(func() {
						if plan.Observation() != want {
							t.Error("memory-only shared result changed")
						}
					})
				}
				wg.Wait()
				return
			}
			requireRemovalRefusal(t, plan.Execute(ctx))
			requireRemovalRefusal(t, plan.Execute(context.Background()))
			if plan.Observation() != (integration.AgentRemoveObservation{}) {
				t.Fatal("pre-effect refusal fabricated observations", plan.Observation())
			}
		})
	}
}

func TestNativeRemoveObservationPartialExecutorDoesNotConflateError(t *testing.T) {
	for _, kind := range []string{"pause_error", "unlink_error", "cancel_after_pause", "cancel_after_unlink"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fault := errors.New("private-fault")
			// Exercise the real shared effect sequence with its existing fault
			// parameters; retained production plans have no replaceable executor.
			err = removeRegistration(ctx, reg, func(r Registration) error {
				if kind == "pause_error" {
					return fault
				}
				if err := pauseSchedules(r); err != nil {
					return err
				}
				if kind == "cancel_after_pause" {
					cancel()
				}
				return nil
			}, func(path string) error {
				if kind == "unlink_error" {
					return fault
				}
				if err := os.Remove(path); err != nil {
					return err
				}
				cancel()
				return nil
			})
			if err == nil {
				t.Fatal("fault/cancellation was hidden")
			}
			got := plan.capture.observeRemoval(ctx, diagnosticCheckpoint)
			if strings.HasPrefix(kind, "cancel_") {
				if got != (integration.AgentRemoveObservation{}) {
					t.Fatal("detached cancelled observation", got)
				}
			} else {
				want := integration.AgentRemoveSchedulesPaused
				if kind == "pause_error" {
					want = integration.AgentRemoveSchedulesUnchanged
				}
				if got.Registration != integration.AgentRemoveRegistrationPresent || got.Schedules != want || got.ObservedAt.IsZero() {
					t.Fatal("partial effect/error replaced independent observations", got)
				}
			}
			// A direct internal observation is not execution/consumption/result
			// publication and cannot manufacture admission or a transport result.
			if plan.Observation() != (integration.AgentRemoveObservation{}) {
				t.Fatal("read helper published an unexecuted plan")
			}
		})
	}
}

func TestNativeRemoveObservationRejectsUnsafeOrChangedEvidence(t *testing.T) {
	for _, kind := range []string{"registration_fifo", "registration_symlink", "registration_oversized", "registration_replaced", "schedule_fifo", "schedule_symlink", "schedule_oversized", "schedule_malformed", "schedule_missing", "root_replaced", "instance_replaced", "root_symlink", "instance_symlink", "instance_missing", "empty_replaced", "missing_created"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			if kind == "empty_replaced" {
				if err := atomicJSON(schedulePath(reg), scheduleState{Version: 1}); err != nil {
					t.Fatal(err)
				}
			} else if kind != "missing_created" {
				writeRemovalSchedule(t, reg)
			}
			plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
			if err != nil {
				t.Fatal(err)
			}
			// No private snapshot/store/token is read for outcome observation.
			for _, name := range []string{"snapshot.json", "store.sqlite", "ingress.token", "ownership.json"} {
				if err := syscall.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := removeRegistration(context.Background(), reg, pauseSchedules, os.Remove); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "root_") || strings.HasPrefix(kind, "instance_") {
				path := dir
				if strings.HasPrefix(kind, "root_") {
					path = filepath.Dir(dir)
				}
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(kind, "symlink") {
					err = os.Symlink(path+".retained", path)
				} else if kind != "instance_missing" {
					err = os.Mkdir(path, 0700)
					if err == nil && kind == "root_replaced" {
						err = os.Mkdir(dir, 0700)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			} else {
				path := schedulePath(reg)
				if strings.HasPrefix(kind, "registration_") {
					path = filepath.Join(dir, "registration.json")
				}
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				switch {
				case strings.HasSuffix(kind, "fifo"):
					err = syscall.Mkfifo(path, 0600)
				case strings.HasSuffix(kind, "symlink"):
					err = os.Symlink(path+".missing", path)
				case strings.HasSuffix(kind, "oversized"):
					err = os.WriteFile(path, []byte(strings.Repeat(" ", protocol.AgentMaxFrameBytes+1)), 0600)
				case kind == "schedule_malformed":
					err = os.WriteFile(path, []byte(`{"version":999}`), 0600)
				case kind == "registration_replaced":
					err = atomicJSON(path, reg)
				case kind == "empty_replaced" || kind == "missing_created":
					err = atomicJSON(path, scheduleState{Version: 1})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			got := plan.capture.observeRemoval(context.Background(), diagnosticCheckpoint)
			if time.Since(start) > time.Second {
				t.Fatal("unsafe observation blocked on a checkpoint")
			}
			switch {
			case strings.HasPrefix(kind, "root_") || strings.HasPrefix(kind, "instance_"):
				if got != (integration.AgentRemoveObservation{}) {
					t.Fatal("replaced/missing original directory proved postconditions", got)
				}
			case strings.HasPrefix(kind, "registration_"):
				if got.Registration != integration.AgentRemoveRegistrationUnknown || got.Schedules != integration.AgentRemoveSchedulesPaused {
					t.Fatal("unsafe/replacement registration inferred absent/original", got)
				}
			default:
				if got.Registration != integration.AgentRemoveRegistrationAbsent || got.Schedules != integration.AgentRemoveSchedulesUnknown {
					t.Fatal("unsafe/drifted schedules inferred paused/preserved", got)
				}
			}
		})
	}
}

func TestNativeRemoveObservationBindsOriginalDirectoriesAcrossReads(t *testing.T) {
	for _, kind := range []string{"root", "instance", "cancel_first", "cancel_second", "deadline", "expiry", "unrelated_directory_write"} {
		for _, boundary := range []int{1, 2} {
			t.Run(kind+string(rune('0'+boundary)), func(t *testing.T) {
				reg, m := removePlanFixture(t)
				writeRemovalSchedule(t, reg)
				plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
				if err != nil {
					t.Fatal(err)
				}
				if err := removeRegistration(context.Background(), reg, pauseSchedules, os.Remove); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if kind == "deadline" {
					var stop context.CancelFunc
					ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer stop()
				}
				calls := 0
				read := func(path string) ([]byte, os.FileInfo, error) {
					calls++
					data, info, err := diagnosticCheckpoint(path)
					if calls != boundary {
						return data, info, err
					}
					dir, _ := instanceDir(reg.ID)
					switch kind {
					case "root", "instance":
						path := dir
						if kind == "root" {
							path = filepath.Dir(dir)
						}
						if err := os.Rename(path, path+".retained"); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
					case "cancel_first", "cancel_second":
						cancel()
					case "expiry":
						plan.capture.expires = time.Now().Add(-time.Second)
					case "unrelated_directory_write":
						if err := os.WriteFile(filepath.Join(dir, "unrelated"), []byte("untouched"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return data, info, err
				}
				got := plan.capture.observeRemoval(ctx, read)
				if kind == "unrelated_directory_write" {
					if got.Registration != integration.AgentRemoveRegistrationAbsent || got.Schedules != integration.AgentRemoveSchedulesPaused {
						t.Fatal("directory mtime was confused with identity", got)
					}
				} else if got != (integration.AgentRemoveObservation{}) {
					t.Fatal("in-flight drift/cancellation retained false evidence", got)
				}
				if kind == "deadline" && calls != 0 {
					t.Fatal("expired context read private checkpoints")
				}
			})
		}
	}
}

func TestNativeRemoveObservationFingerprintsCompleteDecodedState(t *testing.T) {
	for _, field := range []string{"id", "name", "payload", "pending_payload", "pending_catchup", "pending_time", "eligible", "admission", "missed", "problem", "time", "timezone", "weekdays", "enabled", "version"} {
		t.Run(field, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
			if err != nil {
				t.Fatal(err)
			}
			if err := removeRegistration(context.Background(), reg, pauseSchedules, os.Remove); err != nil {
				t.Fatal(err)
			}
			state, err := loadSchedules(reg)
			if err != nil {
				t.Fatal(err)
			}
			s := &state.Schedules[0]
			switch field {
			case "id":
				s.ID += "-other"
				s.Pending.Event.ID = scheduleEventID(s.ID, s.Pending.ScheduledAt)
			case "name":
				s.Name += "-other"
			case "payload":
				s.Payload = json.RawMessage(`{"private":"changed"}`)
			case "pending_payload":
				s.Pending.Event.Payload = json.RawMessage(`{"private":"changed"}`)
			case "pending_catchup":
				s.Pending.CatchUp = !s.Pending.CatchUp
			case "pending_time":
				s.Pending.ScheduledAt = s.Pending.ScheduledAt.Add(time.Hour)
				s.Pending.Event.ID = scheduleEventID(s.ID, s.Pending.ScheduledAt)
			case "eligible":
				s.EligibleAfter = s.EligibleAfter.Add(time.Hour)
			case "admission":
				due := s.Pending.ScheduledAt
				s.LastAdmission = &protocol.AgentScheduleAdmission{TaskID: 123, EventID: scheduleEventID(s.ID, due), ScheduledAt: due, AcceptedAt: due.Add(time.Minute)}
			case "missed":
				s.LastMissedAt = s.Pending.ScheduledAt
			case "problem":
				s.Problem = "private-problem"
			case "time":
				s.Time = "11:00"
			case "timezone":
				s.Timezone = "Europe/Vienna"
			case "weekdays":
				s.Weekdays = []string{"sun"}
			case "enabled":
				s.Enabled = true
			case "version":
				state.Version++
			}
			if err := atomicJSON(schedulePath(reg), state); err != nil {
				t.Fatal(err)
			}
			got := plan.capture.observeRemoval(context.Background(), diagnosticCheckpoint)
			if got.Schedules != integration.AgentRemoveSchedulesUnknown || got.Registration != integration.AgentRemoveRegistrationAbsent || got.ObservedAt.IsZero() {
				t.Fatal("counts/disabled flags substituted for complete native state", got)
			}
		})
	}
}

func TestNativeRemoveObservationRechecksLeavesAfterBothReads(t *testing.T) {
	for _, kind := range []string{"registration_reappears", "schedule_replaced", "schedule_removed"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
			if err != nil {
				t.Fatal(err)
			}
			if err := removeRegistration(context.Background(), reg, pauseSchedules, os.Remove); err != nil {
				t.Fatal(err)
			}
			calls := 0
			read := func(path string) ([]byte, os.FileInfo, error) {
				calls++
				data, info, err := diagnosticCheckpoint(path)
				if calls == 2 {
					switch kind {
					case "registration_reappears":
						dir, _ := instanceDir(reg.ID)
						if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
							t.Fatal(err)
						}
					case "schedule_replaced":
						if err := pauseSchedules(reg); err != nil {
							t.Fatal(err)
						}
					case "schedule_removed":
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					}
				}
				return data, info, err
			}
			got := plan.capture.observeRemoval(context.Background(), read)
			registration, schedules := integration.AgentRemoveRegistrationAbsent, integration.AgentRemoveSchedulesUnknown
			if kind == "registration_reappears" {
				registration, schedules = integration.AgentRemoveRegistrationUnknown, integration.AgentRemoveSchedulesPaused
			}
			if got.Registration != registration || got.Schedules != schedules || got.ObservedAt.IsZero() {
				t.Fatal("cross-checkpoint leaf drift retained false postconditions", got)
			}
		})
	}
}
