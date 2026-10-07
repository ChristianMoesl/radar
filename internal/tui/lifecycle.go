package tui

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

// Accepted workspace creations belong to the originating process, not the UI's
// command loop. The model shares this tracker across its value copies.
type creationTracker struct {
	pending sync.WaitGroup
}

func (t *creationTracker) start(action tea.Cmd) tea.Cmd {
	result := make(chan tea.Msg, 1)
	// Register before returning to Bubble Tea: the UI can stop before it even
	// dispatches the returned command, at which point runModel starts waiting.
	t.pending.Add(1)
	go func() {
		defer t.pending.Done()
		// Delivery must not keep creation alive when the UI has gone away.
		result <- action()
	}()
	return func() tea.Msg { return <-result }
}

func (t *creationTracker) wait() {
	t.pending.Wait()
}

func runModel(m model, options ...tea.ProgramOption) error {
	// Own all termination signals together. Bubble Tea's signal handler uses
	// an unguarded send; combining it with a separate hangup Quit can deadlock
	// its shutdown when both popup signals arrive together.
	program := tea.NewProgram(m, append([]tea.ProgramOption{tea.WithAltScreen(), tea.WithoutSignalHandler()}, options...)...)
	hangups := make(chan os.Signal, 3)
	signal.Notify(hangups, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(hangups)

	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		for {
			select {
			case sig := <-hangups:
				if sig == syscall.SIGINT {
					program.Send(tea.InterruptMsg{})
				} else {
					program.Quit()
				}
			case <-stopped:
				return
			}
		}
	}()

	final, err := program.Run()
	// Keep SIGHUP handled until accepted creations finish, even if tmux closes
	// the popup's terminal or Bubble Tea returns an input/terminal error.
	m.creations.wait()
	if result, ok := final.(model); ok && result.relaunch && err == nil {
		return ErrRelaunch
	}
	return err
}

var ErrRelaunch = errors.New("relaunch the installed Radar")
