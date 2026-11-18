package tui

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"radar/internal/integration"
)

type foregroundTestModel struct {
	initial tea.Cmd
	result  actionMsg
}

func (m foregroundTestModel) Init() tea.Cmd { return m.initial }
func (m foregroundTestModel) View() string  { return "" }
func (m foregroundTestModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if result, ok := msg.(actionMsg); ok {
		m.result = result
		return m, tea.Quit
	}
	return m, nil
}
func runForeground(t *testing.T, initial tea.Cmd) actionMsg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p := tea.NewProgram(foregroundTestModel{initial: initial}, tea.WithContext(ctx), tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	final, err := p.Run()
	if err != nil {
		t.Fatal(err)
	}
	return final.(foregroundTestModel).result
}
func TestForegroundAttachmentReleasesOnlyAfterExecutorOutcome(t *testing.T) {
	for _, outcome := range []string{"detach", "process-failure", "start-failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			process := exec.CommandContext(ctx, "sh", "-c", "exit 0")
			switch outcome {
			case "process-failure":
				process = exec.CommandContext(ctx, "sh", "-c", "exit 7")
			case "start-failure":
				process = exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-executable"))
			case "cancelled":
				cancel()
			}
			releases := 0
			attachment := integration.SessionAttachment{Command: process, Validate: func() error { return ctx.Err() }, Release: func() {
				releases++
				if outcome != "start-failure" && outcome != "cancelled" && process.ProcessState == nil {
					t.Error("admission released before foreground exit")
				}
			}}
			command := attachSession(attachment)
			if releases != 0 {
				t.Fatal("constructor released foreground admission")
			}
			result := runForeground(t, command)
			if releases != 1 || (result.err != nil) != (outcome != "detach") || result.refresh != (outcome == "detach") {
				t.Fatal("executor callback lost release/outcome", releases, result)
			}
		})
	}
}
