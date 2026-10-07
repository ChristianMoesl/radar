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

const radarSource = "npm:" + update.Package

type ReleaseState struct {
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
func radarDeclaration(s map[string]json.RawMessage, base string) (source string, filtered bool, err error) {
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
		if isRadarSource(candidate, base) {
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
		if isRadarSource(strings.TrimLeft(e, "!+-"), base) {
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
func isRadarSource(source, base string) bool {
	if source == radarSource || strings.HasPrefix(source, radarSource+"@") {
		return true
	}
	lower := strings.ToLower(source)
	if strings.Contains(lower, "github.com/christianmoesl/radar") || strings.Contains(lower, "github.com:christianmoesl/radar") || strings.Contains(lower, "/pi-radar/") || strings.HasPrefix(lower, "git:") && strings.Contains(lower, "/radar") || strings.HasSuffix(lower, "/radar.ts") || strings.HasSuffix(lower, "/pi-radar.ts") {
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
	return json.Unmarshal(data, &pkg) == nil && pkg.Name == update.Package
}
func InspectRelease(cwd string) ReleaseState {
	profile, err := ProfileDir()
	s := ReleaseState{Profile: profile}
	if err != nil {
		s.Reason = err.Error()
		return s
	}
	settings, err := readSettings(filepath.Join(profile, "settings.json"))
	if err != nil {
		s.Reason = "Cannot read Pi settings: " + err.Error()
		return s
	}
	s.Source, _, err = radarDeclaration(settings, profile)
	if err != nil {
		s.Reason = "Cannot interpret Pi settings"
		return s
	}
	_, filtered, _ := radarDeclaration(settings, profile)
	s.Loaded = loadedReleases(profile)
	if hasLocalRadarExtension(profile) {
		s.Reason = "Local Pi extension: left untouched"
		return s
	}
	// A project override takes precedence in Pi. Conservatively detect ancestors
	// as well; never install from the selected workspace's working directory.
	current, _ := filepath.Abs(cwd)
	for {
		if hasLocalRadarExtension(filepath.Join(current, ".pi")) {
			s.Reason = "Project-local Pi extension: left untouched"
			return s
		}
		project, err := readSettings(filepath.Join(current, ".pi/settings.json"))
		if err != nil {
			s.Reason = "Cannot read project Pi settings"
			return s
		}
		source, _, err := radarDeclaration(project, filepath.Join(current, ".pi"))
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
	packagePath := filepath.Join(profile, "npm/node_modules", update.Package)
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
	if json.Unmarshal(data, &pkg) != nil || pkg.Name != update.Package || !update.Stable(pkg.Version) {
		s.Reason = "Unrecognized installed Pi package"
		return s
	}
	s.Installed = pkg.Version
	if filtered {
		s.Reason = "Disabled/filtered/custom Pi declaration: left untouched"
		return s
	}
	if s.Source != radarSource {
		var marker struct {
			Source string `json:"source"`
		}
		data, err := os.ReadFile(filepath.Join(profile, "radar/release-pin.json"))
		if err != nil || json.Unmarshal(data, &marker) != nil || marker.Source != s.Source || !strings.HasPrefix(s.Source, radarSource+"@") {
			s.Reason = "Pinned/Git/local Pi declaration: left untouched"
			return s
		}
	}
	s.CanUpdate = true
	s.Reason = "Exact-version install requires an explicit Radar-managed pin; running sessions need /reload"
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
func InstallRelease(ctx context.Context, before ReleaseState, cwd, target string) error {
	if !before.CanUpdate || !update.Stable(target) {
		return errors.New("Pi source is not eligible for coordinated updates")
	}
	now := InspectRelease(cwd)
	if !now.CanUpdate || now.Source != before.Source || now.Profile != before.Profile || now.Installed != before.Installed {
		return errors.New("Pi installation changed; review it again")
	}
	source := radarSource + "@" + target
	cmd := exec.CommandContext(ctx, "pi", "install", source)
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+before.Profile)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Radar updated, but Pi install failed (it may have partially changed): %w; retry the displayed pi install command", err)
	}
	settings, err := readSettings(filepath.Join(before.Profile, "settings.json"))
	if err != nil {
		return err
	}
	actual, filtered, err := radarDeclaration(settings, before.Profile)
	data, readErr := os.ReadFile(filepath.Join(before.Profile, "npm/node_modules", update.Package, "package.json"))
	var pkg struct {
		Version string `json:"version"`
	}
	if err != nil || filtered || actual != source || readErr != nil || json.Unmarshal(data, &pkg) != nil || pkg.Version != target {
		return errors.New("Pi command finished but the exact package/pin could not be verified; repair Pi separately")
	}
	dir := filepath.Join(before.Profile, "radar")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	marker, _ := json.Marshal(map[string]string{"source": source})
	file, err := os.CreateTemp(dir, ".release-pin-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(marker)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "release-pin.json"))
}

func hasLocalRadarExtension(base string) bool {
	for _, name := range []string{"radar.ts", "radar.js", "pi-radar.ts", "pi-radar.js", "radar", "pi-radar"} {
		if _, err := os.Lstat(filepath.Join(base, "extensions", name)); err == nil {
			return true
		}
	}
	return false
}
