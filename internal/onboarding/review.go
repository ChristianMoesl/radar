package onboarding

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/config"
	"radar/internal/pathdisplay"
	"radar/internal/pi"
	"radar/internal/setup"
)

type reviewFile struct {
	path, purpose, action string
	details               string // Never contains credential-file contents.
}

type setupReview struct {
	files       []reviewFile
	directories []string
	credentials bool
	completed   []string
	summaryText string
}

func fromSetupFile(file setup.File) reviewFile {
	return reviewFile{path: file.Path, purpose: file.Purpose, action: file.Action, details: file.Details}
}
func reviewFileChange(path, purpose string, after []byte) (reviewFile, error) {
	file, err := setup.ReviewFileChange(path, purpose, after)
	return fromSetupFile(file), err
}
func (w wizard) review(path string, data []byte, updates config.Secrets) error {
	report, err := buildReview(path, data, updates, w.plans)
	if err != nil {
		return err
	}
	report.completed = append([]string(nil), w.completed...)
	return w.ui.review(report)
}

func buildReview(path string, data []byte, updates config.Secrets, plans []setup.Plan) (setupReview, error) {
	var report setupReview
	settings, err := reviewFileChange(path, "Radar settings", data)
	if err != nil {
		return report, err
	}
	report.files = append(report.files, settings)

	secrets, err := reviewSecrets(updates)
	if err != nil {
		return report, err
	}
	if secrets != nil {
		report.files = append(report.files, *secrets)
		report.credentials = true
	}

	instructions := reviewFile{path: filepath.Join(filepath.Dir(path), "AGENTS.md"), purpose: "Agent instructions", action: "UNCHANGED", details: "Existing instructions are preserved, including symlinks."}
	if _, err := os.Lstat(instructions.path); os.IsNotExist(err) {
		instructions.action, instructions.details = "CREATE", pi.DefaultInstructions
	} else if err != nil {
		return report, err
	}
	report.files = append(report.files, instructions)

	seen := map[string]bool{}
	for _, plan := range plans {
		for _, file := range plan.Files {
			report.files = append(report.files, fromSetupFile(file))
		}
		if plan.Summary != "" {
			if report.summaryText != "" {
				report.summaryText += "\n"
			}
			report.summaryText += plan.Summary
		}
		for _, dir := range plan.Directories {
			if seen[dir] {
				continue
			}
			seen[dir] = true
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				report.directories = append(report.directories, dir)
			} else if err != nil {
				return report, err
			}
		}
	}

	return report, nil
}

// Do not build a raw preview/diff of secrets.yaml. Only compare values to label
// the action accurately; the review model receives descriptions, never secrets.
func reviewSecrets(updates config.Secrets) (*reviewFile, error) {
	path, err := config.SecretsPath()
	if err != nil {
		return nil, err
	}
	_, err = os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if os.IsNotExist(err) && len(updates) == 0 {
		return nil, nil
	}
	file := &reviewFile{path: path, purpose: "Credentials (values hidden)", action: "UNCHANGED", details: "Existing credentials are retained. Values are never displayed."}
	if len(updates) == 0 {
		return file, nil
	}
	before, err := config.LoadSecrets()
	if err != nil {
		return nil, err
	}
	for integration, values := range updates {
		if before[integration] == nil {
			file.action = "UPDATE"
		}
		for key, value := range values {
			old, exists := before[integration][key]
			if !exists || old != value {
				file.action = "UPDATE"
			}
		}
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		file.action = "CREATE"
	}
	if file.action != "UNCHANGED" {
		file.details = "Save the provided credentials; retain existing entries.\nPlaintext, owner-only permissions (0600). Values are never displayed."
	}
	return file, nil
}

func (r setupReview) summary(style lipgloss.Style) string {
	var out strings.Builder
	fmt.Fprintln(&out, style.Bold(true).Render("Review changes"))
	fmt.Fprint(&out, "Nothing below is written until you save.\n\n")
	if r.summaryText != "" {
		fmt.Fprintln(&out, r.summaryText+"\n")
	}
	width := 0
	for _, file := range r.files {
		width = max(width, ansi.StringWidth(pathdisplay.HomeRelative(file.path)))
	}
	for _, file := range r.files {
		path := pathdisplay.HomeRelative(file.path)
		label := fmt.Sprintf("%-9s", file.action)
		color := lipgloss.Color("2")
		if file.action == "UPDATE" || file.action == "APPEND" {
			color = "3"
		}
		line := style.Foreground(color).Render(label) + "  " + path + strings.Repeat(" ", width-ansi.StringWidth(path)+2) + file.purpose
		if file.action == "UNCHANGED" {
			line = style.Foreground(lipgloss.Color("8")).Render(label + "  " + path + strings.Repeat(" ", width-ansi.StringWidth(path)+2) + file.purpose)
		}
		fmt.Fprintln(&out, line)
	}
	if len(r.directories) > 0 {
		fmt.Fprintln(&out, "\nNew directories")
		for _, dir := range r.directories {
			fmt.Fprintln(&out, "  "+pathdisplay.HomeRelative(dir))
		}
	}
	if r.credentials {
		fmt.Fprintln(&out, "\nCredentials are plaintext; writes use owner-only permissions (0600). Values stay hidden.")
	}
	if len(r.completed) > 0 {
		fmt.Fprintf(&out, "\nAlready done: %s. Cancelling will not undo these.\n", strings.Join(r.completed, "; "))
	}
	return out.String()
}
