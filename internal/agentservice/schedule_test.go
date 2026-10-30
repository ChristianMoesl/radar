package agentservice

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/protocol"
)

func scheduleTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t
}

func workdaySchedule() protocol.AgentScheduleConfig {
	return protocol.AgentScheduleConfig{Name: "morning", Time: "09:00", Timezone: "UTC", Weekdays: []string{"mon", "tue", "wed", "thu", "fri"}, Payload: json.RawMessage(`{"type":"daily.check","windowHours":24}`)}
}

func TestCalendarWeekdaysTimezonesAndDST(t *testing.T) {
	for _, test := range []struct {
		name, zone, clock, days, limit, want string
		forward                              bool
	}{
		{"Friday to Monday", "Europe/Vienna", "09:00", "mon,tue,wed,thu,fri", "2026-10-23T07:00:00Z", "2026-10-26T08:00:00Z", true},
		{"no weekend occurrence", "Europe/Vienna", "09:00", "mon,tue,wed,thu,fri", "2026-10-25T18:00:00Z", "2026-10-23T07:00:00Z", false},
		{"spring workday offset", "Europe/Vienna", "09:00", "mon,tue,wed,thu,fri", "2026-03-27T08:00:00Z", "2026-03-30T07:00:00Z", true},
		{"spring gap skips day", "Europe/Vienna", "02:30", "sun", "2026-03-28T00:00:00Z", "2026-04-05T00:30:00Z", true},
		{"spring gap backward", "America/New_York", "02:30", "sun", "2026-03-09T00:00:00Z", "2026-03-01T07:30:00Z", false},
		{"fold chooses first", "Europe/Vienna", "02:30", "sun", "2026-10-24T00:00:00Z", "2026-10-25T00:30:00Z", true},
		{"fold never emits second", "Europe/Vienna", "02:30", "sun", "2026-10-25T00:31:00Z", "2026-11-01T01:30:00Z", true},
		{"fold backward agrees", "Europe/Vienna", "02:30", "sun", "2026-10-25T01:31:00Z", "2026-10-25T00:30:00Z", false},
		{"half-hour fold", "Australia/Lord_Howe", "01:45", "sun", "2026-04-04T00:00:00Z", "2026-04-04T14:45:00Z", true},
		{"half-hour gap", "Australia/Lord_Howe", "02:15", "sun", "2026-10-03T00:00:00Z", "2026-10-10T15:15:00Z", true},
		{"skipped civil date", "Pacific/Apia", "09:00", "fri", "2011-12-29T20:00:00Z", "2012-01-05T19:00:00Z", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := workdaySchedule()
			c.Timezone, c.Time, c.Weekdays = test.zone, test.clock, strings.Split(test.days, ",")
			if err := validateSchedule(c); err != nil {
				t.Fatal(err)
			}
			got := scheduleOccurrence(c, scheduleTime(test.limit), test.forward)
			if !got.Equal(scheduleTime(test.want)) {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
	for _, mutate := range []func(*protocol.AgentScheduleConfig){
		func(c *protocol.AgentScheduleConfig) { c.Timezone = "" },
		func(c *protocol.AgentScheduleConfig) { c.Timezone = "Local" },
		func(c *protocol.AgentScheduleConfig) { c.Timezone = "Europe/Invalid" },
		func(c *protocol.AgentScheduleConfig) { c.Time = "9:00" },
		func(c *protocol.AgentScheduleConfig) { c.Time = "24:00" },
		func(c *protocol.AgentScheduleConfig) { c.Weekdays = nil },
		func(c *protocol.AgentScheduleConfig) { c.Weekdays = []string{"mon", "mon"} },
		func(c *protocol.AgentScheduleConfig) { c.Weekdays = []string{"Monday"} },
		func(c *protocol.AgentScheduleConfig) { c.Name = "../escape" },
		func(c *protocol.AgentScheduleConfig) { c.Payload = json.RawMessage(`{`) },
		func(c *protocol.AgentScheduleConfig) {
			c.Payload = json.RawMessage(`"` + strings.Repeat("x", protocol.AgentMaxFrameBytes) + `"`)
		},
	} {
		c := workdaySchedule()
		mutate(&c)
		if err := validateSchedule(c); err == nil {
			t.Fatalf("invalid schedule accepted: %+v", c)
		}
	}
}

func configuredSchedule(t *testing.T, m *Manager, reg Registration, config protocol.AgentScheduleConfig) {
	t.Helper()
	params, _ := json.Marshal(config)
	if _, err := m.Handle(context.Background(), protocol.AgentRequest{Operation: "schedule.set", ID: reg.ID, Params: params}); err != nil {
		t.Fatal(err)
	}
}

func savedSchedule(t *testing.T, reg Registration) calendarSchedule {
	t.Helper()
	state, err := loadSchedules(reg)
	if err != nil || len(state.Schedules) != 1 {
		t.Fatalf("schedule state: %+v, %v", state, err)
	}
	return state.Schedules[0]
}

// A transport peer that can lose or contradict admission evidence. Other RPCs
// fail immediately so inspection does not affect the delivery attempt count.
func schedulePeer(t *testing.T, reg Registration, reply func(int, scheduledEvent) any) *atomic.Int32 {
	t.Helper()
	path, _ := socketPath(reg.ID)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	count := &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var request struct {
				Method string
				Params scheduledEvent
			}
			_ = json.NewDecoder(conn).Decode(&request)
			if request.Method == "deliver" {
				if result := reply(int(count.Add(1)), request.Params); result != nil {
					_ = json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
				}
			} else {
				_ = json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32601, "message": "fixture only supports delivery"}})
			}
			_ = conn.Close()
		}
	}()
	return count
}

