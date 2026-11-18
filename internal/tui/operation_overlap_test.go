package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func overlappingOperationFixture() model {
	m := operationFixture()
	m.startOperation(m.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", nil)
	m.startOperation(m.tasks[1], "session", "Starting session…", "Session start failed", nil)
	return m
}

func TestCleanupAndActivationKeepIndependentLocalState(t *testing.T) {
	m := overlappingOperationFixture()
	if _, label := m.taskOperationStatus(m.tasks[0]); label != "Cleaning up…" {
		t.Fatalf("activation overwrote cleanup state: %q", label)
	}
	if _, label := m.taskOperationStatus(m.tasks[1]); label != "Starting session…" {
		t.Fatalf("activation lost its own state: %q", label)
	}
	if m.cleanupOperation.id == 0 || m.cleanupOperation.id == m.operation.id {
		t.Fatal("concurrent requests share an identity")
	}
	for _, op := range m.localOperations() {
		before := m
		cmd := m.startOperation(op.task, op.kind, "Replacement", "Failed", nil)
		if cmd != nil || !reflect.DeepEqual(m, before) {
			t.Fatal("duplicate admission replaced an occupied slot")
		}
	}
}

func TestOperationCommandCarriesItsOriginalRequestID(t *testing.T) {
	m := operationFixture()
	result := cleanupPreviewMsg{preview: cleanupFixture()}
	cmd := m.startOperation(m.tasks[0], "cleanup-check", "Checking…", "Check failed", func() tea.Msg { return result })
	id := m.cleanupOperation.id
	m.startOperation(m.tasks[1], "session", "Starting…", "Start failed", nil)
	got := cmd().(tea.BatchMsg)[0]().(operationResultMsg)
	if got.id != id || !reflect.DeepEqual(got.result, result) {
		t.Fatal("result was tagged with a later request or lost its payload")
	}
	m = updateOperation(m, got)
	if m.cleanupOperation.kind != "" || m.operation.kind != "session" || m.cleanupTask.ID != 1 {
		t.Fatal("preview did not retain its initiating identity independently of activation")
	}
}

func TestOperationCompletionOrderKeepsOtherRequestAndFailure(t *testing.T) {
	for _, first := range []string{"cleanup", "session"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-first/fail=%t", first, fail), func(t *testing.T) {
				m := overlappingOperationFixture()
				var problem error
				if fail {
					problem = errors.New("provider unavailable")
				}
				order := []taskOperation{m.cleanupOperation, m.operation}
				if first == "session" {
					order[0], order[1] = order[1], order[0]
				}
				for i, op := range order {
					m = updateOperation(m, operationResultMsg{id: op.id, result: taskActionMsg{action: actionMsg{err: problem}}})
					if m.operationByID(op.id) != nil {
						t.Fatal("completed request retained its spinner")
					}
					if i == 0 {
						other := m.operationByID(order[1].id)
						if other == nil || !reflect.DeepEqual(*other, order[1]) {
							t.Fatal("completion changed the other request")
						}
					}
				}
				if m.hasActiveOperation() || (fail && len(m.taskFailures) != 2) || (!fail && len(m.taskFailures) != 0) {
					t.Fatal("completion order lost a failure or kept a request active")
				}
				if fail {
					for _, op := range order {
						if !strings.Contains(m.operationDetails(op.task, 120), op.failure) {
							t.Fatal("failure moved off its initiating task")
						}
					}
				}
			})
		}
	}
}

func rejectedOperationResults() []tea.Msg {
	response := &protocol.Response{OK: true, Revision: 999, Tasks: []protocol.Task{{ID: 999, Title: "Wrong target"}}}
	return []tea.Msg{
		taskActionMsg{action: actionMsg{response: response, session: "wrong-session", quit: true, refresh: true}},
		cleanupPreviewMsg{preview: cleanupFixture(), response: response},
		workspaceStateMsg{editor: workspaceEditor{active: true, create: integration.ManagedWorkspaceRequest{Name: "Wrong editor"}}},
		workspacePlanMsg{plan: integration.WorkspaceReconcilePlan{PlanID: "wrong-plan"}},
		workspaceAppliedMsg{created: integration.Workspace{Path: "/work/wrong", SessionName: "wrong-session"}, publication: &actionMsg{response: response}},
		taskDeletionPreviewMsg{preview: deletionFixture().deletion.preview},
	}
}

