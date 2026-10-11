package onboarding

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"radar/internal/integration/tmux"
)

// ErrToolsNotReady means the tool report and next steps have already been shown.
// Callers should exit unsuccessfully without printing another error paragraph.
var ErrToolsNotReady = errors.New("required tools need attention")

var requiredTools = []string{"git", "tmux", "fd", "gh", "node", "npm", "pi"}

const piInstallCommand = "curl -fsSL https://pi.dev/install.sh | sh"

var cliVersionPattern = regexp.MustCompile(`(?:^|\s)v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\s|$)`)
var tmuxVersionPattern = regexp.MustCompile(`^tmux ((?:next-)?[0-9]+\.[0-9]+[a-z]?)`)

type toolState int

const (
	toolReady toolState = iota
	toolMissing
	toolOutdated
	toolBroken
)

type toolCheck struct {
	name   string
	state  toolState
	detail string
}

func toolLabel(name string) string {
	switch name {
	case "node":
		return "Node.js"
	case "pi":
		return "Pi"
	default:
		return name
	}
}

func toolRequirement(name string) string {
	switch name {
	case "node":
		return " — requires 24+"
	case "pi":
		return " — requires 0.85.1+"
	case "tmux":
		return " — requires 3.2+"
	case "npm":
		return " — included with Node.js"
	default:
		return ""
	}
}

func (w wizard) checkTool(ctx context.Context, name string) toolCheck {
	check := toolCheck{name: name, detail: "ready"}
	binary := toolBinary(w.system, name)
	if binary == "" {
		check.state, check.detail = toolMissing, "missing"+toolRequirement(name)
		return check
	}
	flag := "--version"
	if name == "tmux" {
		flag = "-V"
	}
	output, err := w.system.output(ctx, binary, flag)
	if err != nil {
		check.state, check.detail = toolBroken, "failed to run"
		return check
	}

	var version string
	ready := true
	switch name {
	case "node", "pi":
		minimum := [3]int{24, 0, 0}
		if name == "pi" {
			minimum = [3]int{0, 85, 1}
		}
		ready = versionAtLeast(output, minimum)
		if match := cliVersionPattern.FindStringSubmatch(strings.TrimSpace(output)); match != nil {
			version = strings.Join(match[1:], ".")
		}
	case "tmux":
		ready = tmux.SupportsPopup(output)
		if match := tmuxVersionPattern.FindStringSubmatch(strings.TrimSpace(output)); match != nil {
			version = match[1]
		}
	}
	if !ready {
		check.state, check.detail = toolOutdated, version+toolRequirement(name)
		if version == "" {
			check.state, check.detail = toolBroken, "could not read version"+toolRequirement(name)
		}
	}
	return check
}

func (w wizard) requiredTools(ctx context.Context) error {
	checks := make(map[string]toolCheck, len(requiredTools))
	ready := true
	style := w.ui.style()
	for _, name := range requiredTools {
		check := w.checkTool(ctx, name)
		checks[name] = check
		marker := style.Foreground(lipgloss.Color("2")).Render("✓")
		detail := style.Foreground(lipgloss.Color("8")).Render(check.detail)
		if check.state != toolReady {
			ready = false
			marker = style.Foreground(lipgloss.Color("3")).Render("!")
			detail = check.detail
		}
		w.ui.print("  %s %-8s %s\n", marker, toolLabel(name), detail)
	}
	if ready {
		return nil
	}

	commands, guidance := w.toolRepairs(checks)
	title := "Install the missing tools:"
	if len(guidance) > 0 {
		title = "Next steps:"
	}
	w.ui.print("\n%s\n", style.Bold(true).Render(title))
	for _, line := range guidance {
		w.ui.print("  %s\n", line)
	}
	if len(commands) > 0 {
		w.ui.print("\n  %s\n", strings.Join(commands, " && "))
	}
	w.ui.print("\nThen run radar again to continue setup.\n")
	return ErrToolsNotReady
}

// Recommend installs only for absent tools. Existing installations may belong
// to nvm, asdf, source builds, etc.; do not replace them with a second install.
// These commands are displayed, never executed by setup.
func (w wizard) toolRepairs(checks map[string]toolCheck) (commands, guidance []string) {
	brew := w.system.lookPath("brew")
	var packages, systemPackages []string
	for _, name := range []string{"git", "tmux", "fd", "gh"} {
		check := checks[name]
		switch check.state {
		case toolMissing:
			if brew {
				packages = append(packages, name)
			} else {
				systemPackages = append(systemPackages, name)
			}
		case toolOutdated:
			guidance = append(guidance, "Update "+name+" using its existing installation method"+toolRequirement(name)+".")
		case toolBroken:
			guidance = append(guidance, "Repair "+name+" using its existing installation method; check PATH.")
		}
	}
	if len(systemPackages) > 0 {
		guidance = append(guidance, "Install "+strings.Join(systemPackages, ", ")+" with your system package manager.")
	}

	node, npm := checks["node"], checks["npm"]
	if node.state != toolReady || npm.state != toolReady {
		switch {
		case node.state == toolMissing && npm.state == toolMissing:
			if brew {
				packages = append(packages, "node")
			} else {
				guidance = append(guidance, "Install Node.js 24+ with npm: https://nodejs.org/en/download")
			}
		case node.state == toolReady:
			guidance = append(guidance, "Repair npm through your existing Node.js installation; check PATH.")
		default:
			guidance = append(guidance, "Update or repair your existing Node.js installation to provide Node.js 24+ and npm on PATH.")
		}
	}

	pi := checks["pi"]
	if pi.state == toolMissing && !w.system.lookPath("curl") {
		if brew {
			packages = append(packages, "curl")
		} else {
			guidance = append(guidance, "Install curl with your system package manager before running the command below.")
		}
	}
	if len(packages) > 0 {
		commands = append(commands, "brew install "+strings.Join(packages, " "))
	}
	switch pi.state {
	case toolMissing:
		commands = append(commands, piInstallCommand)
	case toolOutdated:
		guidance = append(guidance, "Update Pi to 0.85.1+ using its existing installation method.")
	case toolBroken:
		guidance = append(guidance, "Repair Pi using its existing installation method; check PATH.")
	}
	return commands, guidance
}
