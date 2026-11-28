package agentservice

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// StopPlan is a private, bounded, exact-registration native admission, not a
// transport contract or consent. Copies share consumption. The typed caller must
// additionally bind its authorized scope, publication and fresh user approval in
// its existing one-use plan machinery; no native cache or client token is added.
// Unexported fields deliberately serialize as {} and retain no native snapshot.
// This boundary does not enable diagnostic/CLI/Pi controls by itself.
type StopPlan struct{ capture *stopCapture }

type stopCapture struct {
	mu       sync.Mutex
	consumed bool
	manager  *Manager
	guard    integration.AgentStopGuard
	expires  time.Time
	files    stopFiles
	reg      Registration
	running  bool
	pid      int
	bytes    int
}

type stopFile struct {
	info os.FileInfo
	hash [32]byte
}

type stopFiles struct {
	rootPath, socketPath string
	root, instance       os.FileInfo
	registration, owner  stopFile
	socket               os.FileInfo
}

func stopPlanUnavailable() error {
	return errors.New("exact native agent stop unavailable or changed; outcome may be unknown, do not replay or automatically retry")
}

func (p StopPlan) Preview() integration.AgentStopPreview {
	if p.capture == nil {
		return integration.AgentStopPreview{}
	}
	c := p.capture
	return integration.AgentStopPreview{ID: c.reg.ID, Desired: c.reg.Desired, Running: c.running}
}

func (p StopPlan) ExpiresAt() time.Time {
	if p.capture == nil {
		return time.Time{}
	}
	return p.capture.expires
}

func (p StopPlan) RetainedBytes() int {
	if p.capture == nil {
		return 0
	}
	return p.capture.bytes
}

// Budget the retained strings (including registration-only private fields),
// allocation overhead and fixed file/identity metadata conservatively. Neither
// checkpoint bytes nor a snapshot is retained. The shared manager and trusted
// caller guard graph are not copied here; the adapter budgets its own closure.
func stopCaptureBytes(reg Registration, files stopFiles) int {
	return 64*1024 + 4*(len(reg.ID)+len(reg.Entrypoint)+len(reg.Collection)+
		len(reg.Workspace.ID)+len(reg.Workspace.Path)+len(reg.Workspace.Mode)+len(reg.Workspace.Sandbox)+
		len(files.rootPath)+len(files.socketPath))
}

// PrepareStop captures exactly one caller-selected registration and associated
// workspace. It never prepares a workspace, changes desired state, reads private
// checkpoints/tokens, acquires a workspace lease or dispatches shutdown. Busy,
// cancelled and unknown evidence refuse. The non-nil trusted caller guard is
// retained, never invoked by capture, and cannot be replaced at apply. Native I/O
// uses a cooperative 5s bound.
func (m *Manager) PrepareStop(ctx context.Context, id string, expected protocol.AgentWorkspaceBinding, guard integration.AgentStopGuard) (StopPlan, error) {
	if guard == nil || ctx.Err() != nil || !validID.MatchString(id) || expected.ID == "" || !filepath.IsAbs(expected.Path) || filepath.Clean(expected.Path) != expected.Path || m.workspace == nil || !m.mu.TryLock() {
		return StopPlan{}, stopPlanUnavailable()
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	files, reg, err := captureStopFiles(id)
	if err != nil || reg.Workspace != expected {
		return StopPlan{}, stopPlanUnavailable()
	}
	binding, err := m.workspace.AgentWorkspace(ctx, expected.Path)
	if err != nil || binding != expected {
		return StopPlan{}, stopPlanUnavailable()
	}
	observed, err := observeRunner(ctx, reg)
	if err != nil {
		return StopPlan{}, stopPlanUnavailable()
	}
	alive, err := ownerAlive(reg)
	if err != nil || (!observed.Running && alive) || ctx.Err() != nil {
		return StopPlan{}, stopPlanUnavailable()
	}
	after, _, err := captureStopFiles(id)
	if err != nil || !files.same(after) || ctx.Err() != nil {
		return StopPlan{}, stopPlanUnavailable()
	}
	c := &stopCapture{manager: m, guard: guard, expires: time.Now().Add(30 * time.Second), files: files, reg: reg, running: observed.Running, bytes: stopCaptureBytes(reg, files)}
	if observed.Running {
		c.pid = observed.Snapshot.PID
	}
	return StopPlan{capture: c}, nil
}

// Execute consumes this admission BEFORE cancellation, busy/drift checks or
// effects. It reuses native stop's final observation and owner guards, never a
// second shutdown executor. Refusal/lost replies do not restore admission.
// A nil error means native stop returned, not completed agent work, released
// leases or workspace/sandbox absence. Desired-state writes are not rolled back
// on later shutdown failure/cancellation/expiry. Stopped registrations retain
// ownership. The original monotonic expiry also bounds native execution, and
// cancellation/expiry observed after effects cannot certify a successful return.
func (p StopPlan) Execute(ctx context.Context) (result error) {
	if p.capture == nil {
		return stopPlanUnavailable()
	}
	c := p.capture
	c.mu.Lock()
	if c.consumed {
		c.mu.Unlock()
		return stopPlanUnavailable()
	}
	c.consumed = true
	c.mu.Unlock()
	m := c.manager
	if ctx.Err() != nil || !time.Now().Before(c.expires) || !m.mu.TryLock() {
		return stopPlanUnavailable()
	}
	defer m.mu.Unlock()
	deadline := time.Now().Add(15 * time.Second)
	if c.expires.Before(deadline) {
		deadline = c.expires
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	// Normalize BEFORE our own deferred cancel. Already accepted desired-state
	// and shutdown effects remain possible; no rollback or retry is implied.
	defer func() {
		if ctx.Err() != nil || !time.Now().Before(c.expires) {
			result = stopPlanUnavailable()
		}
	}()
	// Reject filesystem/scope drift before contacting a possibly replaced peer.
	if !c.current(ctx) {
		return stopPlanUnavailable()
	}
	err := stopWithAdmission(ctx, c.reg, func(observed protocol.AgentStatus, nativeStop func() error) error {
		if observed.Running != c.running || (observed.Running && observed.Snapshot.PID != c.pid) || !c.current(ctx) {
			return stopPlanUnavailable()
		}
		// Scope/publication admission must cover BOTH the first desired-state
		// write and the original native shutdown, not just checks before I/O.
		return runNativeGuard(ctx, c.guard, func() error {
			if !c.current(ctx) {
				return stopPlanUnavailable()
			}
			if _, err := m.setDesired(c.reg, false); err != nil {
				return err
			}
			return nativeStop()
		})
	})
	if err != nil {
		return stopPlanUnavailable() // Never disclose native errors/private paths.
	}
	return nil
}

// Close the execution callback before releasing manager serialization. A guard
// cannot retain it for later, execute twice, skip it and claim success, or hide a
// native error. The mutex also drains an already-entered callback before return.
// Shared by private stop and removal admission; callers sanitize these internal
// enforcement errors to their own operation-specific refusal. Panic unwinding
// closes and drains too; a recovered effect panic cannot become success.
func runNativeGuard(ctx context.Context, guard func(context.Context, func() error) error, execute func() error) error {
	if guard == nil {
		return stopPlanUnavailable()
	}
	var mu sync.Mutex
	active, calls := true, 0
	var result error
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		active = false
	}()
	guardErr := guard(ctx, func() error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if !active || calls != 1 {
			return stopPlanUnavailable()
		}
		// Only a normal native return can establish completion. If the guard
		// recovers an effect panic, retain failure rather than the zero nil.
		result = stopPlanUnavailable()
		result = execute()
		return result
	})
	mu.Lock()
	defer mu.Unlock()
	active = false
	if calls != 1 || guardErr != nil || result != nil || ctx.Err() != nil {
		return stopPlanUnavailable()
	}
	return nil
}