func assertOperationResultIgnored(t *testing.T, m model, msg tea.Msg) {
	t.Helper()
	updated, cmd := m.Update(msg)
	if cmd != nil || !reflect.DeepEqual(updated.(model), m) {
		t.Fatalf("%T changed state, published a snapshot or handed off the terminal", msg)
	}
}

func TestOperationRejectsUntaggedUnknownAndWrongPhaseResults(t *testing.T) {
	for _, kind := range []string{"session", "cleanup", "cleanup-check", "workspace-inspect", "workspace-prepare", "workspace", "task-delete-check", "task-delete"} {
		t.Run(kind, func(t *testing.T) {
			m := operationFixture()
			m.startOperation(m.tasks[0], kind, "Working…", "Failed", nil)
			id := m.operationGeneration
			m.mode, m.message, m.err, m.loading = "detail", "Unrelated notice", errors.New("Unrelated source error"), true
			expected := map[string]int{"session": 0, "cleanup": 0, "cleanup-check": 1, "workspace-inspect": 2, "workspace-prepare": 3, "workspace": 4, "task-delete-check": 5, "task-delete": 0}
			for index, result := range rejectedOperationResults() {
				assertOperationResultIgnored(t, m, result)
				for _, stale := range []uint64{0, id + 1} {
					assertOperationResultIgnored(t, m, operationResultMsg{id: stale, result: result})
				}
				if index != expected[kind] {
					assertOperationResultIgnored(t, m, operationResultMsg{id: id, result: result})
				}
			}
			assertOperationResultIgnored(t, m, operationResultMsg{id: id, result: actionMsg{quit: true}})
		})
	}
}

func TestOperationRejectsDuplicateAndPreviousAttemptResults(t *testing.T) {
	for _, kind := range []string{"cleanup", "session", "workspace"} {
		t.Run(kind, func(t *testing.T) {
			m := operationFixture()
			m.startOperation(m.tasks[0], kind, "Working…", "Failed", nil)
			var result tea.Msg = taskActionMsg{action: actionMsg{err: errors.New("first attempt failed")}}
			if kind == "workspace" {
				result = workspaceAppliedMsg{err: errors.New("first attempt failed")}
			}
			old := operationResultMsg{id: m.operationGeneration, result: result}
			m = updateOperation(m, old)
			assertOperationResultIgnored(t, m, old)
			m.mode = ""
			m.startOperation(m.tasks[0], kind, "Retrying…", "Failed", nil)
			assertOperationResultIgnored(t, m, old)
			assertOperationResultIgnored(t, m, operationTickMsg(old.id))
			if kind == "workspace" {
				result = workspaceAppliedMsg{result: integration.WorkspaceReconcileResult{OK: true}}
			} else {
				result = taskActionMsg{}
			}
			m = updateOperation(m, operationReply(m, result))
			if m.hasActiveOperation() || len(m.taskFailures) != 0 {
				t.Fatal("matching retry success did not clear only its previous failure")
			}
		})
	}
}

func TestCleanupRetryRejectsPreviousPreviewAndWrongPhase(t *testing.T) {
	m := operationFixture()
	m.startOperation(m.tasks[0], "cleanup-check", "Checking…", "Check failed", nil)
	old := operationReply(m, cleanupPreviewMsg{preview: cleanupFixture()})
	m = updateOperation(m, operationReply(m, cleanupPreviewMsg{err: errors.New("inspection unavailable")}))
	m.mode = ""
	m.startOperation(m.tasks[0], "cleanup-check", "Checking again…", "Check failed", nil)
	assertOperationResultIgnored(t, m, old)
	assertOperationResultIgnored(t, m, operationResultMsg{id: m.cleanupOperation.id, result: taskActionMsg{}})
	m = updateOperation(m, operationReply(m, cleanupPreviewMsg{preview: cleanupFixture()}))
	if m.mode != "cleanup_confirm" || m.hasActiveOperation() || len(m.taskFailures) != 0 {
		t.Fatal("fresh inspection did not require a new confirmation")
	}
	m = updateOperation(m, runeKey('y'))
	assertOperationResultIgnored(t, m, old)
	assertOperationResultIgnored(t, m, operationResultMsg{id: m.cleanupOperation.id, result: cleanupPreviewMsg{preview: cleanupFixture()}})
}

