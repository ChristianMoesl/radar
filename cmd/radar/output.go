package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type registrationResult struct {
	Registered    bool   `json:"registered"`
	WorkspacePath string `json:"workspace_path,omitempty"`
}

type pathResult struct {
	Path string `json:"path"`
}

type rateLimitResult struct {
	Summary string `json:"summary"`
}

type daemonResult struct {
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	PIDs   []int  `json:"pids,omitempty"`
}

func printResult(value any) {
	if err := writeResult(os.Stdout, os.Stderr, value, jsonOutput); err != nil {
		fatal(err)
	}
}

// The JSON branch serializes the original result, not a presentation model.
// Human output may evolve independently without changing machine field names.
func writeResult(stdout, stderr io.Writer, value any, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(stdout).Encode(value)
	}
	var body, warnings bytes.Buffer
	p := textResult{out: &body, warnings: &warnings}
	switch v := value.(type) {
	case *protocol.Task:
		p.task(*v)
	case *protocol.TaskDeletionResult:
		p.line("Deleted authored task %d.", v.TaskID)
		p.field("Original path", v.OriginalPath)
		p.field("Trash path", v.TrashPath)
	case *protocol.CleanupResult:
		p.line("Cleaned up %d local resource(s) for task %d.", len(v.Targets), v.TaskID)
		for _, target := range v.Targets {
			p.line("  - %s", singleLine(cleanupTargetDescription(target)))
		}
	case *protocol.GarbageCollectionResult:
		p.line("Garbage collection: %d deleted, %d skipped.", len(v.Deleted), len(v.Skipped))
		rows := [][]string{{"RESULT", "TASK", "PATH", "REASON"}}
		for _, item := range v.Deleted {
			rows = append(rows, []string{"Deleted", fmt.Sprint(item.TaskID), item.Path, ""})
		}
		for _, item := range v.Skipped {
			rows = append(rows, []string{"Skipped", fmt.Sprint(item.TaskID), item.Path, item.Reason})
		}
		if len(rows) > 1 {
			p.table(rows)
		}
	case integration.Workspace:
		p.line("Workspace ready.")
		p.field("Name", v.Name)
		p.field("Path", v.Path)
		p.field("Repository", v.Repo)
		p.field("Branch", v.Branch)
		p.field("Base", v.Base)
		p.field("Session", v.SessionName)
		p.field("Sandbox", v.SandboxName)
		p.warning(v.Warning)
	case registrationResult:
		if v.Registered {
			p.line("Registered Radar workspace: %s", singleLine(v.WorkspacePath))
		} else {
			p.line("Not inside a registered Radar workspace.")
		}
	case integration.WorkspaceContext:
		p.workspace(v)
	case integration.RepositoryRefs:
		p.field("Repository", v.Repository)
		p.field("Default branch", v.DefaultBranch)
		p.field("Valid bases", strings.Join(v.BaseRefs, ", "))
		rows := [][]string{{"BRANCH", "LOCAL", "ORIGIN", "CHECKOUTS"}}
		for _, branch := range v.Branches {
			rows = append(rows, []string{branch.Name, yesNo(branch.Local), yesNo(branch.Origin), strings.Join(branch.CheckedOutPaths, ", ")})
		}
		if len(v.Branches) == 0 {
			p.line("No branches found.")
		} else {
			p.table(rows)
		}
		p.warning(v.Warning)
	case integration.WorkspaceReconcilePlan:
		p.plan(v)
	case integration.WorkspaceReconcileResult:
		if v.OK {
			p.line("Workspace reconciled.")
		} else {
			p.line("Workspace reconciliation incomplete.")
			p.field("Reason", v.Reason)
			p.field("Error", v.Error)
			if v.ReconfirmRequired {
				p.line("The plan changed; review and confirm the updated plan before retrying.")
			} else if v.Retryable {
				p.line("Inspect the current workspace and retry.")
			}
		}
		p.field("Workspace ID", v.WorkspaceID)
		p.field("Revision", v.Revision)
		if v.NoteAdded {
			p.line("Task note attached.")
		}
		p.line("Worktrees: %d added, %d removed", v.WorktreesAdded, v.WorktreesRemoved)
		if v.SandboxReconciled {
			p.line("Sandbox reconciled.")
		}
		p.line("Mounts: %d added, %d removed", v.MountsAdded, v.MountsRemoved)
		p.line("Ports: %d published, %d unpublished", v.PortsPublished, v.PortsUnpublished)
		if v.Plan != nil {
			p.plan(*v.Plan)
		}
		p.warning(v.Warning)
	case pathResult:
		p.line("%s", v.Path)
	case rateLimitResult:
		p.line("%s", v.Summary)
	case daemonResult:
		p.line("Radar daemon %s.", v.Status)
		if len(v.PIDs) > 0 {
			p.line("PIDs: %v", v.PIDs)
		}
	default:
		return fmt.Errorf("no human output renderer for %T", value)
	}
	if _, err := io.Copy(stderr, &warnings); err != nil {
		return err
	}
	_, err := io.Copy(stdout, &body)
	return err
}

