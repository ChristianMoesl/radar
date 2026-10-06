package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"

	"radar/internal/integration"
)

func TestCreationTrackerDrainsAbandonedDelivery(t *testing.T) {
	tracker := &creationTracker{}
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	// Never execute the returned command. Creation must have been registered
	// and started already, and its result must not need a receiver to finish.
	tracker.start(func() tea.Msg {
		close(started)
		<-release
		return workspaceAppliedMsg{}
	})
	waitForLifecycle(t, started)

	drained := make(chan struct{})
	go func() {
		tracker.wait()
		close(drained)
	}()
	assertLifecyclePending(t, drained)
	release <- struct{}{}
	waitForLifecycle(t, drained)
}

func TestCreationTrackerAlreadyCompleteRetainsSuccessAndError(t *testing.T) {
	for _, problem := range []error{nil, errors.New("sandbox setup failed")} {
		t.Run(fmt.Sprintf("error=%v", problem), func(t *testing.T) {
			tracker := &creationTracker{}
			want := workspaceAppliedMsg{created: integration.Workspace{Path: "/work/ABC-123"}, err: problem}
			delivery := tracker.start(func() tea.Msg { return want })
			tracker.wait()
			// Waiting again after completion must not require UI delivery.
			tracker.wait()
			if got := delivery().(workspaceAppliedMsg); !reflect.DeepEqual(got, want) {
				t.Fatalf("creation result = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCreationTrackerIsSharedAcrossModelUpdates(t *testing.T) {
	m := newModel(filepath.Join(t.TempDir(), "missing.sock"))
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	next := updated.(model)
	if next.creations == nil || next.creations != m.creations {
		t.Fatal("model update lost the process-owned creation tracker")
	}

	release := make(chan struct{})
	defer close(release)
	next.creations.start(func() tea.Msg {
		<-release
		return workspaceAppliedMsg{}
	})
	drained := make(chan struct{})
	go func() {
		m.creations.wait()
		close(drained)
	}()
	assertLifecyclePending(t, drained)
	release <- struct{}{}
	waitForLifecycle(t, drained)
}

func TestRunModelPreservesEntryPointState(t *testing.T) {
	for _, mode := range []string{"", "workspace_name", "create_base", "fork_member"} {
		t.Run(mode, func(t *testing.T) {
			m := newModel(filepath.Join(t.TempDir(), "missing.sock"))
			m.mode = mode
			m.editor = workspaceEditor{active: true, create: integration.ManagedWorkspaceRequest{Name: "ABC-123"}}
			m.create = createForm{name: "ABC-123", forkPiSession: "origin-session", branchMode: integration.WorkspaceBranchNew}
			var observed model
			err := runModel(m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithFilter(func(current tea.Model, msg tea.Msg) tea.Msg {
				if _, ok := msg.(fetchMsg); ok {
					observed = current.(model)
					return tea.QuitMsg{}
				}
				return msg
			}))
			if err != nil {
				t.Fatal(err)
			}
			if observed.mode != m.mode || !reflect.DeepEqual(observed.editor, m.editor) || !reflect.DeepEqual(observed.create, m.create) || observed.creations != m.creations {
				t.Fatal("common runner reset the entry point's form or lifetime state")
			}
		})
	}
}

func TestRunModelDrainsCreationAfterUIQuit(t *testing.T) {
	m := newModel(filepath.Join(t.TempDir(), "missing.sock"))
	release := make(chan struct{})
	defer close(release)
	m.creations.start(func() tea.Msg {
		<-release
		return workspaceAppliedMsg{}
	})
	quit, returned := make(chan struct{}), make(chan struct{})
	var runErr error
	go func() {
		runErr = runModel(m, tea.WithInput(strings.NewReader("q")), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(tea.QuitMsg); ok {
				close(quit)
			}
			return msg
		}))
		close(returned)
	}()
	waitForLifecycle(t, quit)
	assertLifecyclePending(t, returned)
	release <- struct{}{}
	waitForLifecycle(t, returned)
	if runErr != nil {
		t.Fatal(runErr)
	}
}

func TestRunModelDrainsCreationAfterUIError(t *testing.T) {
	m := newModel(filepath.Join(t.TempDir(), "missing.sock"))
	release := make(chan struct{})
	defer close(release)
	m.creations.start(func() tea.Msg {
		<-release
		return workspaceAppliedMsg{err: errors.New("creation failed")}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	returned := make(chan struct{})
	var runErr error
	go func() {
		runErr = runModel(m, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
		close(returned)
	}()
	assertLifecyclePending(t, returned)
	release <- struct{}{}
	waitForLifecycle(t, returned)
	if !errors.Is(runErr, tea.ErrProgramKilled) || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("UI error was not preserved: %v", runErr)
	}
}

func TestRunModelQuitsOnHangupAndKeepsHandlerWhileDraining(t *testing.T) {
	m := newModel(filepath.Join(t.TempDir(), "missing.sock"))
	release := make(chan struct{})
	defer close(release)
	m.creations.start(func() tea.Msg {
		<-release
		return workspaceAppliedMsg{}
	})
	ready, quit, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var runErr error
	go func() {
		runErr = runModel(m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			switch msg.(type) {
			case fetchMsg:
				close(ready)
			case tea.QuitMsg:
				close(quit)
			}
			return msg
		}))
		close(returned)
	}()
	waitForLifecycle(t, ready)
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitForLifecycle(t, quit)
	assertLifecyclePending(t, returned)
	// A second popup hangup while the UI is closed must still be caught.
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	assertLifecyclePending(t, returned)
	release <- struct{}{}
	waitForLifecycle(t, returned)
	if runErr != nil {
		t.Fatal(runErr)
	}
}

func TestTrackedCreationPreservesTaskOperationStateWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := operationFixture()
	m.creations = &creationTracker{}
	m.socketPath = filepath.Join(t.TempDir(), "missing.sock")
	m.editor = workspaceEditor{active: true, task: m.tasks[0]}
	result := make(chan tea.Msg, 1)
	defer close(result)
	cmd := m.startOperation(m.tasks[0], "workspace", "Creating workspace…", "Workspace creation failed", m.creations.start(func() tea.Msg { return <-result }))
	tm := teatest.NewTestModel(t, asynchronousOperationModel{staticTUIModel: staticTUIModel{model: m}, initial: cmd}, teatest.WithInitialTermSize(120, 30))
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
		return strings.Contains(ansi.Strip(string(output)), "Creating workspace…")
	}, teatest.WithDuration(time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	result <- workspaceAppliedMsg{err: errors.New("sandbox setup failed")}
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
		return strings.Contains(ansi.Strip(string(output)), "Workspace creation failed")
	}, teatest.WithDuration(time.Second))
	tm.Send(runeKey('q'))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
	if final.creations != m.creations || final.cursor != 1 || final.editor.active || final.operation.kind != "" || len(final.taskFailures) != 1 || final.taskFailures[0].operation.task.ID != 1 || final.taskFailures[0].err.Error() != "sandbox setup failed" {
		t.Fatal("owned creation changed UI selection, target, or failure semantics")
	}
	final.creations.wait()
}

