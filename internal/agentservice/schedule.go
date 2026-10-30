package agentservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"

	"radar/internal/protocol"
)

const scheduleCatchUp = 24 * time.Hour

var scheduleWeekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

type scheduledEvent struct {
	Source  string          `json:"source"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

type schedulePending struct {
	Event       scheduledEvent `json:"event"`
	ScheduledAt time.Time      `json:"scheduled_at"`
	CatchUp     bool           `json:"catch_up"`
}

type calendarSchedule struct {
	protocol.AgentScheduleConfig
	ID            string                           `json:"id"`
	Enabled       bool                             `json:"enabled"`
	EligibleAfter time.Time                        `json:"eligible_after"`
	Pending       *schedulePending                 `json:"pending,omitempty"`
	LastAdmission *protocol.AgentScheduleAdmission `json:"last_admission,omitempty"`
	LastMissedAt  time.Time                        `json:"last_missed_at,omitzero"`
	Problem       string                           `json:"problem,omitempty"`
}

// This is an admission outbox, not an execution queue. There is at most one
// immutable, possibly admitted event per schedule. Only its matching receipt
// advances LastAdmission. EligibleAfter also records explicit skips/resets.
type scheduleState struct {
	Version   int                `json:"version"`
	Schedules []calendarSchedule `json:"schedules"`
}

func schedulePath(reg Registration) string {
	dir, _ := instanceDir(reg.ID)
	return filepath.Join(dir, "schedules.json")
}

func validateSchedule(config protocol.AgentScheduleConfig) error {
	if !validID.MatchString(config.Name) {
		return fmt.Errorf("schedule name must contain 1–64 lowercase letters, digits, underscores, or hyphens")
	}
	t, err := time.Parse("15:04", config.Time)
	if err != nil || t.Format("15:04") != config.Time {
		return fmt.Errorf("schedule time must be HH:MM in 24-hour local time")
	}
	if config.Timezone == "" || config.Timezone == "Local" {
		return fmt.Errorf("schedule requires an explicit IANA timezone")
	}
	if _, err := time.LoadLocation(config.Timezone); err != nil {
		return fmt.Errorf("invalid IANA timezone %q: %w", config.Timezone, err)
	}
	if len(config.Weekdays) == 0 {
		return fmt.Errorf("schedule requires selected weekdays (mon,tue,wed,thu,fri,sat,sun)")
	}
	seen := map[string]bool{}
	for _, day := range config.Weekdays {
		if !slices.Contains(scheduleWeekdays, day) || seen[day] {
			return fmt.Errorf("invalid or duplicate schedule weekday %q", day)
		}
		seen[day] = true
	}
	payload, err := json.Marshal(config.Payload)
	if len(config.Payload) == 0 || err != nil {
		return fmt.Errorf("schedule payload must be JSON")
	}
	// Leave room for schedule metadata and both daemon and runner envelopes.
	// Measure encoded bytes: pretty-printing persisted JSON must not turn a
	// valid payload into an invalid checkpoint on the next read.
	if len(payload) > protocol.AgentMaxFrameBytes-16384 {
		return fmt.Errorf("schedule payload exceeds admission limit")
	}
	return nil
}

func loadSchedules(reg Registration) (scheduleState, error) {
	state := scheduleState{Version: 1}
	data, err := os.ReadFile(schedulePath(reg))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state = scheduleState{}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Version != 1 {
		return state, fmt.Errorf("unsupported schedule checkpoint; preserve agent data")
	}
	names, ids := map[string]bool{}, map[string]bool{}
	for _, s := range state.Schedules {
		if err := validateSchedule(s.AgentScheduleConfig); err != nil {
			return state, err
		}
		if s.ID == "" || ids[s.ID] || names[s.Name] || s.EligibleAfter.IsZero() {
			return state, fmt.Errorf("invalid schedule checkpoint; preserve agent data")
		}
		if a := s.LastAdmission; a != nil && (a.TaskID <= 0 || a.ScheduledAt.IsZero() || a.AcceptedAt.IsZero() || a.EventID != scheduleEventID(s.ID, a.ScheduledAt)) {
			return state, fmt.Errorf("invalid schedule admission checkpoint; preserve agent data")
		}
		names[s.Name], ids[s.ID] = true, true
		if p := s.Pending; p != nil {
			if p.ScheduledAt.IsZero() || p.Event.Source != "radar.schedule" || p.Event.ID != scheduleEventID(s.ID, p.ScheduledAt) || !json.Valid(p.Event.Payload) {
				return state, fmt.Errorf("invalid pending schedule event; preserve agent data")
			}
		}
	}
	return state, nil
}

// Resolve wall time by all offsets around the date, verifying the result. Gaps
// have no candidate; folds select the first instant, regardless of Go's choice
// in time.Date. Iterate zone boundaries, not minutes or elapsed 24-hour days.
func scheduleWallTime(date time.Time, clock string, loc *time.Location) time.Time {
	parsed, _ := time.Parse("15:04", clock)
	y, month, day := date.Date()
	wall := time.Date(y, month, day, parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
	var earliest time.Time
	end := wall.Add(48 * time.Hour)
	for probe := wall.Add(-48 * time.Hour); !probe.After(end); {
		local := probe.In(loc)
		_, offset := local.Zone()
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		c := candidate.In(loc)
		if c.Year() == y && c.Month() == month && c.Day() == day && c.Hour() == parsed.Hour() && c.Minute() == parsed.Minute() && (earliest.IsZero() || candidate.Before(earliest)) {
			earliest = candidate
		}
		_, boundary := local.ZoneBounds()
		if boundary.IsZero() || !boundary.After(probe) {
			break
		}
		probe = boundary
	}
	return earliest
}

func scheduleOccurrence(config protocol.AgentScheduleConfig, limit time.Time, forward bool) time.Time {
	loc, _ := time.LoadLocation(config.Timezone) // Validated at every load/set.
	y, month, day := limit.In(loc).Date()
	date := time.Date(y, month, day, 12, 0, 0, 0, time.UTC)
	step := -1
	if forward {
		step = 1
	}
	// Three calendar weeks bound work even after years of downtime, while
	// allowing an entire civil date or a selected DST-gap day to be skipped.
	for i := 0; i < 22; i++ {
		if slices.Contains(config.Weekdays, scheduleWeekdays[date.Weekday()]) {
			due := scheduleWallTime(date, config.Time, loc)
			if !due.IsZero() && ((forward && due.After(limit)) || (!forward && !due.After(limit))) {
				return due
			}
		}
		date = date.AddDate(0, 0, step)
	}
	return time.Time{}
}

func scheduleEventID(id string, at time.Time) string {
	return id + ":" + at.UTC().Format(time.RFC3339)
}

func (m *Manager) handleSchedule(reg Registration, request protocol.AgentRequest) error {
	state, err := loadSchedules(reg)
	if err != nil {
		return err
	}
	var config protocol.AgentScheduleConfig
	if err := json.Unmarshal(request.Params, &config); err != nil {
		return err
	}
	index := slices.IndexFunc(state.Schedules, func(s calendarSchedule) bool { return s.Name == config.Name })
	now := m.now().UTC()
	if request.Operation == "schedule.set" {
		if err := validateSchedule(config); err != nil {
			return err
		}
		if index < 0 {
			id := make([]byte, 16)
			if _, err := rand.Read(id); err != nil {
				return err
			}
			state.Schedules = append(state.Schedules, calendarSchedule{ID: hex.EncodeToString(id), Enabled: true})
			index = len(state.Schedules) - 1
		}
		s := &state.Schedules[index]
		if s.Pending != nil {
			return fmt.Errorf("schedule has an uncertain admission; resume/start the agent to reconcile it before editing")
		}
		if now.Before(s.EligibleAfter) {
			now = s.EligibleAfter
		}
		s.AgentScheduleConfig, s.EligibleAfter, s.Problem = config, now, ""
	} else {
		if index < 0 {
			return fmt.Errorf("schedule %q does not exist", config.Name)
		}
		s := &state.Schedules[index]
		switch request.Operation {
		case "schedule.pause":
			s.Enabled = false
		case "schedule.resume":
			if !s.Enabled {
				if now.Before(s.EligibleAfter) {
					now = s.EligibleAfter
				}
				s.Enabled, s.EligibleAfter = true, now
				if s.Pending == nil {
					s.Problem = ""
				}
			}
		case "schedule.remove":
			if s.Pending != nil {
				return fmt.Errorf("schedule has an uncertain admission; resume/start the agent to reconcile it before removal")
			}
			state.Schedules = slices.Delete(state.Schedules, index, index+1)
		default:
			return fmt.Errorf("unknown schedule operation %q", request.Operation)
		}
	}
	slices.SortFunc(state.Schedules, func(a, b calendarSchedule) int { return strings.Compare(a.Name, b.Name) })
	return atomicJSON(schedulePath(reg), state)
}

func pauseSchedules(reg Registration) error {
	state, err := loadSchedules(reg)
	if err != nil || len(state.Schedules) == 0 {
		return err
	}
	for i := range state.Schedules {
		state.Schedules[i].Enabled = false
	}
	return atomicJSON(schedulePath(reg), state)
}

func (m *Manager) scheduleStatus(reg Registration, status *protocol.AgentStatus) {
	state, err := loadSchedules(reg)
	if err != nil {
		status.ScheduleError = err.Error()
		return
	}
	status.ScheduleError = m.scheduleErrors[reg.ID]
	for _, s := range state.Schedules {
		value := protocol.AgentScheduleStatus{AgentScheduleConfig: s.AgentScheduleConfig, ID: s.ID, Enabled: s.Enabled, LastAdmission: s.LastAdmission, LastMissedAt: s.LastMissedAt, Error: s.Problem}
		if s.Enabled {
			floor := m.now()
			if floor.Before(s.EligibleAfter) {
				floor = s.EligibleAfter
			}
			value.NextDue = scheduleOccurrence(s.AgentScheduleConfig, floor, true)
		}
		if s.Pending != nil {
			value.PendingAt = s.Pending.ScheduledAt
		}
		status.Schedules = append(status.Schedules, value)
	}
}

// Caller holds m.mu, shared with configuration and lifecycle operations.
func (m *Manager) tickSchedules(ctx context.Context, reg Registration, now time.Time, available bool) error {
	state, err := loadSchedules(reg)
	if err != nil {
		return err
	}
	var failures []error
	for i := range state.Schedules {
		s := &state.Schedules[i]
		if !s.Enabled {
			continue
		}
		before, _ := json.Marshal(s)
		if s.Pending == nil {
			latest := scheduleOccurrence(s.AgentScheduleConfig, now, false)
			if latest.IsZero() || !latest.After(s.EligibleAfter) {
				continue
			}
			prior := scheduleOccurrence(s.AgentScheduleConfig, latest.Add(-time.Nanosecond), false)
			if prior.After(s.EligibleAfter) {
				s.LastMissedAt = prior
			}
			if now.Sub(latest) > scheduleCatchUp {
				s.LastMissedAt, s.EligibleAfter, s.Problem = latest, latest, ""
			} else if !reg.Desired || !available {
				s.Problem = "schedule due; agent unavailable"
				if !reg.Desired {
					s.Problem = "schedule due; agent intentionally stopped"
				}
			} else {
				payload, err := json.Marshal(map[string]any{"schedule": map[string]any{"id": s.ID, "name": s.Name, "timezone": s.Timezone, "scheduledAt": latest.UTC().Format(time.RFC3339)}, "data": s.Payload})
				if err != nil {
					return err
				}
				s.Pending = &schedulePending{Event: scheduledEvent{Source: "radar.schedule", ID: scheduleEventID(s.ID, latest), Payload: payload}, ScheduledAt: latest, CatchUp: now.Sub(latest) > time.Minute}
			}
		}
		after, _ := json.Marshal(s)
		if string(before) != string(after) {
			// Persist bytes/identity before the first possibly successful RPC.
			if err := atomicJSON(schedulePath(reg), state); err != nil {
				return err
			}
		}
		if s.Pending == nil || !reg.Desired || !available {
			continue
		}
		p := s.Pending
		var receipt struct {
			TaskID     int       `json:"taskId"`
			Source     string    `json:"source"`
			EventID    string    `json:"eventId"`
			AcceptedAt time.Time `json:"acceptedAt"`
		}
		path, err := socketPath(reg.ID)
		if err == nil {
			err = call(ctx, path, "deliver", p.Event, &receipt)
		}
		if err == nil && (receipt.TaskID <= 0 || receipt.Source != p.Event.Source || receipt.EventID != p.Event.ID || receipt.AcceptedAt.IsZero()) {
			err = fmt.Errorf("invalid schedule admission receipt")
		}
		if err != nil {
			s.Problem = "schedule admission pending: " + err.Error()
			failures = append(failures, fmt.Errorf("schedule %s: %w", s.Name, err))
		} else {
			s.LastAdmission = &protocol.AgentScheduleAdmission{ScheduledAt: p.ScheduledAt, AcceptedAt: receipt.AcceptedAt, TaskID: receipt.TaskID, EventID: receipt.EventID, CatchUp: p.CatchUp}
			// Resume may have intentionally moved the eligibility floor forward.
			if p.ScheduledAt.After(s.EligibleAfter) {
				s.EligibleAfter = p.ScheduledAt
			}
			s.Pending, s.Problem = nil, ""
		}
		if saveErr := atomicJSON(schedulePath(reg), state); saveErr != nil {
			return errors.Join(err, saveErr)
		}
	}
	return errors.Join(failures...)
}