func writeDaemonResult(out io.Writer, method string, response protocol.Response) error {
	var body bytes.Buffer
	p := textResult{out: &body}
	switch {
	case method == "refresh":
		p.line("Radar refreshed.")
	case method == "reset":
		p.line("Radar state reset and refreshed.")
	case strings.HasPrefix(method, "ack:"):
		p.line("Acknowledged task %s.", strings.TrimPrefix(method, "ack:"))
	}
	if response.Summary != nil {
		s := response.Summary
		p.line("Tasks: %d immediate, %d need attention, %d in progress, %d done, %d low priority, %d muted",
			s.Immediate, s.Attention, s.InProgress, s.Done, s.LowPriority, s.Muted)
	}
	if method == "tasks" {
		if len(response.Tasks) == 0 {
			p.line("No tasks.")
		} else {
			rows := [][]string{{"ID", "STATE", "ACTIVITY", "TITLE", "REPOSITORY", "REASON"}}
			for _, task := range response.Tasks {
				rows = append(rows, []string{fmt.Sprint(task.ID), humanState(task.DisplayGroup()), string(task.Activity), task.Title, task.Repo, task.Reason})
			}
			p.table(rows)
		}
	}
	if len(response.Sources) > 0 {
		p.line("\nSources:")
		rows := [][]string{{"SOURCE", "STATUS", "ITEMS", "DETAIL"}}
		for _, source := range response.Sources {
			rows = append(rows, []string{source.Name, source.Status, fmt.Sprint(source.SourceRefCount), source.Detail})
		}
		p.table(rows)
	}
	_, err := io.Copy(out, &body)
	return err
}

type textResult struct {
	out      *bytes.Buffer
	warnings *bytes.Buffer
}

func (p textResult) line(format string, args ...any) {
	fmt.Fprintf(p.out, format+"\n", args...)
}

func (p textResult) field(label, value string) {
	if value != "" {
		p.line("%s: %s", label, singleLine(value))
	}
}

func (p textResult) warning(value string) {
	if value != "" {
		fmt.Fprintf(p.warnings, "Warning: %s\n", singleLine(value))
	}
}

func (p textResult) table(rows [][]string) {
	w := tabwriter.NewWriter(p.out, 0, 4, 2, ' ', 0)
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = singleLine(cell)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	_ = w.Flush() // bytes.Buffer writes cannot fail.
}

func (p textResult) task(task protocol.Task) {
	p.line("Task %d: %s", task.ID, singleLine(task.Title))
	p.field("State", humanState(task.Attention))
	p.line("Muted: %s", yesNo(task.Muted))
	p.field("Activity", string(task.Activity))
	p.field("Reason", task.Reason)
	p.field("Repository", task.Repo)
	p.field("URL", task.URL)
	p.field("Completed", task.DoneAt)
}

func (p textResult) plan(plan integration.WorkspaceReconcilePlan) {
	p.field("Workspace", plan.WorkspaceName)
	p.field("Workspace ID", plan.WorkspaceID)
	p.field("Revision", plan.Revision)
	p.field("Next revision", plan.NextRevision)
	p.field("Plan ID", plan.PlanID)
	p.line("Auto-confirm: %s", yesNo(plan.AutoConfirm))
	if len(plan.Changes) == 0 {
		p.line("No changes required.")
	} else {
		p.line("Planned changes (%d):", len(plan.Changes))
		rows := [][]string{{"ACTION", "RESOURCE", "CHANGE"}}
		for _, change := range plan.Changes {
			rows = append(rows, []string{change.Action, change.Resource, change.Summary})
		}
		p.table(rows)
	}
	for _, warning := range plan.Warnings {
		p.warning(warning)
	}
}

func (p textResult) workspace(v integration.WorkspaceContext) {
	p.field("Workspace", v.WorkspaceName)
	p.field("Path", v.WorkspacePath)
	p.field("Workspace ID", v.WorkspaceID)
	p.field("Revision", v.Revision)
	p.line("Registered: %s", yesNo(v.Registered))
	if v.EnrollmentRequired {
		p.line("Workspace enrollment required.")
	}
	p.field("Session", v.SessionName)
	if v.Note != nil {
		p.field("Task note", v.Note.Path)
	}
	if len(v.Members) == 0 {
		p.line("\nNo member worktrees.")
	} else {
		p.line("\nMember worktrees:")
		rows := [][]string{{"BRANCH", "STATE", "PATH", "REPOSITORY"}}
		for _, member := range v.Members {
			state := "clean"
			if member.Dirty {
				state = "dirty"
			}
			rows = append(rows, []string{member.Branch, state, member.Path, member.Repository})
		}
		p.table(rows)
	}
	if v.Sandbox == nil {
		p.line("\nSandbox: none")
	} else {
		p.line("\nSandbox: %s", singleLine(v.Sandbox.Name))
		p.field("Shared directory", v.Sandbox.SharedDirectory)
		p.line("Shared directory ready: %s", yesNo(v.Sandbox.SharedDirectoryReady))
		for _, mount := range v.Sandbox.Mounts {
			p.line("  Mount: %s", singleLine(mount))
		}
		for _, port := range v.Sandbox.Ports {
			p.line("  Port: 127.0.0.1:%d -> %d", port.HostPort, port.SandboxPort)
		}
	}
	p.line("\nAvailable repositories (%d):", len(v.Repositories))
	if len(v.Repositories) > 0 {
		rows := [][]string{{"NAME", "MEMBER", "PATH"}}
		for _, repo := range v.Repositories {
			rows = append(rows, []string{repo.Name, yesNo(repo.AlreadyMember), repo.Path})
		}
		p.table(rows)
	}
}

func singleLine(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}

func humanState(value string) string { return strings.ReplaceAll(value, "_", " ") }
func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
