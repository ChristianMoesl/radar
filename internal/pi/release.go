package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"radar/internal/update"
)

const SandboxPackage = "@christianmoesl/pi-sbx"

func supportedPackage(name string) bool {
	return name == update.Package || name == SandboxPackage
}

type ReleaseState struct {
	Package   string
	Profile   string
	Source    string
	Installed string
	Reason    string
	CanUpdate bool
	Loaded    []LoadedRelease
}
type LoadedRelease struct {
	PID     int    `json:"pid"`
	Version string `json:"version"`
	Profile string `json:"profile"`
	CWD     string `json:"cwd"`
	Source  string `json:"source"`
	Updated int64  `json:"updated"`
}

func ProfileDir() (string, error) {
	if p := os.Getenv("PI_CODING_AGENT_DIR"); p != "" {
		if p == "~" || strings.HasPrefix(p, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
		return filepath.Abs(p)
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".pi/agent"), err
}
func readSettings(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s map[string]json.RawMessage
	err = json.Unmarshal(data, &s)
	return s, err
}
func packageDeclaration(s map[string]json.RawMessage, base, name string) (source string, filtered bool, err error) {
	var packages []json.RawMessage
	if data, ok := s["packages"]; ok {
		if err := json.Unmarshal(data, &packages); err != nil {
			return "", false, err
		}
	}
	matches := 0
	for _, raw := range packages {
		var candidate string
		isFiltered := false
		if json.Unmarshal(raw, &candidate) != nil {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				return "", false, err
			}
			if err := json.Unmarshal(object["source"], &candidate); err != nil {
				return "", false, err
			}
			// Do not reinterpret resource filters or deliberate autoload settings.
			isFiltered = len(object) > 1
		}
		if isPackageSource(candidate, base, name) {
			source = candidate
			filtered = filtered || isFiltered
			matches++
		}
	}
	var extensions []string
	if data, ok := s["extensions"]; ok {
		if err := json.Unmarshal(data, &extensions); err != nil {
			return "", false, err
		}
	}
	for _, e := range extensions {
		if isPackageSource(strings.TrimLeft(e, "!+-"), base, name) {
			source = e
			filtered = true
			matches++
		}
	}
	if matches > 1 {
		filtered = true
	}
	return source, filtered, nil
}
func isPackageSource(source, base, name string) bool {
	npmSource := "npm:" + name
	if source == npmSource || strings.HasPrefix(source, npmSource+"@") {
		return true
	}
	lower := strings.ToLower(source)
	short := strings.TrimPrefix(name, "@christianmoesl/")
	repo := short
	if name == update.Package {
		repo = "radar"
	}
	if strings.Contains(lower, "github.com/christianmoesl/"+repo) || strings.Contains(lower, "github.com:christianmoesl/"+repo) || strings.Contains(lower, "/"+short+"/") || strings.HasPrefix(lower, "git:") && strings.Contains(lower, "/"+repo) || strings.HasSuffix(lower, "/"+repo+".ts") || strings.HasSuffix(lower, "/"+short+".ts") {
		return true
	}
	if strings.HasPrefix(source, "npm:") || strings.Contains(source, "://") || strings.HasPrefix(source, "git:") {
		return false
	}
	if strings.HasPrefix(source, "~/") {
		home, _ := os.UserHomeDir()
		source = filepath.Join(home, source[2:])
	}
	if !filepath.IsAbs(source) {
		source = filepath.Join(base, source)
	}
	data, err := os.ReadFile(filepath.Join(source, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Name string `json:"name"`
	}
	return json.Unmarshal(data, &pkg) == nil && pkg.Name == name
}
func InspectRelease(cwd, name string) ReleaseState {
	profile, err := ProfileDir()
	s := ReleaseState{Profile: profile, Package: name}
	if !supportedPackage(name) {
		s.Reason = "Not a Radar-related Pi package"
		return s
	}
	if err != nil {
		s.Reason = err.Error()
		return s
	}
	settings, err := readSettings(filepath.Join(profile, "settings.json"))
	if err != nil {
		s.Reason = "Cannot read Pi settings: " + err.Error()
		return s
	}
	var filtered bool
	s.Source, filtered, err = packageDeclaration(settings, profile, name)
	if err != nil {
		s.Reason = "Cannot interpret Pi settings"
		return s
	}
	if name == update.Package {
		s.Loaded = loadedReleases(profile)
	}
	if hasLocalPackageExtension(profile, name) {
		s.Reason = "Local Pi extension: left untouched"
		return s
	}
	// A project override takes precedence in Pi. Conservatively detect ancestors
	// as well; never install from the selected workspace's working directory.
	current, _ := filepath.Abs(cwd)
	for {
		if hasLocalPackageExtension(filepath.Join(current, ".pi"), name) {
			s.Reason = "Project-local Pi extension: left untouched"
			return s
		}
		project, err := readSettings(filepath.Join(current, ".pi/settings.json"))
		if err != nil {
			s.Reason = "Cannot read project Pi settings"
			return s
		}
		source, _, err := packageDeclaration(project, filepath.Join(current, ".pi"), name)
		if err != nil || source != "" {
			s.Reason = "Project-local Pi integration: update it explicitly in that project"
			return s
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	if s.Source == "" {
		s.Reason = "Pi integration not configured (optional; left untouched)"
		return s
	}
	packagePath := filepath.Join(profile, "npm/node_modules", name)
	resolved, err := filepath.EvalSymlinks(packagePath)
	if err != nil {
		s.Reason = "Pi package is not installed in the host profile; left untouched"
		return s
	}
	npmRoot, _ := filepath.EvalSymlinks(filepath.Join(profile, "npm"))
	rel, err := filepath.Rel(npmRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		s.Reason = "Local/development Pi package: left untouched"
		return s
	}
	for _, r := range s.Loaded {
		if r.Source == "" {
			continue
		}
		source := filepath.Clean(r.Source)
		if !filepath.IsAbs(source) {
			s.Reason = "Loaded Pi source is not an absolute path; left untouched"
			return s
		}
		if canonical, err := filepath.EvalSymlinks(source); err == nil {
			source = canonical
		}
		// A resident pnpm-loaded module can refer to a now-removed older
		// version directory. It still belongs to this managed npm profile.
		relative, err := filepath.Rel(npmRoot, source)
		if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			s.Reason = "A running Pi session loaded a custom/project Radar source; left untouched"
			return s
		}
	}
	data, err := os.ReadFile(filepath.Join(packagePath, "package.json"))
	if err != nil {
		s.Reason = "Cannot read installed Pi package"
		return s
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Name != name || !update.Stable(pkg.Version) {
		s.Reason = "Unrecognized installed Pi package"
		return s
	}
	s.Installed = pkg.Version
	if filtered {
		s.Reason = "Disabled/filtered/custom Pi declaration: left untouched"
		return s
	}
	if s.Source != "npm:"+name {
		s.Reason = "Pinned/Git/local/custom Pi declaration: left untouched"
		return s
	}
	s.CanUpdate = true
	s.Reason = "Unpinned user package: updates automatically; declaration stays unpinned"
	return s
}
func loadedReleases(profile string) []LoadedRelease {
	var result []LoadedRelease
	entries, _ := os.ReadDir(filepath.Join(profile, "radar/loaded"))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(profile, "radar/loaded", e.Name()))
		if err != nil || len(data) > 16384 {
			continue
		}
		var r LoadedRelease
		if json.Unmarshal(data, &r) != nil || r.Profile != profile || r.PID <= 0 || !update.Stable(r.Version) || time.Now().UnixMilli()-r.Updated > 180000 || r.Updated > time.Now().UnixMilli()+60000 {
			continue
		}
		if syscall.Kill(r.PID, 0) == nil {
			result = append(result, r)
		}
	}
	return result
}
func CheckReleasePrerequisites(ctx context.Context, m update.Manifest) error {
	for _, spec := range []struct {
		name    string
		minimum string
	}{{"node", fmt.Sprintf("%d.0.0", m.MinNode)}, {"pi", m.MinPi}} {
		output, err := exec.CommandContext(ctx, spec.name, "--version").Output()
		if err != nil {
			return fmt.Errorf("%s is required for the requested Pi update: %w", spec.name, err)
		}
		version := strings.TrimSpace(string(output))
		version = strings.TrimPrefix(version, "v")
		if !update.Stable(version) || update.Compare(version, spec.minimum) < 0 {
			return fmt.Errorf("%s >= %s required; Radar will not update Pi core or Node", spec.name, spec.minimum)
		}
	}
	return nil
}

// UpdateRelease delegates to Pi's targeted package updater: it follows the
// configured unpinned source without pinning it or updating unrelated packages.
func UpdateRelease(ctx context.Context, before ReleaseState, cwd string) (ReleaseState, error) {
	if !before.CanUpdate || !supportedPackage(before.Package) {
		return before, errors.New("Pi source is not eligible for coordinated updates")
	}
	now := InspectRelease(cwd, before.Package)
	if !now.CanUpdate || now.Source != before.Source || now.Profile != before.Profile || now.Installed != before.Installed {
		return now, errors.New("Pi installation changed; inspect it before retrying radar update")
	}
	cmd := exec.CommandContext(ctx, "pi", "update", before.Source, "--no-approve")
	home, err := os.UserHomeDir()
	if err != nil {
		return before, err
	}
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+before.Profile)
	// Package updates are non-interactive; never consume CLI approval input.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return before, fmt.Errorf("Pi package update failed (it may have partially changed): %w; retry PI_CODING_AGENT_DIR=%q pi update %s --no-approve", err, before.Profile, before.Source)
	}
	after := InspectRelease(cwd, before.Package)
	if !after.CanUpdate || after.Source != before.Source || after.Profile != before.Profile || update.Compare(after.Installed, before.Installed) < 0 {
		return after, errors.New("Pi command finished but the unpinned package could not be verified; repair Pi separately")
	}
	return after, nil
}

func hasLocalPackageExtension(base, name string) bool {
	short := strings.TrimPrefix(name, "@christianmoesl/")
	names := []string{short + ".ts", short + ".js", short}
	if name == update.Package {
		names = append(names, "radar.ts", "radar.js", "radar")
	}
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(base, "extensions", name)); err == nil {
			return true
		}
	}
	return false
}
