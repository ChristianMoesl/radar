package tui

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"radar/internal/protocol"
)

func TestCleanupFailureShowsUnderlyingProblemAndRequiresFreshConfirmation(t *testing.T) {
	m := operationFixture()
	m = updateOperation(m, runeKey('x'))
	m = updateOperation(m, cleanupPreviewMsg{err: errors.New("git worktree /work/selected: inspection unavailable; refresh Radar and retry cleanup")})
	if m.mode != "cleanup_error" || !strings.Contains(m.View(), "inspection unavailable") || !strings.Contains(m.View(), "/work/selected") || !strings.Contains(m.View(), "Refresh and retry") {
		t.Fatalf("failure hidden: %s", m.View())
	}
	for _, key := range []tea.KeyMsg{runeKey('y'), {Type: tea.KeyEnter}} {
		next, cmd := m.Update(key)
		if cmd != nil || next.(model).mode != "cleanup_error" {
			t.Fatal("failure screen allowed unpreviewed deletion")
		}
	}
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != "" || len(m.taskFailures) != 1 {
		t.Fatal("back discarded failure/recovery context")
	}
}

func TestCleanupRetryRefreshesAndRepreviewsTheOriginalIdentity(t *testing.T) {
	dir, err := os.MkdirTemp("", "radar-cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	m := operationFixture()
	origin := m.tasks[0]
	replacement := origin
	replacement.ID = 99
	m.socketPath = socket
	m = updateOperation(m, runeKey('x'))
	m = updateOperation(m, cleanupPreviewMsg{err: errors.New("failed resource check")})
	finished := make(chan error, 1)
	go func() {
		for _, method := range []string{"refresh", "cleanup-preview"} {
			conn, err := listener.Accept()
			if err != nil {
				finished <- err
				return
			}
			var req protocol.Request
			err = json.NewDecoder(conn).Decode(&req)
			if err != nil || req.Method != method || (method == "cleanup-preview" && req.TaskID != 99) {
				conn.Close()
				finished <- errors.New("retry replayed a stale plan or checked the wrong task")
				return
			}
			response := protocol.Response{OK: true, Tasks: []protocol.Task{replacement}}
			if method == "cleanup-preview" {
				preview := cleanupFixture()
				preview.TaskID = 99
				response.CleanupPreview = &preview
			}
			err = json.NewEncoder(conn).Encode(response)
			conn.Close()
			if err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	next, cmd := m.Update(runeKey('r'))
	m = next.(model)
	if cmd == nil || m.operation.kind != "cleanup-check" {
		t.Fatal("retry did not start a new resource check")
	}
	msg := cmd().(tea.BatchMsg)[0]()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	m = updateOperation(m, msg)
	if m.mode != "cleanup_confirm" || m.cleanup.TaskID != 99 || len(m.taskFailures) != 0 {
		t.Fatalf("retry did not require a new confirmation: %+v", m)
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter confirmed destructive retry")
	}
}

func TestPartialCleanupFailureShowsRemainingResources(t *testing.T) {
	m := operationFixture()
	origin := m.tasks[0]
	m.startOperation(origin, "cleanup", "Cleaning up…", "Cleanup failed", nil)
	problem := errors.New("cleanup stopped after 1 of 3 resources: sandbox remove failed; remaining resources: sbx sandbox selected, workspace workspace /work/selected; refresh Radar and preview cleanup again to retry")
	m = updateOperation(m, taskActionMsg{action: actionMsg{err: problem}})
	if m.mode != "cleanup_error" || !strings.Contains(m.View(), "remaining resources") || !strings.Contains(m.View(), "selected") {
		t.Fatalf("partial failure hidden or reported as success: %s", m.View())
	}
}

func TestCleanupFailureInspectAndScrollingRemainRecoverable(t *testing.T) {
	m := operationFixture()
	m.width, m.height = 40, 18
	m = updateOperation(m, runeKey('x'))
	m = updateOperation(m, cleanupPreviewMsg{err: errors.New(strings.Repeat("worktree inspection failed; ", 30))})
	assertNoWideLines(t, m.View(), 40)
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyEnd})
	if m.cleanupScroll == 0 || !strings.Contains(m.View(), "Refresh and retry") {
		t.Fatal("long error hides recovery actions")
	}
	m = updateOperation(m, runeKey('i'))
	if m.mode != "detail" || !strings.Contains(m.View(), "Resource check failed") {
		t.Fatal("inspection lost failing resource")
	}
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != "cleanup_error" {
		t.Fatal("inspection did not return to recovery")
	}
}

func TestCleanupRetryDoesNotUseUnrelatedSameTitleTask(t *testing.T) {
	dir, err := os.MkdirTemp("", "radar-cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	m := operationFixture()
	origin := m.tasks[0]
	unrelated := protocol.Task{ID: 99, Title: origin.Title, SourceRefs: []protocol.SourceRef{{ID: "workspace:other", Source: "workspace"}}}
	m.socketPath = socket
	finished := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			finished <- err
			return
		}
		defer conn.Close()
		var req protocol.Request
		if err = json.NewDecoder(conn).Decode(&req); err == nil {
			err = json.NewEncoder(conn).Encode(protocol.Response{OK: true, Tasks: []protocol.Task{unrelated}})
		}
		finished <- err
	}()
	msg := m.retryCleanup(origin)().(cleanupPreviewMsg)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if msg.err == nil || msg.response == nil || msg.preview.TaskID != 0 || !strings.Contains(msg.err.Error(), "no longer") {
		t.Fatalf("retry changed identity: %+v", msg)
	}
}
