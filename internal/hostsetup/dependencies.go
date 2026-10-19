package hostsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"radar/internal/setup"
)

type Dependency struct{ Name, Reason string }
type Service struct {
	UI        setup.UI
	System    setup.System
	Completed func(string)
}

// MissingTools checks only executable availability on PATH. Dashboard startup
// must not launch shims, prompt for developer tools, or run slow version probes.
// Setup remains responsible for checking usability and minimum versions.
func MissingTools() []string { return missingTools(setup.RealSystem{}) }

func missingTools(sys setup.System) []string {
	var missing []string
	for _, name := range requiredTools {
		if toolBinary(sys, name) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func toolBinary(sys setup.System, name string) string {
	if sys.LookPath(name) {
		return name
	}
	if name == "fd" && sys.Platform() == "linux" && sys.LookPath("fdfind") {
		return "fdfind"
	}
	return ""
}

func (w Service) Dependencies(ctx context.Context) error {
	if err := w.requiredTools(ctx); err != nil {
		return err
	}
	w.UI.Print("\nPi extensions\n")
	err := w.Extensions(ctx, Dependency{"pi-radar", "Pi's mandatory Radar workspace tools and context"})
	if errors.Is(err, setup.ErrExtensionDeclined) {
		return setup.ErrAborted
	}
	return err
}

func (w Service) Extensions(ctx context.Context, checks ...Dependency) error {
	for _, dep := range checks {
		if dep.Name == "pi-sbx" {
			w.UI.Print("\nSBX sandboxing is enabled — checking pi-sbx 0.7.0+.\n")
		}
		ready, err := w.installed(ctx, dep.Name)
		if err != nil {
			return err
		}
		if ready {
			w.UI.Print("✓ %s ready\n", dep.Name)
			continue
		}
		argv, err := w.installCommand(dep.Name)
		if err != nil {
			return err
		}
		w.UI.Print("! %s is missing or below its required version.\n  %s\n  Command: %s\n", dep.Name, dep.Reason, strings.Join(argv, " "))
		yes, err := w.UI.Confirm("Install or update "+dep.Name+" now?", true)
		if err != nil {
			return err
		}
		if !yes {
			return setup.ErrExtensionDeclined
		}
		if err := w.System.Run(ctx, setup.Command{Name: argv[0], Args: argv[1:], Dir: userHome()}); err != nil {
			return fmt.Errorf("install %s failed — fix the command above and rerun `radar setup`: %w", dep.Name, err)
		}
		w.Completed(dep.Name + " installed")
		ready, err = w.installed(ctx, dep.Name)
		if err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("%s is still unavailable or too old on PATH — install the required version, check PATH, then rerun `radar setup`", dep.Name)
		}
		w.UI.Print("✓ %s ready\n", dep.Name)
	}
	return nil
}

func (w Service) installed(ctx context.Context, name string) (bool, error) {
	if name == "pi-radar" || name == "pi-sbx" {
		// Inspect the user profile without executing extension code.
		return packageInstalled(name)
	}
	return w.checkTool(ctx, name).state == toolReady, nil
}

func (w Service) installCommand(name string) ([]string, error) {
	switch name {
	case "pi-radar", "pi-sbx":
		return []string{"pi", "install", "npm:@christianmoesl/" + name}, nil
	default:
		return nil, fmt.Errorf("%s must be installed by the installer, not onboarding", name)
	}
}

func versionAtLeast(value string, minimum [3]int) bool {
	match := cliVersionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return false
	}
	for i, min := range minimum {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return false
		}
		if n != min {
			return n > min
		}
	}
	return true
}

func packageInstalled(name string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		dir = filepath.Join(home, ".pi", "agent")
	}
	if strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(home, dir[2:])
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	path := filepath.Join(dir, "settings.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Pi settings: %w", err)
	}
	var settings struct {
		Packages   []json.RawMessage `json:"packages"`
		Extensions []string          `json:"extensions"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return false, fmt.Errorf("Pi settings at %s are invalid — fix them before setup", path)
	}
	for _, raw := range settings.Packages {
		var source string
		var entry struct {
			Source     string    `json:"source"`
			Extensions *[]string `json:"extensions"`
		}
		if json.Unmarshal(raw, &source) != nil {
			if json.Unmarshal(raw, &entry) != nil {
				return false, fmt.Errorf("invalid Pi package entry in %s", path)
			}
			source = entry.Source
		}
		installed := packageSourcePath(dir, source, name)
		if installed == "" {
			continue
		}
		if entry.Extensions != nil {
			return false, fmt.Errorf("%s has an explicit extension filter — enable it with `pi config` before setup; Radar will not override that choice", name)
		}
		if packageReady(installed, name) {
			return true, nil
		}
		if !strings.HasPrefix(source, "npm:") {
			return false, fmt.Errorf("the configured %s package is missing or too old — repair/update its existing source with Pi before setup; Radar will not add a duplicate npm installation", name)
		}
	}
	for _, extension := range settings.Extensions {
		enabled := !strings.HasPrefix(extension, "!") && !strings.HasPrefix(extension, "-")
		candidate := strings.TrimLeft(extension, "!+-")
		candidate = localPath(dir, candidate)
		if strings.HasSuffix(filepath.ToSlash(candidate), "/"+name+"/index.ts") {
			if !enabled {
				return false, fmt.Errorf("%s is explicitly disabled — enable it with `pi config` before setup", name)
			}
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				if name == "pi-sbx" {
					return false, fmt.Errorf("install pi-sbx 0.7.0+ as a Pi package so its version can be checked")
				}
				return true, nil
			}
		}
	}
	return false, nil
}

func localPath(dir, value string) string {
	if strings.HasPrefix(value, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, value[2:])
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(dir, value)
}
func packageSourcePath(dir, source, name string) string {
	packageName := "@christianmoesl/" + name
	if source == "npm:"+packageName || strings.HasPrefix(source, "npm:"+packageName+"@") {
		return filepath.Join(dir, "npm", "node_modules", "@christianmoesl", name)
	}
	repo := name
	if name == "pi-radar" {
		repo = "radar"
	}
	if regexp.MustCompile(`(?i)^(?:git:)?(?:https://)?github\.com/ChristianMoesl/` + regexp.QuoteMeta(repo) + `(?:\.git)?(?:@[^\s]+)?/?$`).MatchString(source) {
		return filepath.Join(dir, "git", "github.com", "ChristianMoesl", repo)
	}
	if strings.Contains(source, ":") {
		return ""
	}
	candidate := localPath(dir, source)
	if packageIdentity(candidate, name) {
		return candidate
	}
	return ""
}
func readManifest(path string) (string, string) {
	data, err := os.ReadFile(filepath.Join(path, "package.json"))
	if err != nil {
		return "", ""
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return "", ""
	}
	return manifest.Name, manifest.Version
}
func packageIdentity(path, name string) bool {
	packageName, _ := readManifest(path)
	return packageName == "@christianmoesl/"+name
}
func packageReady(path, name string) bool {
	packageName, version := readManifest(path)
	if packageName != "@christianmoesl/"+name {
		return false
	}
	if name != "pi-sbx" {
		return true
	}
	// Match the early-sandbox guard: only stable, canonical package versions
	// establish readiness. Build metadata is valid; prereleases and loose CLI
	// spellings such as v0.7.0 or 0.06.0 must not pass onboarding.
	stable := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	core, _, _ := strings.Cut(version, "+")
	return stable.MatchString(version) && versionAtLeast(core, [3]int{0, 7, 0})
}

func userHome() string { home, _ := os.UserHomeDir(); return home }
