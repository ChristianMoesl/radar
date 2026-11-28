// Package agentservice owns durable-agent process and transport infrastructure.
// It has no knowledge of any agent's domain, tools, or conversation routing.
package agentservice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"radar/internal/agentruntime"
	"radar/internal/integration"
	"radar/internal/operationlock"
	"radar/internal/process"
	"radar/internal/protocol"
	"radar/internal/socket"
)

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Registration struct {
	Version    int                            `json:"version"`
	ID         string                         `json:"id"`
	Entrypoint string                         `json:"entrypoint"`
	Desired    bool                           `json:"desired"`
	Workspace  protocol.AgentWorkspaceBinding `json:"workspace"`
	Collection string                         `json:"collection,omitempty"`
}

type retryState struct {
	attempts int
	next     time.Time
	problem  string
}
type Manager struct {
	mu             sync.Mutex
	retry          map[string]retryState
	workspace      integration.AgentWorkspaceManager
	now            func() time.Time
	scheduleErrors map[string]string
}

func New(workspace integration.AgentWorkspaceManager) *Manager {
	return &Manager{retry: map[string]retryState{}, workspace: workspace, now: time.Now, scheduleErrors: map[string]string{}}
}

func Root() (string, error) {
	if explicit := os.Getenv("RADAR_AGENT_ROOT"); explicit != "" {
		return filepath.Abs(explicit)
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "radar", "agents"), nil
}

func instanceDir(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("agent ID must contain 1–64 lowercase letters, digits, underscores, or hyphens")
	}
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

func socketPath(id string) (string, error) {
	base, err := socket.Path()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(id))
	// macOS temporary directories already consume much of sockaddr_un's path
	// limit. Keep the same 64-bit identity in a compact private filename.
	return filepath.Join(filepath.Dir(base), base64.RawURLEncoding.EncodeToString(sum[:8])), nil
}

func atomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func load(id string) (Registration, error) {
	dir, err := instanceDir(id)
	if err != nil {
		return Registration{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "registration.json"))
	if err != nil {
		return Registration{}, err
	}
	return decodeRegistration(data, id)
}

func decodeRegistration(data []byte, id string) (Registration, error) {
	// Ownership and native controls must not choose the last duplicate or a
	// case-folded alias, including inside the selected workspace binding. Keep
	// unknown private extension subtrees outside this validation boundary.
	members, err := agentResponseObject(data, "version", "id", "entrypoint", "desired", "workspace", "collection")
	if err != nil {
		return Registration{}, fmt.Errorf("invalid or unsupported registration; preserve agent data")
	}
	if _, err := agentResponseObject(members["workspace"], "id", "path", "mode", "sandbox"); err != nil {
		return Registration{}, fmt.Errorf("invalid or unsupported registration; preserve agent data")
	}
	var reg Registration
	if err := json.Unmarshal(data, &reg); err != nil {
		return reg, err
	}
	if reg.Version != 1 || reg.ID != id || reg.Workspace.ID == "" || reg.Workspace.Path == "" || (reg.Collection != "" && reg.Collection != "github") {
		return reg, fmt.Errorf("invalid or unsupported registration; preserve agent data")
	}
	return reg, nil
}