func (c *stopCapture) current(ctx context.Context) bool {
	if ctx.Err() != nil || !time.Now().Before(c.expires) {
		return false
	}
	binding, err := c.manager.workspace.AgentWorkspace(ctx, c.reg.Workspace.Path)
	if err != nil || binding != c.reg.Workspace {
		return false
	}
	files, _, err := captureStopFiles(c.reg.ID)
	return err == nil && c.files.same(files) && ctx.Err() == nil && time.Now().Before(c.expires)
}

// Reuse the no-follow, nonblocking, stable bounded checkpoint reader for private
// registration/owner evidence. Root/instance/leaf and socket identity are retained
// across calls. These are point-in-time checks, not OS peer/PID-incarnation
// attestation, an atomic filesystem transaction or validation of all ancestors.
func captureStopFiles(id string) (stopFiles, Registration, error) {
	var f stopFiles
	var reg Registration
	dir, err := instanceDir(id)
	if err != nil {
		return f, reg, stopPlanUnavailable()
	}
	f.rootPath = filepath.Dir(dir)
	f.socketPath, err = socketPath(id)
	if err != nil {
		return f, reg, stopPlanUnavailable()
	}
	f.root, err = os.Lstat(f.rootPath)
	if err != nil || !f.root.IsDir() {
		return f, reg, stopPlanUnavailable()
	}
	f.instance, err = os.Lstat(dir)
	if err != nil || !f.instance.IsDir() {
		return f, reg, stopPlanUnavailable()
	}
	data, info, err := diagnosticCheckpoint(filepath.Join(dir, "registration.json"))
	if err != nil {
		return f, reg, stopPlanUnavailable()
	}
	f.registration = stopFile{info: info, hash: sha256.Sum256(data)}
	reg, err = decodeRegistration(data, id)
	if err != nil {
		return f, reg, stopPlanUnavailable()
	}
	data, info, err = diagnosticCheckpoint(filepath.Join(dir, "pid"))
	if err != nil && !errors.Is(err, os.ErrNotExist) || len(data) > 64 {
		return f, reg, stopPlanUnavailable()
	}
	if err == nil {
		f.owner = stopFile{info: info, hash: sha256.Sum256(data)}
	}
	f.socket, err = os.Lstat(f.socketPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && f.socket.Mode()&os.ModeSocket == 0 {
		return f, reg, stopPlanUnavailable()
	}
	root, rootErr := os.Lstat(f.rootPath)
	instance, instanceErr := os.Lstat(dir)
	if rootErr != nil || instanceErr != nil || !sameStopFile(f.root, root) || !sameStopFile(f.instance, instance) {
		return f, reg, stopPlanUnavailable()
	}
	return f, reg, nil
}

func sameStopFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Mode() == b.Mode()
}

func sameStopLeaf(x, y stopFile) bool {
	return sameStopFile(x.info, y.info) && x.hash == y.hash && (x.info == nil || x.info.Size() == y.info.Size() && x.info.ModTime().Equal(y.info.ModTime()))
}

func (a stopFiles) same(b stopFiles) bool {
	return a.rootPath == b.rootPath && a.socketPath == b.socketPath && sameStopFile(a.root, b.root) && sameStopFile(a.instance, b.instance) && sameStopFile(a.socket, b.socket) && sameStopLeaf(a.registration, b.registration) && sameStopLeaf(a.owner, b.owner)
}
