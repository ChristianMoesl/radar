package agentservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func removePlanFixture(t *testing.T) (Registration, *Manager) {
	t.Helper()
	reg := probeFixture(t)
	prepared := 0
	return reg, nativeStatusManager(reg, &prepared)
}

func writeRemovalSchedule(t *testing.T, reg Registration) {
	t.Helper()
	s := calendarSchedule{AgentScheduleConfig: workdaySchedule(), ID: "retained-schedule", Enabled: true, EligibleAfter: scheduleTime("2026-10-26T08:00:00Z")}
	due := scheduleTime("2026-10-26T09:00:00Z")
	s.Pending = &schedulePending{Event: scheduledEvent{Source: "radar.schedule", ID: scheduleEventID(s.ID, due), Payload: json.RawMessage(`{"private":"payload"}`)}, ScheduledAt: due}
	if err := atomicJSON(schedulePath(reg), scheduleState{Version: 1, Schedules: []calendarSchedule{s}}); err != nil {
		t.Fatal(err)
	}
}

func requireRemovalRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil || err.Error() != removePlanUnavailable().Error() {
		t.Fatal("accepted or leaked private refusal", err)
	}
}

func TestNativeRemovePlanEffectFreePrivateCaptureAndRetainedData(t *testing.T) {
	reg, m := removePlanFixture(t)
	dir, _ := instanceDir(reg.ID)
	writeRemovalSchedule(t, reg)
	for _, name := range []string{"snapshot.json", "ingress.token", "collection.json", "config.json", "ownership.json", "store.sqlite"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(schedulePath(reg))
	expected, err := loadSchedules(reg)
	if err != nil {
		t.Fatal(err)
	}
	expected.Schedules[0].Enabled = false
	calls := 0
	guard := func(_ context.Context, execute func() error) error {
		calls++
		if _, err := load(reg.ID); err != nil || !savedSchedule(t, reg).Enabled {
			t.Error("guard entered after first effect", err)
		}
		err := execute()
		if _, statErr := os.Lstat(filepath.Join(dir, "registration.json")); !os.IsNotExist(statErr) || savedSchedule(t, reg).Enabled {
			t.Error("guard released before BOTH effects", statErr)
		}
		return err
	}
	start := time.Now()
	plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, guard)
	if err != nil || calls != 0 || !plan.ExpiresAt().After(start) || plan.ExpiresAt().After(time.Now().Add(30*time.Second)) || plan.RetainedBytes() < 64*1024 || plan.RetainedBytes() > 128*1024 {
		t.Fatal("invalid capture/retention", err)
	}
	if plan.Preview() != (integration.AgentRemovePreview{ID: reg.ID, Schedules: 1, EnabledSchedules: 1}) {
		t.Fatal("preview lost exact native capture facts", plan.Preview())
	}
	mutable := plan.Preview()
	mutable.ID, mutable.Schedules, mutable.EnabledSchedules = "other", 99, 0
	if plan.Preview() == mutable {
		t.Fatal("caller changed retained preview")
	}
	encoded, err := json.Marshal(plan)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("executable evidence serialized", string(encoded), err)
	}
	after, _ := os.ReadFile(schedulePath(reg))
	if !bytes.Equal(before, after) || CheckWorkspaceRemoval(reg.Workspace.Path) == nil {
		t.Fatal("capture changed schedules/released ownership")
	}
	copy := plan
	if err := plan.Execute(context.Background()); err != nil || calls != 1 {
		t.Fatal("native removal failed", err)
	}
	requireRemovalRefusal(t, copy.Execute(context.Background()))
	state, err := loadSchedules(reg)
	if err != nil || !reflect.DeepEqual(state, expected) {
		t.Fatal("retained schedule identity/pending evidence changed", state, err)
	}
	if err := CheckWorkspaceRemoval(reg.Workspace.Path); err != nil {
		t.Fatal("removed registration still counted as owner", err)
	}
	for _, name := range []string{"snapshot.json", "ingress.token", "collection.json", "config.json", "ownership.json", "store.sqlite"} {
		if info, err := os.Lstat(filepath.Join(dir, name)); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			t.Fatal("removal opened/deleted unrelated private data", name, err)
		}
	}
}

