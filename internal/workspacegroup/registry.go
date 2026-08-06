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
)

const Version = 1
const FileName = ".radar-workspaces.json"

type Registry struct {
	Version    int         `json:"version"`
	Workspaces []Workspace `json:"workspaces"`
}

type Workspace struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	PrimaryPath string   `json:"primary_path"`
	SessionName string   `json:"session_name,omitempty"`
	Sandbox     *Sandbox `json:"sandbox,omitempty"`
	Members     []Member `json:"members"`
}

type Sandbox struct {
	Name    string   `json:"name"`
	Agent   string   `json:"agent"`
	KitPath string   `json:"kit_path,omitempty"`
	Mounts  []string `json:"mounts"`
}

type Member struct {
	Repository     string `json:"repository"`
	Path           string `json:"path"`
	Branch         string `json:"branch"`
	Primary        bool   `json:"primary,omitempty"`
	SetupScheduled bool   `json:"setup_scheduled"`
}

var registryMu sync.Mutex

func Path(root string) string { return filepath.Join(filepath.Clean(root), FileName) }

func ID(primaryPath string) string {
	sum := sha256.Sum256([]byte(cleanPath(primaryPath)))
	return hex.EncodeToString(sum[:16])
}

func Load(root string) (Registry, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	return load(root)
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
	return save(root, registry)
}

func Update(root string, update func(*Registry) error) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry, err := load(root)
	if err != nil {
		return err
	}
	if err := update(&registry); err != nil {
		return err
	}
	return save(root, registry)
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
	registry, err := Load(root)
	if err != nil {
		return err
	}
	if _, found := FindByMemberPath(registry, memberPath); !found {
		return nil
	}
	return Update(root, func(registry *Registry) error {
		memberPath = cleanPath(memberPath)
		for workspaceIndex := 0; workspaceIndex < len(registry.Workspaces); workspaceIndex++ {
			workspace := &registry.Workspaces[workspaceIndex]
			matched := false
			removedPrimary := false
			members := make([]Member, 0, len(workspace.Members))
			for _, member := range workspace.Members {
				if samePath(member.Path, memberPath) {
					matched = true
					removedPrimary = member.Primary
					continue
				}
				members = append(members, member)
			}
			if !matched {
				continue
			}
			if removedPrimary && len(members) > 0 {
				// Keep the primary metadata until the remaining members have also
				// been cleaned; the registry invariant requires one primary.
				return nil
			}
			alive := members[:0]
			for _, member := range members {
				if _, statErr := os.Stat(member.Path); statErr == nil || !os.IsNotExist(statErr) {
					alive = append(alive, member)
				}
			}
			workspace.Members = alive
			if len(alive) == 0 {
				registry.Workspaces = append(registry.Workspaces[:workspaceIndex], registry.Workspaces[workspaceIndex+1:]...)
				return nil
			}
			return nil
		}
		return nil
	})
}

func normalizeAndValidate(registry *Registry) error {
	ids := map[string]bool{}
	paths := map[string]bool{}
	for workspaceIndex := range registry.Workspaces {
		workspace := &registry.Workspaces[workspaceIndex]
		workspace.ID = strings.TrimSpace(workspace.ID)
		workspace.Name = strings.TrimSpace(workspace.Name)
		workspace.PrimaryPath = cleanPath(workspace.PrimaryPath)
		workspace.SessionName = strings.TrimSpace(workspace.SessionName)
		if workspace.ID == "" || workspace.Name == "" || !filepath.IsAbs(workspace.PrimaryPath) {
			return fmt.Errorf("workspace id, name, and absolute primary_path are required")
		}
		if ids[workspace.ID] {
			return fmt.Errorf("duplicate workspace id %q", workspace.ID)
		}
		ids[workspace.ID] = true
		primaryCount := 0
		for memberIndex := range workspace.Members {
			member := &workspace.Members[memberIndex]
			member.Repository = cleanPath(member.Repository)
			member.Path = cleanPath(member.Path)
			member.Branch = strings.TrimSpace(member.Branch)
			if !filepath.IsAbs(member.Repository) || !filepath.IsAbs(member.Path) || member.Branch == "" {
				return fmt.Errorf("workspace %q members require absolute repository and path plus branch", workspace.ID)
			}
			key := pathKey(member.Path)
			if paths[key] {
				return fmt.Errorf("duplicate workspace member path %q", member.Path)
			}
			paths[key] = true
			if member.Primary {
				primaryCount++
				if !samePath(member.Path, workspace.PrimaryPath) {
					return fmt.Errorf("workspace %q primary member does not match primary_path", workspace.ID)
				}
			}
		}
		if primaryCount != 1 {
			return fmt.Errorf("workspace %q must contain exactly one primary member", workspace.ID)
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
		}
		sort.Slice(workspace.Members, func(i, j int) bool { return pathKey(workspace.Members[i].Path) < pathKey(workspace.Members[j].Path) })
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
func pathKey(path string) string {
	path = cleanPath(path)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