func TestOperationTicksAreRequestLocal(t *testing.T) {
	m := overlappingOperationFixture()
	cleanup, activation := m.cleanupOperation, m.operation
	updated, cmd := m.Update(operationTickMsg(cleanup.id))
	m = updated.(model)
	if cmd == nil || m.cleanupOperation.frame != 1 || m.operation.frame != 0 {
		t.Fatal("cleanup tick advanced activation or stopped scheduling")
	}
	m = updateOperation(m, operationTickMsg(activation.id))
	if m.operation.frame != 1 || m.cleanupOperation.frame != 1 {
		t.Fatal("activation tick advanced cleanup")
	}
	m = updateOperation(m, operationResultMsg{id: activation.id, result: taskActionMsg{}})
	assertOperationResultIgnored(t, m, operationTickMsg(activation.id))
	m = updateOperation(m, operationTickMsg(cleanup.id))
	if m.cleanupOperation.frame != 2 {
		t.Fatal("finishing activation stopped cleanup animation")
	}
}

func TestOperationFailuresAreTaskAndKindLocal(t *testing.T) {
	for _, sameTarget := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-target=%t", sameTarget), func(t *testing.T) {
			m := overlappingOperationFixture()
			if sameTarget {
				m.operation.task = m.cleanupOperation.task
			}
			cleanup, activation := m.cleanupOperation, m.operation
			m = updateOperation(m, operationResultMsg{id: cleanup.id, result: taskActionMsg{action: actionMsg{err: errors.New("cleanup failed")}}})
			m = updateOperation(m, operationResultMsg{id: activation.id, result: taskActionMsg{action: actionMsg{err: errors.New("activation failed")}}})
			if len(m.taskFailures) != 2 {
				t.Fatal("one request replaced another kind's failure")
			}
			m.mode = ""
			m.startOperation(activation.task, "session", "Retrying…", "Session start failed", nil)
			m = updateOperation(m, operationReply(m, taskActionMsg{}))
			if len(m.taskFailures) != 1 || m.taskFailures[0].operation.kind != "cleanup" {
				t.Fatal("activation retry cleared cleanup failure")
			}
			m.startOperation(cleanup.task, "cleanup", "Retrying…", "Cleanup failed", nil)
			m = updateOperation(m, operationReply(m, taskActionMsg{}))
			if len(m.taskFailures) != 0 {
				t.Fatal("successful cleanup retry retained its failure")
			}
		})
	}
}

func TestOperationCompletionPreservesUnrelatedRefreshAndError(t *testing.T) {
	m := overlappingOperationFixture()
	cleanup, activation := m.cleanupOperation, m.operation
	problem := errors.New("source refresh unavailable")
	for _, msg := range []tea.Msg{watchMsg{}, fetchMsg{}, actionMsg{}, watchMsg{err: problem}, fetchMsg{err: problem}, actionMsg{err: problem}} {
		m = updateOperation(m, msg)
		if !reflect.DeepEqual(m.cleanupOperation, cleanup) || !reflect.DeepEqual(m.operation, activation) {
			t.Fatal("unrelated update finished or changed a local request")
		}
	}
	m.loading = true
	m = updateOperation(m, operationResultMsg{id: activation.id, result: taskActionMsg{}})
	m = updateOperation(m, operationResultMsg{id: cleanup.id, result: taskActionMsg{}})
	if m.err != problem || !m.loading {
		t.Fatal("resource completion cleared unrelated error or pending refresh")
	}
	// A standalone editor has no task row, but its successful result must not
	// clear a source error which arrived after admission either.
	m.startOperation(protocol.Task{}, "workspace", "Updating…", "Update failed", nil)
	m.err = problem
	m = updateOperation(m, operationReply(m, workspaceAppliedMsg{result: integration.WorkspaceReconcileResult{OK: true}}))
	if m.err != problem || !m.loading {
		t.Fatal("standalone success cleared unrelated refresh state")
	}
}

func TestBothOperationStatusesSurviveHiddenRows(t *testing.T) {
	m := overlappingOperationFixture()
	for _, width := range []int{40, 120} {
		m.width = width
		m.searchQuery = "no matching tasks"
		view := ansi.Strip(m.View())
		for _, label := range []string{"Cleaning up…", "Starting session…"} {
			if !strings.Contains(view, label) {
				t.Fatalf("hidden operation lost status %q at width %d", label, width)
			}
		}
		assertNoWideLines(t, m.View(), width)
	}
}