func scheduleReceipt(event scheduledEvent) map[string]any {
	return map[string]any{"taskId": 7, "source": event.Source, "eventId": event.ID, "acceptedAt": "2026-10-26T09:00:01Z"}
}

func TestScheduleLostAdmissionRestartPauseAndMatchingEvidence(t *testing.T) {
	m, reg := collectionFixture(t)
	clock := scheduleTime("2026-10-26T08:00:00Z")
	m.now = func() time.Time { return clock }
	configuredSchedule(t, m, reg, workdaySchedule())
	reg.Desired = true
	initial := savedSchedule(t, reg)
	wires := make(chan string, 3)
	count := schedulePeer(t, reg, func(attempt int, event scheduledEvent) any {
		wire, _ := json.Marshal(event)
		wires <- string(wire)
		if attempt == 1 {
			return nil
		}
		receipt := scheduleReceipt(event)
		if attempt == 2 {
			receipt["eventId"] = "wrong-occurrence"
		}
		return receipt
	})
	clock = scheduleTime("2026-10-26T09:00:00Z")
	if err := m.tickSchedules(context.Background(), reg, clock, true); err == nil {
		t.Fatal("lost response advanced admission")
	}
	pending := savedSchedule(t, reg)
	if pending.Pending == nil || pending.LastAdmission != nil || !pending.EligibleAfter.Equal(initial.EligibleAfter) || pending.Problem == "" {
		t.Fatalf("uncertain admission lost: %+v", pending)
	}
	params, _ := json.Marshal(workdaySchedule())
	for _, operation := range []string{"schedule.set", "schedule.remove"} {
		if err := m.handleSchedule(reg, protocol.AgentRequest{Operation: operation, Params: params}); err == nil {
			t.Fatal("uncertain occurrence edited/removed")
		}
	}
	if err := m.handleSchedule(reg, protocol.AgentRequest{Operation: "schedule.pause", Params: params}); err != nil {
		t.Fatal(err)
	}
	if err := m.tickSchedules(context.Background(), reg, clock, true); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatal("paused schedule retried")
	}
	// A new process has no memory of the uncertain response, and the pending
	// identity must survive even beyond the normal catch-up deadline.
	m = New(m.workspace)
	clock = scheduleTime("2026-10-28T10:00:00Z")
	m.now = func() time.Time { return clock }
	if err := m.handleSchedule(reg, protocol.AgentRequest{Operation: "schedule.resume", Params: params}); err != nil {
		t.Fatal(err)
	}
	if err := m.tickSchedules(context.Background(), reg, clock, true); err == nil {
		t.Fatal("mismatched evidence advanced admission")
	}
	if savedSchedule(t, reg).LastAdmission != nil {
		t.Fatal("mismatched evidence checkpointed")
	}
	if err := m.tickSchedules(context.Background(), reg, clock, true); err != nil {
		t.Fatal(err)
	}
	first, second, third := <-wires, <-wires, <-wires
	if first != second || first != third {
		t.Fatal("retry changed source, ID or content")
	}
	after := savedSchedule(t, reg)
	if after.Pending != nil || after.Problem != "" || after.LastAdmission == nil || after.LastAdmission.TaskID != 7 || !after.LastAdmission.ScheduledAt.Equal(scheduleTime("2026-10-26T09:00:00Z")) || !after.EligibleAfter.Equal(clock) {
		t.Fatalf("matching receipt/resume floor not retained: %+v", after)
	}
	if err := m.tickSchedules(context.Background(), reg, clock, true); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 3 {
		t.Fatal("resume replayed paused history")
	}
}

