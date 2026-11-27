package agentservice

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// RemovePlan is private exact-registration admission, not a transport, preview
// token or user consent. Copies share consumption and the original monotonic
// deadline. No native cache, public control or persisted format is introduced.
// Unexported fields serialize as {}; checkpoint payloads are not retained.
type RemovePlan struct{ capture *removeCapture }

type removeCapture struct {
	mu             sync.Mutex
	consumed       bool
	manager        *Manager
	guard          integration.AgentRemoveGuard
	expires        time.Time
	preview        integration.AgentRemovePreview
	reg            Registration
	files          stopFiles // Reuse the exact no-follow registration/owner/socket capture.
	schedule       stopFile
	pausedSchedule [32]byte
	observation    integration.AgentRemoveObservation
}

func (p RemovePlan) Observation() integration.AgentRemoveObservation {
	if p.capture == nil {
		return integration.AgentRemoveObservation{}
	}
	p.capture.mu.Lock()
	defer p.capture.mu.Unlock()
	return p.capture.observation
}

func removePlanUnavailable() error {
	return errors.New("exact native agent removal unavailable or changed; outcome may be partial or unknown, do not replay or automatically retry")
}

// Context timer notification can lag its absolute deadline. Both are admission
// boundaries, including between effects and before publishing observations.
func removalContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, bounded := ctx.Deadline(); bounded && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (p RemovePlan) Preview() integration.AgentRemovePreview {
	if p.capture == nil {
		return integration.AgentRemovePreview{}
	}
	return p.capture.preview
}

func (p RemovePlan) ExpiresAt() time.Time {
	if p.capture == nil {
		return time.Time{}
	}
	return p.capture.expires
}

func (p RemovePlan) RetainedBytes() int {
	if p.capture == nil {
		return 0
	}
	return stopCaptureBytes(p.capture.reg, p.capture.files) + 1024
}

