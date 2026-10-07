package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"radar/internal/command"
	"radar/internal/config"
	"radar/internal/integration/tmux"
)

const radarPackage = "@christianmoesl/pi-radar"

type system interface {
	lookPath(string) bool
	platform() string
	output(context.Context, string, ...string) (string, error)
	run(context.Context, string, ...string) error
}
type realSystem struct{}

func (realSystem) lookPath(name string) bool { _, err := exec.LookPath(name); return err == nil }
func (realSystem) platform() string          { return runtime.GOOS }
func (realSystem) output(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := command.CommandContext(ctx, name, args...)
	// Package commands must not inspect or change the project we launched from.
	if name == "pi" {
		cmd.Dir, _ = os.UserHomeDir()
	}
	data, err := cmd.Output()
	return string(data), err
}
func (realSystem) run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "pi" {
		cmd.Dir, _ = os.UserHomeDir()
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

type dependency struct{ name, reason string }

var dependencies = []dependency{
	{"git", "repository and worktree management"},
	{"tmux", "tmux 3.2+ for workspace sessions and dashboard popups"},
	{"fd", "repository discovery"},
	{"node", "Node.js 24+ is a Pi runtime prerequisite; Radar itself does not need it"},
	{"npm", "installing Pi and its packages"},
	{"pi", "Pi 0.85.1+ runs your workspace coding sessions"},
	{"pi-radar", "Pi's Radar workspace tools and context"},
	{"gh", "GitHub authentication and pull requests"},
}

func (w *wizard) dependencies(ctx context.Context, sandbox config.SBXConfig) error {
	w.ui.print("Required tools\n")
	for _, dep := range dependencies {
		w.ui.print("  %-10s %s\n", dep.name, dep.reason)
	}
	w.ui.print("\n")
	checks := append([]dependency(nil), dependencies...)
	// pi-sbx belongs to optional sandboxing, not Radar's baseline tool set.
	// Use the same effective default as workspace creation, including explicit
	// disablement even when the SBX executable happens to be installed.
	if sandbox.WorkspaceEnabled(w.system.platform(), func(name string) error {
		if w.system.lookPath(name) {
			return nil
		}
		return exec.ErrNotFound
	}) {
		checks = append(checks, dependency{"pi-sbx", "pi-sbx 0.6.0+ routes Pi tools when SBX sandboxing is used"})
	}
	for _, dep := range checks {
		if dep.name == "pi-sbx" {
			w.ui.print("\nSBX sandboxing is enabled — checking its optional integration dependency.\n")
		}
		ready, err := w.installed(ctx, dep.name)
		if err != nil {
			return err
		}
		if ready {
			w.ui.print("✓ %s ready\n", dep.name)
			continue
		}
		argv, err := w.installCommand(dep.name)
		if err != nil {
			return err
		}
		w.ui.print("! %s is missing or below its required version.\n  %s\n  Command: %s\n", dep.name, dep.reason, strings.Join(argv, " "))
		if err := w.allow("Install or update " + dep.name + " now?"); err != nil {
			return err
		}
		wasInstalled := w.system.lookPath(dep.name)
		if err := w.system.run(ctx, argv[0], argv[1:]...); err != nil {
			return fmt.Errorf("install %s failed — fix the command above and rerun `radar setup`: %w", dep.name, err)
		}
		ready, err = w.installed(ctx, dep.name)
		if err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("%s is still unavailable or too old on PATH — install the required version, check PATH, then rerun `radar setup`", dep.name)
		}
		if dep.name == "tmux" && !wasInstalled {
			w.installedTmux = true
		}
		w.ui.print("✓ %s ready\n", dep.name)
	}
	return nil
}

func (w wizard) installed(ctx context.Context, name string) (bool, error) {
	switch name {
	case "tmux":
		if !w.system.lookPath(name) {
			return false, nil
		}
		version, err := w.system.output(ctx, name, "-V")
		return err == nil && tmux.SupportsPopup(version), nil
	case "node":
		if !w.system.lookPath(name) {
			return false, nil
		}
		version, err := w.system.output(ctx, name, "--version")
		return err == nil && versionAtLeast(version, [3]int{24, 0, 0}), nil
	case "pi":
		if !w.system.lookPath(name) {
			return false, nil
		}
		version, err := w.system.output(ctx, name, "--version")
		return err == nil && versionAtLeast(version, [3]int{0, 85, 1}), nil
	case "fd":
		binary := "fd"
		if !w.system.lookPath(binary) {
			if w.system.platform() != "linux" || !w.system.lookPath("fdfind") {
				return false, nil
			}
			binary = "fdfind"
		}
		_, err := w.system.output(ctx, binary, "--version")
		return err == nil, nil
	case "pi-radar", "pi-sbx":
		// Read-only inspection of the user profile, never execute extension code or
		// rely on a project-only install that new workspaces will not inherit.
		return packageInstalled(name)
	default:
		if !w.system.lookPath(name) {
			return false, nil
		}
		// A PATH entry can still be an unusable shim (for example macOS Git
		// before Command Line Tools are installed). Prove it can execute.
		_, err := w.system.output(ctx, name, "--version")
		return err == nil, nil
	}
}

func (w wizard) installCommand(name string) ([]string, error) {
	switch name {
	case "pi":
		return []string{"npm", "install", "--global", "--ignore-scripts", "@earendil-works/pi-coding-agent"}, nil
	case "pi-radar", "pi-sbx":
		return []string{"pi", "install", "npm:@christianmoesl/" + name}, nil
	}
	if w.system.lookPath("brew") {
		pkg := name
		if pkg == "npm" {
			pkg = "node"
		}
		verb := "install"
		if (name == "node" || name == "tmux") && w.system.lookPath(name) {
			verb = "upgrade"
		}
		return []string{"brew", verb, pkg}, nil
	}
	if w.system.platform() == "linux" {
		if w.system.lookPath("apt-get") {
			pkg := name
			if pkg == "fd" {
				pkg = "fd-find"
			}
			if pkg == "node" {
				pkg = "nodejs"
			}
			args := []string{"apt-get", "install", "-y", pkg}
			if name == "node" {
				args = append(args, "npm")
			}
			if os.Geteuid() != 0 {
				args = append([]string{"sudo"}, args...)
			}
			return args, nil
		}
	}
	return nil, fmt.Errorf("%s is required — install it on PATH and rerun `radar setup` (automatic installation needs Homebrew or apt-get)", name)
}

func versionAtLeast(value string, minimum [3]int) bool {
	match := regexp.MustCompile(`(?:^|\s)v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\s|$)`).FindStringSubmatch(strings.TrimSpace(value))
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
					return false, fmt.Errorf("install pi-sbx 0.6.0+ as a Pi package so its version can be checked")
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
	// spellings such as v0.6.0 or 0.06.0 must not pass onboarding.
	stable := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	core, _, _ := strings.Cut(version, "+")
	return stable.MatchString(version) && versionAtLeast(core, [3]int{0, 6, 0})
}
