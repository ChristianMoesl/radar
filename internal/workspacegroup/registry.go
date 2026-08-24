package workspacegroup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"radar/internal/tmuxlayout"
)

const Version = 2
const FileName = ".radar-workspaces.json"
const lockFileName = ".radar-workspaces.lock"

type Registry struct {
	Version    int         `json:"version"`
	Workspaces []Workspace `json:"workspaces"`
}

type Workspace struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Path           string            `json:"path"`
	SessionName    string            `json:"session_name,omitempty"`
	TaskLinkingKey string            `json:"task_linking_key,omitempty"`
	NotePath       string            `json:"note_path,omitempty"`
	Model          string            `json:"model,omitempty"`
	Thinking       string            `json:"thinking,omitempty"`
	Tmux           tmuxlayout.Config `json:"tmux"`
	Sandbox        *Sandbox          `json:"sandbox,omitempty"`
	Members        []Member          `json:"members"`
}

type Sandbox struct {
	Name             string         `json:"name"`
	Agent            string         `json:"agent"`
	KitPath          string         `json:"kit_path,omitempty"`
	Mounts           []string       `json:"mounts"`
	AdditionalMounts []SandboxMount `json:"additional_mounts"`
	Ports            []SandboxPort  `json:"ports"`
}

type SandboxMount struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
}

type SandboxPort struct {
	HostPort    int `json:"host_port"`
	SandboxPort int `json:"sandbox_port"`
}

type Member struct {
	Repository     string `json:"repository"`
	Path           string `json:"path"`
	Branch         string `json:"branch"`
	SetupScheduled bool   `json:"setup_scheduled"`
}

var registryMu sync.Mutex

func Path(root string) string { return filepath.Join(filepath.Clean(root), FileName) }

func ID(workspacePath string) string {
	sum := sha256.Sum256([]byte(cleanPath(workspacePath)))
	return hex.EncodeToString(sum[:16])
}

func Load(root string) (Registry, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	var registry Registry
	err := withRegistryLock(root, false, func() error {
		var err error
		registry, err = load(root)
		return err
	})
	return registry, err
}

func load(root string) (Registry, error) {
	data, err := os.ReadFile(Path(root))
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Version: Version, Workspaces: []Workspace{}}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	var registry Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return Registry{}, fmt.Errorf("read %s: %w", Path(root), err)
	}
	if registry.Version != Version {
		return Registry{}, fmt.Errorf("unsupported Radar workspace registry version %d", registry.Version)
	}
	if err := normalizeAndValidate(&registry); err != nil {
		return Registry{}, fmt.Errorf("read %s: %w", Path(root), err)
	}
	return registry, nil
}

func Save(root string, registry Registry) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	return withRegistryLock(root, true, func() error { return save(root, registry) })
}

func Update(root string, update func(*Registry) error) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	return withRegistryLock(root, true, func() error {
		registry, err := load(root)
		if err != nil {
			return err
		}
		if err := update(&registry); err != nil {
			return err
		}
		return save(root, registry)
	})
}

func withRegistryLock(root string, exclusive bool, run func() error) error {
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	operation := unix.LOCK_SH
	if exclusive {
		operation = unix.LOCK_EX
	}
	if err := unix.Flock(int(lock.Fd()), operation); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	return run()
}