// PrepareRemove captures one caller-selected STOPPED registration and its exact
// workspace and schedule identity, without writing, stopping, preparing resources
// or reading tokens/store/snapshot payloads. Schedules are decoded transiently by
// the native decoder; only bounded file metadata/hash is retained. Busy, unknown,
// unsafe and cancelled evidence refuses, with cooperative 5s native I/O bound.
func (m *Manager) PrepareRemove(ctx context.Context, id string, expected protocol.AgentWorkspaceBinding, guard integration.AgentRemoveGuard) (RemovePlan, error) {
	if guard == nil || removalContextError(ctx) != nil || !validID.MatchString(id) || expected.ID == "" || !filepath.IsAbs(expected.Path) || filepath.Clean(expected.Path) != expected.Path || m.workspace == nil || !m.mu.TryLock() {
		return RemovePlan{}, removePlanUnavailable()
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	files, reg, err := captureStopFiles(id)
	if err != nil || reg.Workspace != expected {
		return RemovePlan{}, removePlanUnavailable()
	}
	state, schedule, err := removalSchedules(reg)
	if err != nil {
		return RemovePlan{}, removePlanUnavailable()
	}
	preview := integration.AgentRemovePreview{ID: reg.ID, Schedules: len(state.Schedules)}
	for _, s := range state.Schedules {
		if s.Enabled {
			preview.EnabledSchedules++
		}
	}
	paused, err := removalScheduleFingerprint(pausedScheduleState(state))
	if err != nil {
		return RemovePlan{}, removePlanUnavailable()
	}
	c := &removeCapture{manager: m, guard: guard, expires: time.Now().Add(30 * time.Second), preview: preview, reg: reg, files: files, schedule: schedule, pausedSchedule: paused}
	if !c.current(ctx) || stoppedForRemoval(ctx, reg) != nil || !c.current(ctx) {
		return RemovePlan{}, removePlanUnavailable()
	}
	return RemovePlan{capture: c}, nil
}

// Execute consumes BEFORE cancellation, expiry, busy/drift checks or effects.
// Uses the SAME native stopped-owner checks and schedule/unlink executor as
// ordinary remove, never generic Handle (which has no removal preview). The
// original final caller guard spans BOTH schedule persistence and unlink.
// Failure/cancellation/lost response may follow effects and never restores a
// plan, schedules or registration. Nil means handler returned, not absence of
// resources/other owners, cleanup eligibility, completed work or safe retry.
// Execution keeps the earliest original expiry, caller deadline and 5s cap;
// absolute deadlines apply even before their context timer notification.
func (p RemovePlan) Execute(ctx context.Context) (result error) {
	if p.capture == nil {
		return removePlanUnavailable()
	}
	c := p.capture
	c.mu.Lock()
	if c.consumed {
		c.mu.Unlock()
		return removePlanUnavailable()
	}
	c.consumed = true
	c.mu.Unlock()
	m := c.manager
	if removalContextError(ctx) != nil || !time.Now().Before(c.expires) || !m.mu.TryLock() {
		return removePlanUnavailable()
	}
	defer m.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	if c.expires.Before(deadline) {
		deadline = c.expires
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var observation integration.AgentRemoveObservation
	// Publish once, under manager serialization, BEFORE our own deferred cancel.
	// Later inspection is memory-only; it cannot refresh evidence or admission.
	defer func() {
		if removalContextError(ctx) != nil || !time.Now().Before(c.expires) {
			observation = integration.AgentRemoveObservation{}
			result = removePlanUnavailable()
		}
		c.mu.Lock()
		c.observation = observation
		c.mu.Unlock()
	}()
	if !c.current(ctx) {
		return removePlanUnavailable()
	}
	attempted := false
	err := removeWithAdmission(ctx, c.reg, func(execute func() error) error {
		return runNativeGuard(ctx, c.guard, func() error {
			if !c.current(ctx) || stoppedForRemoval(ctx, c.reg) != nil || !c.current(ctx) {
				return removePlanUnavailable()
			}
			attempted = true
			return execute()
		})
	})
	if attempted {
		observation = c.observeRemoval(ctx, diagnosticCheckpoint)
	}
	if err != nil {
		return removePlanUnavailable() // No private paths, payloads or native errors.
	}
	return nil
}

func (c *removeCapture) current(ctx context.Context) bool {
	if removalContextError(ctx) != nil || !time.Now().Before(c.expires) {
		return false
	}
	binding, err := c.manager.workspace.AgentWorkspace(ctx, c.reg.Workspace.Path)
	if err != nil || binding != c.reg.Workspace {
		return false
	}
	files, _, err := captureStopFiles(c.reg.ID)
	if err != nil || !c.files.same(files) {
		return false
	}
	_, schedule, err := removalSchedules(c.reg)
	return err == nil && sameStopLeaf(c.schedule, schedule) && removalContextError(ctx) == nil && time.Now().Before(c.expires)
}

// Native remove and exact admission use the same independently grounded stopped
// check. A cached snapshot or missing listener cannot override unknown/live owner
// evidence. These remain point-in-time checks, not OS peer/PID-incarnation
// attestation or an atomic transaction with external filesystem/process changes.
func stoppedForRemoval(ctx context.Context, reg Registration) error {
	if err := removalContextError(ctx); err != nil {
		return err
	}
	observed, err := observeRunner(ctx, reg)
	if err != nil {
		return err
	}
	alive, err := ownerAlive(reg)
	if err != nil {
		return err
	}
	if reg.Desired || observed.Running || alive {
		return fmt.Errorf("stop the agent before removing its registration")
	}
	return removalContextError(ctx)
}

// Caller holds manager serialization. The optional internal admission wraps the
// entire existing effect sequence; no second schedule or registration executor.
func removeWithAdmission(ctx context.Context, reg Registration, admit func(func() error) error) error {
	if err := stoppedForRemoval(ctx, reg); err != nil {
		return err
	}
	execute := func() error { return removeRegistration(ctx, reg, pauseSchedules, os.Remove) }
	if admit != nil {
		return admit(execute)
	}
	return execute()
}

// Parameters permit deterministic fault/order tests of the real shared effect
// sequence, without global hooks or replaceable executors in retained plans.
// Cancellation is cooperative at each effect boundary, not rollback: a completed
// pause remains persisted if cancellation prevents the later registration unlink.
// Check absolute expiry as well as Err; timer notification is not admission.
func removeRegistration(ctx context.Context, reg Registration, pause func(Registration) error, unlink func(string) error) error {
	if err := removalContextError(ctx); err != nil {
		return err
	}
	if err := pause(reg); err != nil {
		return err
	}
	dir, err := instanceDir(reg.ID)
	if err != nil {
		return err
	}
	if err := removalContextError(ctx); err != nil {
		return err
	}
	if err := unlink(filepath.Join(dir, "registration.json")); err != nil {
		return err
	}
	// Cancellation during unlink can follow BOTH effects. Do not report ordinary
	// native remove as successful, or restore schedules/registration/admission.
	return removalContextError(ctx)
}

// Native removal's bounded stable no-follow read and existing decoder. Missing
// schedules keep their existing empty-state semantics. Reject malformed/unsafe
// state without rewriting it; retained plans keep no payload or history copy.
func removalSchedules(reg Registration) (scheduleState, stopFile, error) {
	data, info, err := diagnosticCheckpoint(schedulePath(reg))
	if errors.Is(err, os.ErrNotExist) {
		return scheduleState{Version: 1}, stopFile{}, nil
	}
	if err != nil {
		return scheduleState{}, stopFile{}, err
	}
	state, err := decodeSchedules(data)
	return state, stopFile{info: info, hash: sha256.Sum256(data)}, err
}
