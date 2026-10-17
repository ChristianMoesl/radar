// Package agentservice owns durable-agent process and transport infrastructure.
// It has no knowledge of any agent's domain, tools, or conversation routing.
package agentservice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
}

type retryState struct {
	attempts int
	next     time.Time
	problem  string
}
type Manager struct {
	mu        sync.Mutex
	retry     map[string]retryState
	workspace func(context.Context, string) (protocol.AgentWorkspaceBinding, error)
}

func New(workspace func(context.Context, string) (protocol.AgentWorkspaceBinding, error)) *Manager {
	return &Manager{retry: map[string]retryState{}, workspace: workspace}
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
	return filepath.Join(filepath.Dir(base), "agent-"+hex.EncodeToString(sum[:8])+".sock"), nil
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
	var reg Registration
	if err := json.Unmarshal(data, &reg); err != nil {
		return reg, err
	}
	if reg.Version != 1 || reg.ID != id || reg.Workspace.ID == "" || reg.Workspace.Path == "" {
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

func hostCommand(entrypoint string) (string, []string, error) {
	if explicit := os.Getenv("RADAR_AGENT_HOST"); explicit != "" {
		node, err := exec.LookPath("node")
		return node, []string{explicit}, err
	}
	for dir := filepath.Dir(entrypoint); ; dir = filepath.Dir(dir) {
		host := filepath.Join(dir, "node_modules", ".bin", "radar-agent-host")
		if info, err := os.Stat(host); err == nil && info.Mode().IsRegular() {
			return host, nil, nil
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return "", nil, fmt.Errorf("agent host missing; install @christianmoesl/radar-agent-host in the definition's project (RADAR_AGENT_HOST selects a development runner)")
}

func status(ctx context.Context, reg Registration) protocol.AgentStatus {
	dir, _ := instanceDir(reg.ID)
	result := protocol.AgentStatus{ID: reg.ID, Entrypoint: reg.Entrypoint, DataDir: dir, Desired: reg.Desired, Workspace: reg.Workspace}
	path, err := socketPath(reg.ID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	var hello struct {
		Protocol   int    `json:"protocol"`
		InstanceID string `json:"instanceId"`
	}
	if err = call(ctx, path, "hello", nil, &hello); err != nil {
		if data, readErr := os.ReadFile(filepath.Join(dir, "snapshot.json")); readErr == nil {
			var snapshot protocol.AgentSnapshot
			if json.Unmarshal(data, &snapshot) == nil {
				result.Snapshot = &snapshot
			}
		}
		if reg.Desired {
			result.Error = "runner unavailable"
		}
		return result
	}
	if hello.Protocol != protocol.AgentProtocolVersion || hello.InstanceID != reg.ID {
		result.Error = "agent protocol/identity mismatch"
		return result
	}
	var snapshot protocol.AgentSnapshot
	if err = call(ctx, path, "snapshot", nil, &snapshot); err != nil {
		result.Error = err.Error()
		return result
	}
	result.Running = true
	result.Snapshot = &snapshot
	return result
}

func (m *Manager) status(ctx context.Context, reg Registration) protocol.AgentStatus {
	result := status(ctx, reg)
	binding, err := m.workspace(ctx, reg.Workspace.Path)
	if err != nil {
		result.Error = "workspace unavailable: " + err.Error()
	} else if binding.ID != reg.Workspace.ID {
		result.Error = "associated workspace identity changed"
	} else {
		result.Workspace = binding
	}
	return result
}

func (m *Manager) start(ctx context.Context, reg Registration) error {
	binding, err := m.workspace(ctx, reg.Workspace.Path)
	if err != nil {
		return err
	}
	if binding.ID != reg.Workspace.ID {
		return fmt.Errorf("associated workspace identity changed; refusing to recover agent work")
	}
	reg.Workspace = binding
	dir, _ := instanceDir(reg.ID)
	if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
		return err
	}
	if s := status(ctx, reg); s.Running {
		return nil
	} else if strings.Contains(s.Error, "mismatch") {
		return errors.New(s.Error)
	}
	if ownerAlive(reg) {
		return fmt.Errorf("runner process exists but is not ready; inspect its logs before replacement")
	}
	command, args, err := hostCommand(reg.Entrypoint)
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

func stop(ctx context.Context, reg Registration) error {
	path, err := socketPath(reg.ID)
	if err != nil {
		return err
	}
	s := status(ctx, reg)
	if !s.Running {
		if ownerAlive(reg) {
			return fmt.Errorf("runner is unreachable but its process remains alive; inspect logs before removing its registration")
		}
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
			if !runnerProcessAlive(pid) {
				return nil
			}
		case <-deadline.C:
			return fmt.Errorf("runner did not stop; refusing a second storage owner")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
		release, err := operationlock.Acquire(false)
		if err != nil {
			return nil, err
		}
		defer release()
		if request.Workspace == "" {
			return nil, fmt.Errorf("agent add requires --workspace naming a registered Radar workspace")
		}
		binding, err := m.workspace(ctx, request.Workspace)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(binding.Path, dir)
		if err != nil {
			return nil, err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return nil, fmt.Errorf("durable agent data must be outside its disposable workspace")
		}
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
		config := request.Config
		if len(config) == 0 {
			config = json.RawMessage(`{}`)
		}
		var values map[string]any
		if err := json.Unmarshal(config, &values); err != nil || values == nil {
			return nil, fmt.Errorf("config must be a JSON object")
		}
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
		reg := Registration{Version: 1, ID: request.ID, Entrypoint: entry, Workspace: binding}
		if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
			return nil, err
		}
		return encode(m.status(ctx, reg))
	}
	reg, err := load(request.ID)
	if err != nil {
		return nil, err
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
		if reg.Desired || status(ctx, reg).Running || ownerAlive(reg) {
			return nil, fmt.Errorf("stop the agent before removing its registration")
		}
		if err := os.Remove(filepath.Join(dir, "registration.json")); err != nil {
			return nil, err
		}
		return encode(map[string]string{"id": reg.ID, "data_dir": dir, "status": "registration removed; durable data retained"})
	case "start", "stop", "restart":
		reg.Desired = request.Operation != "stop"
		if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
			return nil, err
		}
		delete(m.retry, reg.ID)
		if request.Operation != "start" {
			if err := stop(ctx, reg); err != nil {
				return nil, err
			}
		}
		if reg.Desired {
			if err := m.start(ctx, reg); err != nil {
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

func ownerAlive(reg Registration) bool {
	dir, err := instanceDir(reg.ID)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	return runnerProcessAlive(pid)
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
				if reg.Desired && !s.Running {
					r := m.retry[reg.ID]
					if !time.Now().Before(r.next) {
						if err := m.start(ctx, reg); err != nil {
							r.attempts++
							r.problem = err.Error()
							r.next = time.Now().Add(time.Duration(1<<min(r.attempts, 6)) * time.Second)
							m.retry[reg.ID] = r
						} else {
							delete(m.retry, reg.ID)
							s = status(ctx, reg)
						}
					}
				}
				if s.Snapshot != nil && s.Running {
					dir, _ := instanceDir(reg.ID)
					_ = atomicJSON(filepath.Join(dir, "snapshot.json"), s.Snapshot)
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