func save(root string, registry Registry) error {
	if registry.Version == 0 {
		registry.Version = Version
	}
	if registry.Version != Version {
		return fmt.Errorf("unsupported Radar workspace registry version %d", registry.Version)
	}
	if err := normalizeAndValidate(&registry); err != nil {
		return err
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := Path(root)
	tmp, err := os.CreateTemp(root, ".radar-workspaces-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// FindByContainingPath resolves an anchor, note link, member, or any nested
// path to its registered logical workspace. The most specific anchor wins.
func FindByContainingPath(registry Registry, path string) (Workspace, bool) {
	path = cleanPath(path)
	var found Workspace
	foundLength := -1
	for _, workspace := range registry.Workspaces {
		if sameOrDescendant(path, workspace.Path) && len(workspace.Path) > foundLength {
			found = workspace
			foundLength = len(workspace.Path)
			continue
		}
		for _, member := range workspace.Members {
			if sameOrDescendant(path, member.Path) && len(member.Path) > foundLength {
				found = workspace
				foundLength = len(member.Path)
			}
		}
	}
	return found, foundLength >= 0
}

func FindByMemberPath(registry Registry, path string) (Workspace, bool) {
	path = cleanPath(path)
	for _, workspace := range registry.Workspaces {
		for _, member := range workspace.Members {
			if samePath(member.Path, path) {
				return workspace, true
			}
		}
	}
	return Workspace{}, false
}

func FindByTaskLinkingKey(registry Registry, key string) (Workspace, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Workspace{}, false
	}
	for _, workspace := range registry.Workspaces {
		if workspace.TaskLinkingKey == key {
			return workspace, true
		}
	}
	return Workspace{}, false
}

func FindMemberByPath(registry Registry, path string) (Member, bool) {
	path = cleanPath(path)
	for _, workspace := range registry.Workspaces {
		for _, member := range workspace.Members {
			if samePath(member.Path, path) {
				return member, true
			}
		}
	}
	return Member{}, false
}

func FindByID(registry Registry, id string) (Workspace, bool) {
	for _, workspace := range registry.Workspaces {
		if workspace.ID == id {
			return workspace, true
		}
	}
	return Workspace{}, false
}

func Put(registry *Registry, workspace Workspace) {
	for index := range registry.Workspaces {
		if registry.Workspaces[index].ID == workspace.ID {
			registry.Workspaces[index] = workspace
			return
		}
	}
	registry.Workspaces = append(registry.Workspaces, workspace)
}

func RemoveMember(root string, memberPath string) error {
	return Update(root, func(registry *Registry) error {
		memberPath = cleanPath(memberPath)
		for workspaceIndex := range registry.Workspaces {
			workspace := &registry.Workspaces[workspaceIndex]
			members := make([]Member, 0, len(workspace.Members))
			for _, member := range workspace.Members {
				if !samePath(member.Path, memberPath) {
					members = append(members, member)
				}
			}
			workspace.Members = members
		}
		return nil
	})
}

func RemoveWorkspace(root, id string) error {
	return Update(root, func(registry *Registry) error {
		for index, workspace := range registry.Workspaces {
			if workspace.ID == id {
				registry.Workspaces = append(registry.Workspaces[:index], registry.Workspaces[index+1:]...)
				break
			}
		}
		return nil
	})
}

func normalizeAndValidate(registry *Registry) error {
	ids := map[string]bool{}
	paths := map[string]bool{}
	memberIdentities := map[string]bool{}
	taskLinks := map[string]bool{}
	for workspaceIndex := range registry.Workspaces {
		workspace := &registry.Workspaces[workspaceIndex]
		workspace.ID = strings.TrimSpace(workspace.ID)
		workspace.Name = strings.TrimSpace(workspace.Name)
		workspace.Path = cleanPath(workspace.Path)
		workspace.SessionName = strings.TrimSpace(workspace.SessionName)
		workspace.TaskLinkingKey = strings.TrimSpace(workspace.TaskLinkingKey)
		workspace.NotePath = cleanOptionalPath(workspace.NotePath)
		workspace.Model = strings.TrimSpace(workspace.Model)
		workspace.Thinking = strings.TrimSpace(workspace.Thinking)
		if workspace.TaskLinkingKey != "" {
			prefix, _, found := strings.Cut(workspace.TaskLinkingKey, ":")
			if !found || strings.TrimSpace(prefix) == "" {
				return fmt.Errorf("workspace %q task_linking_key requires a non-empty prefix", workspace.ID)
			}
			if taskLinks[workspace.TaskLinkingKey] {
				return fmt.Errorf("duplicate workspace task_linking_key %q", workspace.TaskLinkingKey)
			}
			taskLinks[workspace.TaskLinkingKey] = true
		}
		if workspace.ID == "" || workspace.Name == "" || !filepath.IsAbs(workspace.Path) {
			return fmt.Errorf("workspace id, name, and absolute path are required")
		}
		if workspace.ID != ID(workspace.Path) {
			return fmt.Errorf("workspace %q id does not match anchor path %s", workspace.ID, workspace.Path)
		}
		if workspace.NotePath != "" && !filepath.IsAbs(workspace.NotePath) {
			return fmt.Errorf("workspace %q note_path must be absolute", workspace.ID)
		}
		if ids[workspace.ID] {
			return fmt.Errorf("duplicate workspace id %q", workspace.ID)
		}
		if paths[pathKey(workspace.Path)] {
			return fmt.Errorf("duplicate workspace path %q", workspace.Path)
		}
		ids[workspace.ID] = true
		paths[pathKey(workspace.Path)] = true
		for memberIndex := range workspace.Members {
			member := &workspace.Members[memberIndex]
			member.Repository = cleanPath(member.Repository)
			member.Path = cleanPath(member.Path)
			member.Branch = strings.TrimSpace(member.Branch)
			if !filepath.IsAbs(member.Repository) || !filepath.IsAbs(member.Path) || member.Branch == "" {
				return fmt.Errorf("workspace %q members require absolute repository and path plus branch", workspace.ID)
			}
			if !samePath(filepath.Dir(member.Path), workspace.Path) {
				return fmt.Errorf("workspace %q member %q must be a direct child of its anchor", workspace.ID, member.Path)
			}
			if strings.EqualFold(filepath.Base(member.Path), "note.md") {
				return fmt.Errorf("workspace %q member path uses reserved name note.md", workspace.ID)
			}
			key := pathKey(member.Path)
			if paths[key] {
				return fmt.Errorf("duplicate workspace member path %q", member.Path)
			}
			paths[key] = true
			identity := pathKey(member.Repository) + "\x00" + member.Branch
			if memberIdentities[identity] {
				return fmt.Errorf("duplicate workspace member repository %q and branch %q", member.Repository, member.Branch)
			}
			memberIdentities[identity] = true
		}
		if workspace.Sandbox != nil {
			workspace.Sandbox.Name = strings.TrimSpace(workspace.Sandbox.Name)
			workspace.Sandbox.Agent = strings.TrimSpace(workspace.Sandbox.Agent)
			workspace.Sandbox.KitPath = cleanOptionalPath(workspace.Sandbox.KitPath)
			if workspace.Sandbox.Name == "" || workspace.Sandbox.Agent == "" {
				return fmt.Errorf("workspace %q sandbox name and agent are required", workspace.ID)
			}
			if workspace.Sandbox.KitPath != "" && !filepath.IsAbs(workspace.Sandbox.KitPath) {
				return fmt.Errorf("workspace %q sandbox kit_path must be absolute", workspace.ID)
			}
			for _, mount := range workspace.Sandbox.Mounts {
				path := strings.TrimSuffix(strings.TrimSpace(mount), ":ro")
				if path == "" || !filepath.IsAbs(path) {
					return fmt.Errorf("workspace %q sandbox mounts must be absolute", workspace.ID)
				}
			}
			workspace.Sandbox.Mounts = normalizeMounts(workspace.Sandbox.Mounts)
			additionalMounts := make([]SandboxMount, 0, len(workspace.Sandbox.AdditionalMounts))
			additionalMountPaths := map[string]bool{}
			for _, mount := range workspace.Sandbox.AdditionalMounts {
				mount.Path = cleanPath(mount.Path)
				if !filepath.IsAbs(mount.Path) {
					return fmt.Errorf("workspace %q sandbox additional mounts must be absolute", workspace.ID)
				}
				key := pathKey(mount.Path)
				if additionalMountPaths[key] {
					return fmt.Errorf("workspace %q has duplicate sandbox additional mount %q", workspace.ID, mount.Path)
				}
				additionalMountPaths[key] = true
				additionalMounts = append(additionalMounts, mount)
			}
			sort.Slice(additionalMounts, func(i, j int) bool { return pathKey(additionalMounts[i].Path) < pathKey(additionalMounts[j].Path) })
			if additionalMounts == nil {
				additionalMounts = []SandboxMount{}
			}
			workspace.Sandbox.AdditionalMounts = additionalMounts
			ports := make([]SandboxPort, 0, len(workspace.Sandbox.Ports))
			hostPorts := map[int]bool{}
			for _, port := range workspace.Sandbox.Ports {
				if port.HostPort < 1 || port.HostPort > 65535 || port.SandboxPort < 1 || port.SandboxPort > 65535 {
					return fmt.Errorf("workspace %q sandbox ports must be between 1 and 65535", workspace.ID)
				}
				if hostPorts[port.HostPort] {
					return fmt.Errorf("workspace %q has duplicate sandbox host port %d", workspace.ID, port.HostPort)
				}
				hostPorts[port.HostPort] = true
				ports = append(ports, port)
			}
			sort.Slice(ports, func(i, j int) bool {
				if ports[i].HostPort != ports[j].HostPort {
					return ports[i].HostPort < ports[j].HostPort
				}
				return ports[i].SandboxPort < ports[j].SandboxPort
			})
			if ports == nil {
				ports = []SandboxPort{}
			}
			workspace.Sandbox.Ports = ports
		}
		sort.Slice(workspace.Members, func(i, j int) bool { return pathKey(workspace.Members[i].Path) < pathKey(workspace.Members[j].Path) })
		if workspace.Members == nil {
			workspace.Members = []Member{}
		}
	}
	sort.Slice(registry.Workspaces, func(i, j int) bool { return registry.Workspaces[i].ID < registry.Workspaces[j].ID })
	if registry.Workspaces == nil {
		registry.Workspaces = []Workspace{}
	}
	return nil
}

func normalizeMounts(mounts []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		mount = strings.TrimSpace(mount)
		if mount == "" {
			continue
		}
		suffix := ""
		path := mount
		if strings.HasSuffix(path, ":ro") {
			path = strings.TrimSuffix(path, ":ro")
			suffix = ":ro"
		}
		path = cleanPath(path)
		if !filepath.IsAbs(path) {
			continue
		}
		key := pathKey(path + suffix)
		if !seen[key] {
			seen[key] = true
			result = append(result, path+suffix)
		}
	}
	sort.Strings(result)
	return result
}

func cleanOptionalPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return cleanPath(path)
}

func cleanPath(path string) string     { return filepath.Clean(strings.TrimSpace(path)) }
func samePath(left, right string) bool { return pathKey(left) == pathKey(right) }
func sameOrDescendant(path, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	path = cleanPath(path)
	root = cleanPath(root)
	if samePath(path, root) {
		return true
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
func pathKey(path string) string {
	path = cleanPath(path)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