func TestNativeRemovePlanRefusesUnsafeCapture(t *testing.T) {
	for _, kind := range []string{"id", "binding", "noncanonical", "nil_guard", "busy", "cancelled", "desired", "live_owner", "invalid_owner", "owner_fifo", "owner_symlink", "owner_oversized", "registration_missing", "registration_fifo", "registration_symlink", "registration_oversized", "registration_duplicate", "root_symlink", "instance_symlink", "socket_symlink", "running", "unknown_hello", "unknown_snapshot", "schedule_fifo", "schedule_symlink", "schedule_oversized", "schedule_invalid", "workspace_unavailable"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			writeRemovalSchedule(t, reg)
			id, binding := reg.ID, reg.Workspace
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			guard := integration.AgentRemoveGuard(allowStopGuard)
			methods := func() []string { return nil }
			write := func(name, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "id":
				id = "../worker"
			case "binding":
				binding.ID += "-other"
			case "noncanonical":
				binding.Path += "/../workspace"
			case "nil_guard":
				guard = nil
			case "busy":
				m.mu.Lock()
				defer m.mu.Unlock()
			case "cancelled":
				cancel()
			case "desired":
				reg.Desired = true
				if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
					t.Fatal(err)
				}
			case "live_owner":
				write("pid", strconv.Itoa(os.Getpid()))
			case "invalid_owner":
				write("pid", "private-owner")
			case "owner_oversized":
				write("pid", strings.Repeat("1", 65))
			case "registration_oversized":
				write("registration.json", strings.Repeat(" ", protocol.AgentMaxFrameBytes+1))
			case "registration_duplicate":
				write("registration.json", `{"id":"worker","id":"worker"}`)
			case "schedule_oversized":
				write("schedules.json", strings.Repeat(" ", protocol.AgentMaxFrameBytes+1))
			case "schedule_invalid":
				write("schedules.json", `{"version":999,"schedules":[]}`)
			case "registration_missing":
				if err := os.Remove(filepath.Join(dir, "registration.json")); err != nil {
					t.Fatal(err)
				}
			case "registration_fifo", "registration_symlink", "schedule_fifo", "schedule_symlink", "owner_fifo", "owner_symlink":
				name := "registration.json"
				if strings.HasPrefix(kind, "schedule_") {
					name = "schedules.json"
				}
				if strings.HasPrefix(kind, "owner_") {
					name = "pid"
					write(name, "987654")
				}
				path := filepath.Join(dir, name)
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(kind, "fifo") {
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(path+".retained", path); err != nil {
					t.Fatal(err)
				}
			case "root_symlink", "instance_symlink":
				path := dir
				if kind == "root_symlink" {
					path = filepath.Dir(dir)
				}
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".retained", path); err != nil {
					t.Fatal(err)
				}
			case "socket_symlink":
				path, _ := socketPath(reg.ID)
				if err := os.Symlink(path+".missing", path); err != nil {
					t.Fatal(err)
				}
			case "running":
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			case "unknown_hello":
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"other"}`, nativeStatusSnapshot(reg))
			case "unknown_snapshot":
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, "null")
			case "workspace_unavailable":
				m.workspace = workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
					return reg.Workspace, errors.New("private-scope-error")
				}}
			}
			plan, err := m.PrepareRemove(ctx, id, binding, guard)
			requireRemovalRefusal(t, err)
			if plan.capture != nil {
				t.Fatal("refusal retained executable plan")
			}
			requireStopNoShutdown(t, methods())
			if kind != "registration_missing" {
				if _, err := os.Lstat(filepath.Join(dir, "registration.json")); err != nil {
					t.Fatal("capture removed registration", err)
				}
			}
		})
	}
}

func TestNativeRemovePlanConsumesBeforeDriftAndRefusal(t *testing.T) {
	for _, kind := range []string{"cancelled", "expired", "busy", "registration_bytes", "same_id_replacement", "instance_replacement", "root_replacement", "root_change", "socket_change", "owner_added", "live_owner", "workspace_changed", "runner_started", "schedule_added", "schedule_removed", "schedule_replaced", "schedule_bytes", "schedule_fifo"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			if kind != "schedule_added" {
				writeRemovalSchedule(t, reg)
			}
			plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			unlock := func() {}
			methods := func() []string { return nil }
			switch kind {
			case "cancelled":
				cancel()
			case "expired":
				plan.capture.expires = time.Now().Add(-time.Second)
			case "busy":
				m.mu.Lock()
				unlock = m.mu.Unlock
			case "registration_bytes", "schedule_bytes":
				path := filepath.Join(dir, "registration.json")
				if kind == "schedule_bytes" {
					path = schedulePath(reg)
				}
				data, _ := os.ReadFile(path)
				if err := os.WriteFile(path, append(data, ' '), 0600); err != nil {
					t.Fatal(err)
				}
			case "same_id_replacement":
				if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
					t.Fatal(err)
				}
			case "instance_replacement", "root_replacement":
				path := dir
				if kind == "root_replacement" {
					path = filepath.Dir(dir)
				}
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				old := dir + ".retained"
				if kind == "root_replacement" {
					old = filepath.Join(path+".retained", reg.ID)
				}
				for _, name := range []string{"registration.json", "schedules.json"} {
					if err := os.Link(filepath.Join(old, name), filepath.Join(dir, name)); err != nil {
						t.Fatal(err)
					}
				}
			case "root_change":
				t.Setenv("RADAR_AGENT_ROOT", filepath.Dir(dir)+"-other")
			case "socket_change":
				t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "other.sock"))
			case "owner_added", "live_owner":
				pid := "987654"
				if kind == "live_owner" {
					pid = strconv.Itoa(os.Getpid())
				}
				if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(pid), 0600); err != nil {
					t.Fatal(err)
				}
			case "workspace_changed":
				changed := reg.Workspace
				changed.ID += "-other"
				m.workspace = workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) { return changed, nil }}
			case "runner_started":
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			case "schedule_added", "schedule_replaced":
				writeRemovalSchedule(t, reg)
			case "schedule_removed":
				if err := os.Remove(schedulePath(reg)); err != nil {
					t.Fatal(err)
				}
			case "schedule_fifo":
				if err := os.Remove(schedulePath(reg)); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(schedulePath(reg), 0600); err != nil {
					t.Fatal(err)
				}
			}
			requireRemovalRefusal(t, plan.Execute(ctx))
			unlock()
			requireRemovalRefusal(t, plan.Execute(context.Background()))
			if _, err := os.Lstat(filepath.Join(dir, "registration.json")); err != nil {
				t.Fatal("refused plan removed registration", err)
			}
			requireStopNoShutdown(t, methods())
		})
	}
}

func TestNativeRemovePlanRetainsFinalGuardAndClosesCallbacks(t *testing.T) {
	for _, kind := range []string{"denied", "skipped", "retained", "twice", "cancelled", "expired", "registration_replaced", "schedule_replaced", "owner_live", "runner_started", "hide_effect_error", "error_after_effects", "cancel_after_effects"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			dir, _ := instanceDir(reg.ID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var retained func() error
			var plan RemovePlan
			methods := func() []string { return nil }
			guard := func(_ context.Context, execute func() error) error {
				switch kind {
				case "denied":
					return errors.New("private-consent-denied")
				case "skipped":
					return nil
				case "retained":
					retained = execute
					return nil
				case "twice":
					_ = execute()
					return execute()
				case "cancelled":
					cancel()
				case "expired":
					plan.capture.expires = time.Now().Add(-time.Second)
				case "registration_replaced":
					if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
						t.Fatal(err)
					}
				case "schedule_replaced":
					writeRemovalSchedule(t, reg)
				case "owner_live":
					if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
						t.Fatal(err)
					}
				case "runner_started":
					methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
				case "hide_effect_error":
					if err := os.WriteFile(schedulePath(reg), []byte("private-invalid"), 0600); err != nil {
						t.Fatal(err)
					}
					_ = execute()
					return nil
				}
				err := execute()
				if kind == "error_after_effects" {
					return errors.New("private-lost-response")
				}
				if kind == "cancel_after_effects" {
					cancel()
				}
				return err
			}
			var err error
			plan, err = m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, guard)
			if err != nil {
				t.Fatal(err)
			}
			requireRemovalRefusal(t, plan.Execute(ctx))
			requireRemovalRefusal(t, plan.Execute(context.Background()))
			if retained != nil {
				if err := retained(); err == nil {
					t.Fatal("callback escaped manager serialization")
				}
			}
			_, statErr := os.Lstat(filepath.Join(dir, "registration.json"))
			changed := kind == "twice" || kind == "error_after_effects" || kind == "cancel_after_effects"
			if changed != os.IsNotExist(statErr) {
				t.Fatal("effects rolled back or pre-effect refusal mutated registration", statErr)
			}
			requireStopNoShutdown(t, methods())
		})
	}
}

func TestNativeRemovePlanConcurrentCopiesAndOtherOwner(t *testing.T) {
	reg, m := removePlanFixture(t)
	other := reg
	other.ID = "other"
	dir, _ := instanceDir(other.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, "registration.json"), other); err != nil {
		t.Fatal(err)
	}
	plan, err := m.PrepareRemove(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func(p RemovePlan) { defer wg.Done(); results <- p.Execute(context.Background()) }(plan)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 || CheckWorkspaceRemoval(reg.Workspace.Path) == nil {
		t.Fatal("replayed admission/released other owner", success)
	}
	if _, err := load(other.ID); err != nil {
		t.Fatal("removed unrelated owner", err)
	}
	var zero RemovePlan
	requireRemovalRefusal(t, zero.Execute(context.Background()))
	if !zero.ExpiresAt().IsZero() || zero.RetainedBytes() != 0 || zero.Preview() != (integration.AgentRemovePreview{}) {
		t.Fatal("zero plan retains evidence")
	}
}

func TestNativeRemovalSharedExecutorPauseBeforeUnlinkAndPartialFailure(t *testing.T) {
	for _, kind := range []string{"pause_failure", "unlink_failure", "success"} {
		t.Run(kind, func(t *testing.T) {
			reg, _ := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			failure := errors.New("private-fault")
			calls := 0
			err := removeRegistration(context.Background(), reg, func(r Registration) error {
				calls++
				if kind == "pause_failure" {
					return failure
				}
				return pauseSchedules(r)
			}, func(path string) error {
				calls++
				if calls != 2 || savedSchedule(t, reg).Enabled {
					t.Fatal("unlink preceded pause persistence")
				}
				if kind == "unlink_failure" {
					return failure
				}
				return os.Remove(path)
			})
			if (kind == "success") != (err == nil) || (kind == "pause_failure") != savedSchedule(t, reg).Enabled {
				t.Fatal("fault result or retained schedule incorrect", err)
			}
			dir, _ := instanceDir(reg.ID)
			_, statErr := os.Lstat(filepath.Join(dir, "registration.json"))
			if (kind == "success") != os.IsNotExist(statErr) {
				t.Fatal("registration rolled back or unlinked after pause failure", statErr)
			}
		})
	}
}

func TestNativeRemoveCaptureRechecksInFlightIdentity(t *testing.T) {
	for _, kind := range []string{"registration", "schedule", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.workspace = workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
				switch kind {
				case "registration":
					dir, _ := instanceDir(reg.ID)
					if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
						t.Fatal(err)
					}
				case "schedule":
					writeRemovalSchedule(t, reg)
				case "cancelled":
					cancel()
				}
				return reg.Workspace, nil
			}}
			_, err := m.PrepareRemove(ctx, reg.ID, reg.Workspace, allowStopGuard)
			requireRemovalRefusal(t, err)
		})
	}
}

func TestNativeRemovalSharedExecutorCancellationBetweenEffects(t *testing.T) {
	for _, boundary := range []string{"before_pause", "deadline_before_pause", "after_pause", "during_unlink", "pause_error_with_cancel", "unlink_error_with_cancel", "success"} {
		t.Run(boundary, func(t *testing.T) {
			reg, _ := removePlanFixture(t)
			writeRemovalSchedule(t, reg)
			expected, err := loadSchedules(reg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if boundary == "before_pause" {
				cancel()
			}
			if boundary == "deadline_before_pause" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer deadlineCancel()
			}
			fault := errors.New("private-fixture-fault")
			var calls []string
			err = removeRegistration(ctx, reg, func(r Registration) error {
				calls = append(calls, "pause")
				if boundary == "pause_error_with_cancel" {
					cancel()
					return fault
				}
				if err := pauseSchedules(r); err != nil {
					return err
				}
				if boundary == "after_pause" {
					cancel()
				}
				return nil
			}, func(path string) error {
				calls = append(calls, "unlink")
				if boundary == "during_unlink" || boundary == "unlink_error_with_cancel" {
					cancel()
				}
				if boundary == "unlink_error_with_cancel" {
					return fault
				}
				return os.Remove(path)
			})
			wantCalls := []string{"pause", "unlink"}
			wantErr := error(nil)
			removed, paused := true, true
			switch boundary {
			case "before_pause":
				wantCalls, wantErr, removed, paused = nil, context.Canceled, false, false
			case "deadline_before_pause":
				wantCalls, wantErr, removed, paused = nil, context.DeadlineExceeded, false, false
			case "after_pause":
				wantCalls, wantErr, removed = []string{"pause"}, context.Canceled, false
			case "during_unlink":
				wantErr = context.Canceled // Both effects may already have completed; no rollback.
			case "pause_error_with_cancel":
				wantCalls, wantErr, removed, paused = []string{"pause"}, fault, false, false
			case "unlink_error_with_cancel":
				wantErr, removed = fault, false
			}
			if !errors.Is(err, wantErr) || !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("wrong boundary result: err=%v calls=%v; want err=%v calls=%v", err, calls, wantErr, wantCalls)
			}
			if paused {
				expected.Schedules[0].Enabled = false
			}
			actual, readErr := loadSchedules(reg)
			if readErr != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatal("cancellation rewrote capture payloads or rolled back effects", readErr)
			}
			dir, _ := instanceDir(reg.ID)
			_, statErr := os.Lstat(filepath.Join(dir, "registration.json"))
			if removed != os.IsNotExist(statErr) {
				t.Fatal("registration effect disagrees with cancellation boundary", statErr)
			}
			// Preserving this registration after a partial pause preserves its owner hold.
			if !removed && CheckWorkspaceRemoval(reg.Workspace.Path) == nil {
				t.Fatal("cancellation released registration ownership")
			}
		})
	}
}