func TestScheduleBoundedCatchUpWhileStoppedUnavailableAndAfterDowntime(t *testing.T) {
	m, reg := collectionFixture(t)
	m.now = func() time.Time { return scheduleTime("2026-10-01T08:00:00Z") }
	configuredSchedule(t, m, reg, workdaySchedule())
	count := schedulePeer(t, reg, func(_ int, event scheduledEvent) any { return scheduleReceipt(event) })
	clock := scheduleTime("2026-10-26T10:00:00Z")
	if err := m.tickSchedules(context.Background(), reg, clock, true); err != nil {
		t.Fatal(err)
	}
	s := savedSchedule(t, reg)
	if s.Pending != nil || !strings.Contains(s.Problem, "intentionally stopped") || count.Load() != 0 {
		t.Fatalf("stopped agent received work: %+v", s)
	}
	if loaded, _ := load(reg.ID); loaded.Desired {
		t.Fatal("timer started stopped agent")
	}
	reg.Desired = true
	if err := m.tickSchedules(context.Background(), reg, clock, false); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 0 || savedSchedule(t, reg).Pending != nil {
		t.Fatal("unavailable agent accumulated an outbox")
	}
	if err := m.tickSchedules(context.Background(), reg, clock, true); err != nil {
		t.Fatal(err)
	}
	s = savedSchedule(t, reg)
	if count.Load() != 1 || s.LastAdmission == nil || !s.LastAdmission.CatchUp || !s.LastAdmission.ScheduledAt.Equal(scheduleTime("2026-10-26T09:00:00Z")) || !s.LastMissedAt.Equal(scheduleTime("2026-10-23T09:00:00Z")) {
		t.Fatalf("did not coalesce to latest workday: %+v", s)
	}
	// Friday is outside the catch-up limit by Saturday morning. No Saturday
	// event exists and no backlog is queued, even after a later restart.
	clock = scheduleTime("2026-10-31T10:00:00Z")
	if err := m.tickSchedules(context.Background(), reg, clock, true); err != nil {
		t.Fatal(err)
	}
	s = savedSchedule(t, reg)
	if count.Load() != 1 || !s.LastMissedAt.Equal(scheduleTime("2026-10-30T09:00:00Z")) || s.Pending != nil {
		t.Fatalf("expired/weekend occurrence delivered: %+v", s)
	}
	m = New(m.workspace)
	if err := m.tickSchedules(context.Background(), reg, clock, true); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatal("restart replayed skipped work")
	}
	// The calendar search remains bounded after a decade, delivering one task.
	clock = scheduleTime("2036-10-27T10:00:00Z")
	if err := m.tickSchedules(context.Background(), reg, clock, true); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Fatal("long downtime did not coalesce to one latest event")
	}
}

func TestScheduleCatchUpBoundaryAndCreationAtDueTime(t *testing.T) {
	for _, test := range []struct {
		name, created, tick string
		admitted            bool
	}{
		{"on time", "2026-10-30T08:00:00Z", "2026-10-30T09:00:00Z", true},
		{"within 24 hours", "2026-10-30T08:00:00Z", "2026-10-31T08:59:59Z", true},
		{"exactly 24 hours", "2026-10-30T08:00:00Z", "2026-10-31T09:00:00Z", true},
		{"expired by one nanosecond", "2026-10-30T08:00:00Z", "2026-10-31T09:00:00.000000001Z", false},
		{"creation excludes current occurrence", "2026-10-30T09:00:00Z", "2026-10-30T09:00:00Z", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, reg := collectionFixture(t)
			m.now = func() time.Time { return scheduleTime(test.created) }
			configuredSchedule(t, m, reg, workdaySchedule())
			reg.Desired = true
			count := schedulePeer(t, reg, func(_ int, event scheduledEvent) any { return scheduleReceipt(event) })
			if err := m.tickSchedules(context.Background(), reg, scheduleTime(test.tick), true); err != nil {
				t.Fatal(err)
			}
			if (count.Load() == 1) != test.admitted {
				t.Fatalf("unexpected admission count %d", count.Load())
			}
		})
	}
}