func waitForLifecycle(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not finish")
	}
}

func assertLifecyclePending(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("accepted creation was not drained")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRunModelDrainsWhenPopupSendsBothTerminationSignals(t *testing.T) {
	for _, first := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(first.String(), func(t *testing.T) {
			m := newModel(filepath.Join(t.TempDir(), "missing.sock"))
			release := make(chan struct{})
			defer close(release)
			m.creations.start(func() tea.Msg { <-release; return workspaceAppliedMsg{} })
			ready, quit, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var runErr error
			go func() {
				runErr = runModel(m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
					switch msg.(type) {
					case fetchMsg:
						close(ready)
					case tea.QuitMsg:
						close(quit)
					}
					return msg
				}))
				close(returned)
			}()
			waitForLifecycle(t, ready)
			second := syscall.SIGHUP
			if first == syscall.SIGHUP {
				second = syscall.SIGTERM
			}
			if err := syscall.Kill(os.Getpid(), first); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(os.Getpid(), second); err != nil {
				t.Fatal(err)
			}
			waitForLifecycle(t, quit)
			assertLifecyclePending(t, returned)
			// TERM must remain handled after the UI stops, not just HUP.
			if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			assertLifecyclePending(t, returned)
			release <- struct{}{}
			waitForLifecycle(t, returned)
			if runErr != nil {
				t.Fatal(runErr)
			}
		})
	}
}