func TestOperationAdmissionRemainsSerializedAcrossBothSlots(t *testing.T) {
	for _, kind := range []string{"cleanup", "cleanup-check", "session"} {
		m := operationFixture()
		m.startOperation(m.tasks[0], kind, "Working…", "Failed", nil)
		m.cursor = 1
		for _, key := range []tea.KeyMsg{runeKey('w'), runeKey('x'), runeKey('c'), runeKey('D'), {Type: tea.KeyEnter}} {
			before := m.operationGeneration
			updated, cmd := m.Update(key)
			m = updated.(model)
			if cmd != nil || m.operationGeneration != before {
				t.Fatalf("%s admitted %s before backend overlap safety was established", kind, key.String())
			}
		}
	}
}

func TestReversedCompletionThroughBubbleTeaCommandLoop(t *testing.T) {
	for _, first := range []string{"cleanup", "session"} {
		t.Run(first+"-first", func(t *testing.T) {
			m := operationFixture()
			cleanupResult, activationResult := make(chan actionMsg, 1), make(chan actionMsg, 1)
			defer close(cleanupResult)
			defer close(activationResult)
			cleanup := m.startOperation(m.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", taskAction(func() tea.Msg { return <-cleanupResult }))
			activation := m.startOperation(m.tasks[1], "session", "Starting session…", "Session start failed", taskAction(func() tea.Msg { return <-activationResult }))
			tm := teatest.NewTestModel(t, asynchronousOperationModel{staticTUIModel: staticTUIModel{model: m}, initial: tea.Batch(cleanup, activation)}, teatest.WithInitialTermSize(120, 30))
			waitFor := func(text string) {
				t.Helper()
				teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
					return strings.Contains(ansi.Strip(string(output)), text)
				}, teatest.WithDuration(2*time.Second))
			}
			waitFor("Starting session…")
			waitFor("Cleaning up…")
			tm.Send(tea.KeyMsg{Type: tea.KeyDown})
			if first == "cleanup" {
				cleanupResult <- actionMsg{err: errors.New("cleanup provider failed")}
				waitFor("Cleanup failed")
				waitFor("Starting session…")
				activationResult <- actionMsg{err: errors.New("session provider failed")}
				waitFor("Session start failed")
			} else {
				activationResult <- actionMsg{err: errors.New("session provider failed")}
				waitFor("Session start failed")
				waitFor("Cleaning up…")
				cleanupResult <- actionMsg{err: errors.New("cleanup provider failed")}
				waitFor("Cleanup failed")
			}
			tm.Send(runeKey('q'))
			final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(staticTUIModel).model
			if final.hasActiveOperation() || len(final.taskFailures) != 2 || final.taskFailures[0].operation.kind != first {
				t.Fatal("real command loop lost request-local completion/failure state")
			}
		})
	}
}

func TestReversedOperationSnapshotsRespectRevision(t *testing.T) {
	for _, first := range []string{"cleanup", "session"} {
		t.Run(first+"-first", func(t *testing.T) {
			m := overlappingOperationFixture()
			order := []taskOperation{m.cleanupOperation, m.operation}
			if first == "session" {
				order[0], order[1] = order[1], order[0]
			}
			newer := protocol.Response{Revision: 20, Tasks: m.tasks[1:]}
			older := protocol.Response{Revision: 19, Tasks: m.tasks}
			m = updateOperation(m, operationResultMsg{id: order[0].id, result: taskActionMsg{action: actionMsg{response: &newer}}})
			m = updateOperation(m, operationResultMsg{id: order[1].id, result: taskActionMsg{action: actionMsg{response: &older}}})
			if m.hasActiveOperation() || m.revision != 20 || len(m.tasks) != 1 || m.tasks[0].ID != 2 {
				t.Fatal("reversed completion resurrected an old snapshot or lost the remaining request")
			}
		})
	}
}

func TestWorkspacePhaseRejectsPreviousPhaseResult(t *testing.T) {
	m := operationFixture()
	m.editor = workspaceEditor{active: true, task: m.tasks[0], create: integration.ManagedWorkspaceRequest{Name: "New"}}
	next, _ := m.previewWorkspace()
	m = next.(model)
	plan := workspacePlanMsg{plan: integration.WorkspaceReconcilePlan{PlanID: "current", Changes: []integration.WorkspaceChange{{Action: "add", Resource: "workspace"}}}}
	previous := operationReply(m, plan)
	m = updateOperation(m, previous)
	if m.operation.kind != "workspace" || m.operation.id == previous.id {
		t.Fatal("validated creation did not start a new apply request")
	}
	assertOperationResultIgnored(t, m, previous)
	assertOperationResultIgnored(t, m, operationResultMsg{id: m.operation.id, result: plan})
}