func TestSchedulePayloadLimitSurvivesIndentedPersistence(t *testing.T) {
	m, reg := collectionFixture(t)
	c := workdaySchedule()
	c.Payload = json.RawMessage("[" + strings.Repeat("0,", 100000) + "0]")
	configuredSchedule(t, m, reg, c)
	s := savedSchedule(t, reg)
	if len(s.Payload) <= protocol.AgentMaxFrameBytes {
		t.Fatal("fixture did not exercise indented expansion")
	}
	var original, restored []int
	if err := json.Unmarshal(c.Payload, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(s.Payload, &restored); err != nil {
		t.Fatal(err)
	}
	if len(original) != len(restored) {
		t.Fatal("payload changed during persistence")
	}
}

func TestScheduleEditsPauseResumeRemovalAndRestoration(t *testing.T) {
	m, reg := collectionFixture(t)
	clock := scheduleTime("2026-10-26T08:00:00Z")
	m.now = func() time.Time { return clock }
	c := workdaySchedule()
	configuredSchedule(t, m, reg, c)
	id := savedSchedule(t, reg).ID
	request := func(operation string) protocol.AgentStatus {
		t.Helper()
		params, _ := json.Marshal(c)
		data, err := m.Handle(context.Background(), protocol.AgentRequest{Operation: operation, ID: reg.ID, Params: params})
		if err != nil {
			t.Fatal(err)
		}
		var status protocol.AgentStatus
		if err := json.Unmarshal(data, &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	status := request("schedule.pause")
	if status.Schedules[0].Enabled || !status.Schedules[0].NextDue.IsZero() {
		t.Fatal("paused status has next delivery")
	}
	clock = scheduleTime("2026-10-27T10:00:00Z")
	request("schedule.resume")
	if err := m.tickSchedules(context.Background(), reg, clock, true); err != nil {
		t.Fatal(err)
	}
	if savedSchedule(t, reg).Problem != "" {
		t.Fatal("resume caught up paused history")
	}
	floor := savedSchedule(t, reg).EligibleAfter
	clock = clock.Add(time.Minute)
	request("schedule.resume")
	if !savedSchedule(t, reg).EligibleAfter.Equal(floor) {
		t.Fatal("repeated resume reset scheduling window")
	}
	c.Time = "11:00"
	configuredSchedule(t, m, reg, c)
	if s := savedSchedule(t, reg); s.ID != id || !s.EligibleAfter.Equal(clock) {
		t.Fatalf("edit changed identity or replayed history: %+v", s)
	}
	status = request("inspect")
	if !status.Schedules[0].NextDue.Equal(scheduleTime("2026-10-27T11:00:00Z")) {
		t.Fatal("edited next time not inspectable")
	}
	request("remove")
	if savedSchedule(t, reg).Enabled {
		t.Fatal("registration removal did not pause schedules")
	}
	entry := filepath.Join(t.TempDir(), "agent.ts")
	if err := os.WriteFile(entry, []byte("export default {};"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Handle(context.Background(), protocol.AgentRequest{Operation: "add", ID: reg.ID, Collection: reg.Collection, Workspace: reg.Workspace.Path, Entrypoint: entry}); err != nil {
		t.Fatal(err)
	}
	if s := savedSchedule(t, reg); s.Enabled || s.ID != id {
		t.Fatal("restore reactivated/replaced retained schedule")
	}
	request("schedule.remove")
	state, err := loadSchedules(reg)
	if err != nil || len(state.Schedules) != 0 {
		t.Fatal("schedule removal failed")
	}
	configuredSchedule(t, m, reg, c)
	if savedSchedule(t, reg).ID == id {
		t.Fatal("recreated schedule reused removed identity")
	}
}

func TestInvalidScheduleCheckpointPreservedAndVisible(t *testing.T) {
	m, reg := collectionFixture(t)
	for _, value := range []string{`{"version":999,"schedules":[]}`, `{}`, `{"version":1,"schedules":[{}]}`, `{`} {
		data := []byte(value)
		if err := os.WriteFile(schedulePath(reg), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := m.tickSchedules(context.Background(), reg, time.Now(), true); err == nil {
			t.Fatal("invalid checkpoint reset")
		}
		var status protocol.AgentStatus
		m.scheduleStatus(reg, &status)
		if status.ScheduleError == "" {
			t.Fatal("checkpoint failure hidden")
		}
		after, _ := os.ReadFile(schedulePath(reg))
		if string(after) != string(data) {
			t.Fatal("invalid state overwritten")
		}
	}
}

func TestScheduleReceiptRequiresSourceTaskAndAdmissionTime(t *testing.T) {
	for _, field := range []string{"source", "taskId", "acceptedAt"} {
		t.Run(field, func(t *testing.T) {
			m, reg := collectionFixture(t)
			m.now = func() time.Time { return scheduleTime("2026-10-26T08:00:00Z") }
			configuredSchedule(t, m, reg, workdaySchedule())
			reg.Desired = true
			schedulePeer(t, reg, func(_ int, event scheduledEvent) any {
				receipt := scheduleReceipt(event)
				delete(receipt, field)
				return receipt
			})
			if err := m.tickSchedules(context.Background(), reg, scheduleTime("2026-10-26T09:00:00Z"), true); err == nil {
				t.Fatal("incomplete evidence accepted")
			}
			s := savedSchedule(t, reg)
			if s.Pending == nil || s.LastAdmission != nil {
				t.Fatal("incomplete evidence advanced admission history")
			}
		})
	}
}

func TestScheduleClockRollbackDoesNotReopenAdmittedWindow(t *testing.T) {
	m, reg := collectionFixture(t)
	clock := scheduleTime("2026-10-26T08:00:00Z")
	m.now = func() time.Time { return clock }
	c := workdaySchedule()
	configuredSchedule(t, m, reg, c)
	reg.Desired = true
	count := schedulePeer(t, reg, func(_ int, event scheduledEvent) any { return scheduleReceipt(event) })
	if err := m.tickSchedules(context.Background(), reg, scheduleTime("2026-10-26T09:00:00Z"), true); err != nil {
		t.Fatal(err)
	}
	clock = scheduleTime("2026-10-26T07:00:00Z")
	c.Payload = json.RawMessage(`{"type":"edited"}`)
	configuredSchedule(t, m, reg, c)
	params, _ := json.Marshal(c)
	for _, operation := range []string{"schedule.pause", "schedule.resume"} {
		if err := m.handleSchedule(reg, protocol.AgentRequest{Operation: operation, Params: params}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.tickSchedules(context.Background(), reg, scheduleTime("2026-10-26T09:00:00Z"), true); err != nil {
		t.Fatal(err)
	}
	var status protocol.AgentStatus
	m.scheduleStatus(reg, &status)
	if count.Load() != 1 || !status.Schedules[0].NextDue.Equal(scheduleTime("2026-10-27T09:00:00Z")) {
		t.Fatal("clock rollback replayed an admission or advertised an ineligible occurrence")
	}
}

// Used by the native admission integration test as a transparent Unix RPC
// proxy. The first admission response is deliberately lost after SQLite commits.
func loseFirstScheduleResponse(t *testing.T, reg Registration) (<-chan json.RawMessage, func()) {
	t.Helper()
	path, _ := socketPath(reg.ID)
	native := path + ".native"
	if err := os.Rename(path, native); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	lost := make(chan json.RawMessage, 1)
	var dropped atomic.Bool
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				upstream, err := net.Dial("unix", native)
				if err != nil {
					return
				}
				defer upstream.Close()
				wire, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var request struct{ Method string }
				_ = json.Unmarshal(wire, &request)
				_, _ = upstream.Write(wire)
				response, err := bufio.NewReader(upstream).ReadBytes('\n')
				if err != nil {
					return
				}
				if request.Method == "deliver" && !dropped.Swap(true) {
					lost <- response
					return
				}
				_, _ = conn.Write(response)
			}()
		}
	}()
	var once sync.Once
	restore := func() {
		once.Do(func() {
			_ = listener.Close()
			_ = os.Remove(path)
			_ = os.Rename(native, path)
		})
	}
	return lost, restore
}