func registrations() ([]Registration, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []Registration{}, nil
	}
	if err != nil {
		return nil, err
	}
	var result []Registration
	for _, entry := range entries {
		if !entry.IsDir() || !validID.MatchString(entry.Name()) {
			continue
		}
		reg, err := load(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", entry.Name(), err)
		}
		result = append(result, reg)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func Token(id string) (string, error) {
	dir, err := instanceDir(id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "ingress.token"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func hostCommand() (string, []string, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return "", nil, fmt.Errorf("Node.js 24+ is required for durable agents: %w", err)
	}
	if explicit := os.Getenv("RADAR_AGENT_HOST"); explicit != "" {
		return node, []string{explicit}, nil
	}
	root, err := Root()
	if err != nil {
		return "", nil, err
	}
	host, err := agentruntime.Ensure(root)
	return node, []string{host}, err
}

func (m *Manager) status(ctx context.Context, reg Registration) protocol.AgentStatus {
	result := status(ctx, reg)
	binding, err := m.workspace.AgentWorkspace(ctx, reg.Workspace.Path)
	if err != nil {
		result.Error = "workspace unavailable: " + err.Error()
	} else if binding != reg.Workspace {
		result.Error = "associated workspace identity changed"
	} else {
		result.Workspace = binding
	}
	m.collectionStatus(reg, &result)
	m.scheduleStatus(reg, &result)
	return result
}

func (m *Manager) start(ctx context.Context, reg Registration, request protocol.AgentRequest) error {
	persistDesired := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := m.setDesired(reg, reg.Desired)
		return err
	}
	// Runner work can hold a workspace lease. Stop it before repairing its runtime,
	// never underneath an active native tool invocation.
	if binding, err := m.workspace.AgentWorkspace(ctx, reg.Workspace.Path); err == nil {
		if binding != reg.Workspace {
			return fmt.Errorf("associated workspace identity changed; refusing recovery")
		}
		observed, err := observeRunner(ctx, reg)
		if err != nil {
			return err
		}
		if observed.Running {
			return persistDesired(ctx)
		}
	} else if err := stop(ctx, reg); err != nil {
		return err
	}
	if alive, err := ownerAlive(reg); err != nil {
		return err
	} else if alive {
		return fmt.Errorf("runner process exists but is not ready; inspect its logs before replacement")
	}
	dir, _ := instanceDir(reg.ID)
	finishStartup := func(ctx context.Context, binding protocol.AgentWorkspaceBinding) error {
		if binding != reg.Workspace {
			return fmt.Errorf("associated workspace identity changed; refusing to recover agent work")
		}
		reg.Workspace = binding
		if err := persistDesired(ctx); err != nil {
			return err
		}
		observed, err := observeRunner(ctx, reg)
		if err != nil {
			return err
		}
		if observed.Running {
			return nil
		}
		if alive, err := ownerAlive(reg); err != nil {
			return err
		} else if alive {
			return fmt.Errorf("runner process exists but is not ready; inspect its logs before replacement")
		}
		command, args, err := hostCommand()
		if err != nil {
			return err
		}
		path, err := socketPath(reg.ID)
		if err != nil {
			return err
		}
		if err := socket.EnsureDir(path); err != nil {
			return err
		}
		log, err := os.OpenFile(filepath.Join(dir, "runner.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer log.Close()
		args = append(args, "--id", reg.ID, "--entrypoint", reg.Entrypoint, "--data-dir", dir, "--config", filepath.Join(dir, "config.json"), "--socket", path)
		args = append(args, "--workspace", reg.Workspace.Path, "--workspace-id", reg.Workspace.ID)
		cmd := exec.Command(command, args...)
		daemonSocket, _ := socket.Path()
		cmd.Env = append(os.Environ(), "RADAR_SOCKET="+daemonSocket)
		if os.Getenv("RADAR_BINARY") == "" {
			if executable, err := os.Executable(); err == nil {
				cmd.Env = append(cmd.Env, "RADAR_BINARY="+executable)
			}
		}
		cmd.Stdout = log
		cmd.Stderr = log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		finished := make(chan error, 1)
		go func() { finished <- cmd.Wait() }()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(8 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case err := <-finished:
				return fmt.Errorf("runner exited before ready (%v); see %s", err, filepath.Join(dir, "runner.log"))
			case <-ticker.C:
				if s := status(ctx, reg); s.Running {
					return nil
				}
			case <-deadline.C:
				_ = cmd.Process.Kill()
				return fmt.Errorf("runner startup timed out; inspect %s", filepath.Join(dir, "runner.log"))
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				return ctx.Err()
			}
		}
	}
	// Close and drain this continuation before releasing manager serialization,
	// including native errors and panic unwinding. A retained provider callback
	// cannot persist intent or launch later, outside its original admission.
	var completionMu sync.Mutex
	active, completed := true, 0
	var completionErr error
	closeCompletion := func() {
		completionMu.Lock()
		defer completionMu.Unlock()
		active = false
	}
	defer closeCompletion()
	complete := func(ctx context.Context, binding protocol.AgentWorkspaceBinding) error {
		completionMu.Lock()
		defer completionMu.Unlock()
		if !active {
			return fmt.Errorf("agent workspace runner completion closed; refusing late execution")
		}
		completed++
		if completed != 1 {
			completionErr = fmt.Errorf("agent workspace runner completion repeated; refusing replay")
			return completionErr
		}
		// Keep failure sticky if the provider recovers a startup panic. Only a
		// normal return from the original sequence can establish its result.
		completionErr = fmt.Errorf("agent workspace runner completion interrupted; refusing fallback")
		err := finishStartup(ctx, binding)
		completionErr = err
		return err
	}
	binding, _, err := m.workspace.PrepareAgentWorkspace(ctx, integration.AgentWorkspaceRequest{
		ID: reg.ID, DataDir: dir, Path: reg.Workspace.Path, Expected: &reg.Workspace,
		PlanID: request.PlanID, Note: request.PreparedNote, Complete: complete,
	})
	closeCompletion()
	if err != nil {
		return err
	}
	if completionErr != nil {
		return completionErr
	}
	if completed != 1 || binding != reg.Workspace {
		return fmt.Errorf("agent workspace runner completion unavailable or binding changed; refusing fallback")
	}
	return ctx.Err()
}

func stop(ctx context.Context, reg Registration) error {
	return stopWithAdmission(ctx, reg, nil)
}

// A retained exact-stop caller can validate its private capture at the existing
// final native boundary. Ordinary lifecycle calls retain their original path.
func stopWithAdmission(ctx context.Context, reg Registration, admit func(protocol.AgentStatus, func() error) error) error {
	path, err := socketPath(reg.ID)
	if err != nil {
		return err
	}
	s, err := observeRunner(ctx, reg)
	if err != nil {
		return err
	}
	alive, err := ownerAlive(reg)
	if err != nil {
		return err
	}
	if !s.Running && alive {
		return fmt.Errorf("runner is unreachable but its process remains alive; inspect logs before removing its registration")
	}
	execute := func() error {
		if !s.Running {
			return nil
		}
		pid := s.Snapshot.PID
		if err := call(ctx, path, "shutdown", nil, nil); err != nil {
			return err
		}
		deadline := time.NewTimer(6 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				if err := ctx.Err(); err != nil {
					return err
				}
				alive, err := ownerAlive(reg)
				if err != nil {
					return err
				}
				if !runnerProcessAlive(pid) && !alive {
					return nil
				}
			case <-deadline.C:
				return fmt.Errorf("runner did not stop; refusing a second storage owner")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if admit != nil {
		return admit(s, execute)
	}
	return execute()
}

// Shared desired-state persistence. A subsequent shutdown failure is not a
// transaction rollback and must not restore an accepted stop's retry intent.
func (m *Manager) setDesired(reg Registration, desired bool) (Registration, error) {
	reg.Desired = desired
	dir, err := instanceDir(reg.ID)
	if err != nil {
		return reg, err
	}
	if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
		return reg, err
	}
	delete(m.retry, reg.ID)
	return reg, nil
}

func (m *Manager) Handle(ctx context.Context, request protocol.AgentRequest) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	encode := func(value any) (json.RawMessage, error) { return json.Marshal(value) }
	if request.Operation == "list" {
		regs, err := registrations()
		if err != nil {
			return nil, err
		}
		values := make([]protocol.AgentStatus, 0, len(regs))
		for _, reg := range regs {
			s := m.status(ctx, reg)
			if r := m.retry[reg.ID]; s.Error != "" && r.problem != "" {
				s.Error = r.problem
			}
			values = append(values, s)
		}
		return encode(values)
	}
	dir, err := instanceDir(request.ID)
	if err != nil {
		return nil, err
	}
	if request.Operation == "add" {
		if request.Collection != "" && request.Collection != "github" {
			return nil, fmt.Errorf("unsupported collection %q; supported collection: github", request.Collection)
		}
		release, err := operationlock.Acquire(false)
		if err != nil {
			return nil, err
		}
		defer release()
		if _, err := load(request.ID); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("agent already registered")
		}
		entry, err := filepath.Abs(request.Entrypoint)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(entry)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("entrypoint must be a file")
		}
		reg := Registration{Version: 1, ID: request.ID, Entrypoint: entry, Collection: request.Collection}
		// Removed registrations retain pending delivery. Restoring with a
		// different subscription must not strand or reset that checkpoint.
		if _, err := loadCollection(reg); err != nil {
			return nil, err
		}
		if _, err := loadSchedules(reg); err != nil {
			return nil, err
		}
		config := request.Config
		if len(config) == 0 {
			config = json.RawMessage(`{}`)
		}
		var values map[string]any
		if err := json.Unmarshal(config, &values); err != nil || values == nil {
			return nil, fmt.Errorf("config must be a JSON object")
		}
		binding, plan, err := m.workspace.PrepareAgentWorkspace(ctx, integration.AgentWorkspaceRequest{
			ID: request.ID, DataDir: dir, Path: request.Workspace,
			Preview: request.Preview, PlanID: request.PlanID, Note: request.PreparedNote,
		})
		if err != nil {
			return nil, err
		}
		if request.Preview {
			return encode(plan)
		}
		relative, err := filepath.Rel(binding.Path, dir)
		if err != nil {
			return nil, err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return nil, fmt.Errorf("durable agent data must be outside its disposable workspace")
		}
		reg.Workspace = binding
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err := atomicJSON(filepath.Join(dir, "config.json"), values); err != nil {
			return nil, err
		}
		token := make([]byte, 32)
		if _, err := rand.Read(token); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "ingress.token"), []byte(hex.EncodeToString(token)), 0600); err != nil {
			return nil, err
		}
		if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
			return nil, err
		}
		return encode(m.status(ctx, reg))
	}
	reg, err := load(request.ID)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(request.Operation, "schedule.") {
		if err := m.handleSchedule(reg, request); err != nil {
			return nil, err
		}
		delete(m.scheduleErrors, reg.ID)
		return encode(m.status(ctx, reg))
	}
	switch request.Operation {
	case "inspect":
		return encode(m.status(ctx, reg))
	case "logs":
		return encode(map[string]string{"path": filepath.Join(dir, "runner.log")})
	case "token":
		token, err := Token(reg.ID)
		if err != nil {
			return nil, err
		}
		return encode(map[string]string{"token": token})
	case "remove":
		if err := removeWithAdmission(ctx, reg, nil); err != nil {
			return nil, err
		}
		return encode(map[string]string{"id": reg.ID, "data_dir": dir, "status": "registration removed; durable data retained"})
	case "start", "stop", "restart":
		if request.Preview {
			if request.Operation == "stop" {
				return nil, fmt.Errorf("stop has no workspace preparation plan")
			}
			_, plan, err := m.workspace.PrepareAgentWorkspace(ctx, integration.AgentWorkspaceRequest{
				ID: reg.ID, DataDir: dir, Path: reg.Workspace.Path, Expected: &reg.Workspace, Preview: true,
			})
			if err != nil {
				return nil, err
			}
			return encode(plan)
		}
		if request.Operation == "stop" {
			reg, err = m.setDesired(reg, false)
			if err != nil {
				return nil, err
			}
		} else {
			// Keep start intent in memory until native preparation accepts it.
			// Persisting it here would let supervision retry a refused request.
			reg.Desired = true
		}
		if request.Operation != "start" {
			if err := stop(ctx, reg); err != nil {
				return nil, err
			}
		}
		if reg.Desired {
			if err := m.start(ctx, reg, request); err != nil {
				return nil, err
			}
		}
		return encode(m.status(ctx, reg))
	case "emit", "interact", "conversation":
		s := m.status(ctx, reg)
		if !s.Running {
			return nil, fmt.Errorf("agent is unavailable; start it before delivering events")
		}
		if s.Error != "" && request.Operation != "conversation" {
			return nil, errors.New(s.Error)
		}
		method := request.Operation
		if method == "emit" {
			method = "deliver"
		}
		path, _ := socketPath(reg.ID)
		var result json.RawMessage
		if err := call(ctx, path, method, request.Params, &result); err != nil {
			return nil, err
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unknown agent operation %q", request.Operation)
	}
}

func runnerProcessAlive(pid int) bool {
	if pid <= 0 || !process.Running(pid) {
		return false
	}
	// A runner can be adopted by init after its daemon restarts. In containers
	// init may not reap it promptly; a zombie owns no store or workspace lease.
	if data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); err == nil {
		if at := strings.LastIndex(string(data), ") "); at >= 0 {
			fields := strings.Fields(string(data)[at+2:])
			if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
				return false
			}
		}
	}
	return true
}

func (m *Manager) Run(ctx context.Context, changed func()) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	previous := ""
	for {
		m.mu.Lock()
		regs, err := registrations()
		if err == nil {
			for _, reg := range regs {
				s := m.status(ctx, reg)
				if reg.Desired && (!s.Running || s.Error != "") {
					r := m.retry[reg.ID]
					if !time.Now().Before(r.next) {
						if err := m.start(ctx, reg, protocol.AgentRequest{}); err != nil {
							r.attempts++
							r.problem = err.Error()
							r.next = time.Now().Add(time.Duration(1<<min(r.attempts, 6)) * time.Second)
							m.retry[reg.ID] = r
						} else {
							delete(m.retry, reg.ID)
							s = m.status(ctx, reg)
						}
					}
				}
				if s.Snapshot != nil && s.Running {
					dir, _ := instanceDir(reg.ID)
					_ = atomicJSON(filepath.Join(dir, "snapshot.json"), s.Snapshot)
				}
				if reg.Desired && s.Running && s.Error == "" {
					_ = m.flushCollection(ctx, reg)
				}
				if err := m.tickSchedules(ctx, reg, m.now().UTC(), s.Running && s.Error == ""); err != nil {
					m.scheduleErrors[reg.ID] = err.Error()
					slog.Error("agent schedule delivery", "agent", reg.ID, "error", err)
				} else {
					delete(m.scheduleErrors, reg.ID)
				}
			}
		}
		encoded, _ := json.Marshal(regs)
		for _, reg := range regs {
			s := m.status(ctx, reg)
			b, _ := json.Marshal(s)
			encoded = append(encoded, b...)
		}
		current := string(encoded)
		m.mu.Unlock()
		if current != previous {
			previous = current
			if changed != nil {
				changed()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
