package tui

import (
	"context"
	"errors"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"radar/internal/protocol"
)

// Observe the unmodified View and Bubble Tea's real parser, command loop and
// default renderer. This is a controlled pipe/output boundary, not OS keyboard
// delivery or a terminal emulator/paint measurement. Timeouts gate liveness;
// the provisional p95 budgets are reported, not flaky wall-clock CI assertions.
type responsivenessState struct {
	key                               string
	entered, applied                  time.Time
	revision                          int64
	selectedID, scroll, failureTaskID int
	operationID                       uint64
	mode, query, marker               string
}
type responsivenessView struct {
	revision   int64
	start, end time.Time
}
type responsivenessObserver struct {
	mu       sync.Mutex
	watches  []responsivenessState
	views    []responsivenessView
	keys     chan responsivenessState
	terminal chan responsivenessState
	notify   chan struct{}
}
type responsivenessModel struct {
	model
	observer *responsivenessObserver
	initial  tea.Cmd
}

func (m responsivenessModel) Init() tea.Cmd { return m.initial }
func (m responsivenessModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	entered := time.Now()
	next, cmd := m.model.Update(msg)
	m.model = next.(model)
	state := responsivenessState{entered: entered, applied: time.Now(), revision: m.revision, scroll: m.scroll, operationID: m.cleanupOperation.id, mode: m.mode, query: m.searchQuery}
	if m.selectedSection == "" && m.cursor >= 0 && m.cursor < len(m.tasks) {
		state.selectedID = m.tasks[m.cursor].ID
	}
	if len(m.taskFailures) > 0 {
		state.failureTaskID = m.taskFailures[0].operation.task.ID
	}
	switch v := msg.(type) {
	case tea.KeyMsg:
		state.key = v.String()
		m.observer.keys <- state
	case watchMsg:
		if len(v.response.Operations) > 0 {
			state.marker = v.response.Operations[len(v.response.Operations)-1].ID
		}
		m.observer.mu.Lock()
		m.observer.watches = append(m.observer.watches, state)
		m.observer.mu.Unlock()
		select {
		case m.observer.notify <- struct{}{}:
		default:
		}
	}
	if state.operationID == 0 && state.failureTaskID != 0 {
		select {
		case m.observer.terminal <- state:
		default:
		}
	}
	return m, cmd
}
func (m responsivenessModel) View() string {
	start := time.Now()
	view := m.model.View()
	end := time.Now()
	m.observer.mu.Lock()
	m.observer.views = append(m.observer.views, responsivenessView{revision: m.revision, start: start, end: end})
	m.observer.mu.Unlock()
	return view
}

type responsivenessFrame struct {
	match func(string) bool
	bytes []byte
	done  chan time.Time
}
type responsivenessOutput struct {
	mu        sync.Mutex
	pending   []*responsivenessFrame
	recent    []byte
	lastWrite time.Time
}

func (w *responsivenessOutput) arm(match func(string) bool) <-chan time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := &responsivenessFrame{match: match, done: make(chan time.Time, 1)}
	w.pending = append(w.pending, p)
	return p.done
}
func (w *responsivenessOutput) published(text string, since time.Time) <-chan time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := &responsivenessFrame{match: responsivenessContains(text), done: make(chan time.Time, 1)}
	if w.lastWrite.After(since) && p.match(ansi.Strip(string(w.recent))) {
		p.done <- w.lastWrite
	} else {
		w.pending = append(w.pending, p)
	}
	return p.done
}
func (w *responsivenessOutput) Write(data []byte) (int, error) {
	at := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastWrite = at
	w.recent = append(w.recent, data...)
	if len(w.recent) > 8192 {
		w.recent = slices.Clone(w.recent[len(w.recent)-8192:])
	}
	var remaining []*responsivenessFrame
	for _, p := range w.pending {
		p.bytes = append(p.bytes, data...)
		if len(p.bytes) > 65536 {
			p.bytes = slices.Clone(p.bytes[len(p.bytes)-65536:])
		}
		if p.match(ansi.Strip(string(p.bytes))) {
			p.done <- at
		} else {
			remaining = append(remaining, p)
		}
	}
	w.pending = remaining
	return len(data), nil
}
func responsivenessContains(text string) func(string) bool {
	return func(output string) bool { return strings.Contains(output, text) }
}
func responsivenessSelected(prefix string) func(string) bool {
	return func(output string) bool {
		for _, line := range strings.Split(output, "\n") {
			if i := strings.Index(line, "› "); i >= 0 && strings.Contains(line[i:], prefix) {
				return true
			}
		}
		return false
	}
}
func responsivenessAwait[T any](t testing.TB, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		stack := make([]byte, 262144)
		stack = stack[:runtime.Stack(stack, true)]
		t.Fatalf("responsiveness fixture failed to make progress:\n%s", stack)
		var zero T
		return zero
	}
}

func TestResponsivenessNativeLoopUnderStall(t *testing.T) {
	f := newReactivityBaselineFixture(t)
	base := reactivityBaselineModel()
	base.cleanupOperation = taskOperation{}
	base.socketPath = f.model.socketPath
	// Include the baseline's real registered anchor plus 199 synthetic anchors.
	// Native publication assigns stable record IDs: seed from its actual snapshot,
	// never assume fixture input IDs survive server projection.
	f.store.SetTasks(append(slices.Clone(base.tasks[:199]), f.model.rawTasks...))
	base.applyResponse(f.service.ResourceSnapshot(), false)
	if len(base.tasks) != 200 {
		t.Fatalf("native fixture has %d tasks, want 200", len(base.tasks))
	}
	indexOf := func(prefix string) int {
		for i, task := range base.tasks {
			if strings.HasPrefix(task.Title, prefix) {
				return i
			}
		}
		t.Fatalf("native fixture missing %s", prefix)
		return -1
	}
	base.cursor = indexOf("ABC-150 Workspace")
	selectedID := base.tasks[base.cursor].ID
	nextID := base.tasks[indexOf("ABC-151 Workspace")].ID
	initiatingID := base.tasks[0].ID
	base.sourcesExpanded = true
	base.syncTaskScroll()
	originalScroll := base.scroll

	// An actual workspace collection is parked after observing its provider. It
	// cannot finish merely because the user navigates or newer state is served.
	providerStarted, providerRelease, providerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.source.afterCollect = func(ctx context.Context) {
		close(providerStarted)
		select {
		case <-providerRelease:
		case <-ctx.Done():
		}
	}
	providerContext, cancelProvider := context.WithCancel(context.Background())
	go func() { defer close(providerDone); f.service.Refresh(providerContext, true) }()
	responsivenessAwait(t, providerStarted)
	defer func() { cancelProvider(); close(providerRelease); responsivenessAwait(t, providerDone) }()

	cleanupRelease := make(chan struct{})
	var releaseCleanup sync.Once
	defer releaseCleanup.Do(func() { close(cleanupRelease) })
	initial := base.startOperation(base.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", taskAction(func() tea.Msg {
		<-cleanupRelease
		response := f.service.ResourceSnapshot()
		return actionMsg{err: errors.New("fixture cleanup failure"), response: &response}
	}))
	operationID := base.cleanupOperation.id
	observer := &responsivenessObserver{keys: make(chan responsivenessState, 8), terminal: make(chan responsivenessState, 1), notify: make(chan struct{}, 1)}
	output := &responsivenessOutput{}
	ready := output.arm(responsivenessSelected("ABC-150"))
	input, keyboard := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	program := tea.NewProgram(responsivenessModel{model: base, observer: observer, initial: tea.Batch(initial, base.watch(base.revision))}, tea.WithInput(input), tea.WithOutput(output), tea.WithContext(ctx), tea.WithAltScreen(), tea.WithoutSignalHandler())
	type result struct {
		model tea.Model
		err   error
	}
	stopped := make(chan result, 1)
	go func() { m, err := program.Run(); stopped <- result{m, err} }()
	var publishers sync.WaitGroup
	defer publishers.Wait()
	collected := false
	defer func() {
		cancel()
		releaseCleanup.Do(func() { close(cleanupRelease) })
		_ = keyboard.Close()
		_ = input.Close()
		if !collected {
			responsivenessAwait(t, stopped)
		}
	}()
	program.Send(tea.WindowSizeMsg{Width: 120, Height: 40})
	responsivenessAwait(t, ready)

	var streaming atomic.Bool
	overlap := 0
	sendKey := func(name, bytes string, match func(string) bool) responsivenessState {
		t.Helper()
		var frame <-chan time.Time
		if match != nil {
			frame = output.arm(match)
		}
		start := time.Now()
		written := make(chan error, 1)
		go func() { _, err := io.WriteString(keyboard, bytes); written <- err }()
		if err := responsivenessAwait(t, written); err != nil {
			t.Fatal(err)
		}
		ack := responsivenessAwait(t, observer.keys)
		if ack.key != name || ack.operationID != operationID || ack.failureTaskID != 0 {
			t.Fatalf("input changed operation/error identity: %+v", ack)
		}
		if match == nil {
			if ack.mode != "" || ack.query != "" {
				t.Fatalf("Escape did not leave its read-only mode: %+v", ack)
			}
			t.Logf("RESP_ACK key=%s queue=%s update=%s", name, ack.entered.Sub(start), ack.applied.Sub(ack.entered))
			return ack
		}
		var painted time.Time
		select {
		case painted = <-frame:
		case <-time.After(5 * time.Second):
			output.mu.Lock()
			recent := ansi.Strip(string(output.recent))
			output.mu.Unlock()
			t.Fatalf("key %q acknowledged but frame did not reflect state %+v; recent output: %s", name, ack, recent)
		}
		if streaming.Load() {
			overlap++
		}
		t.Logf("RESP_INPUT key=%s queue=%s update=%s output=%s", name, ack.entered.Sub(start), ack.applied.Sub(ack.entered), painted.Sub(start))
		return ack
	}

	waitRevision := func(want int64) responsivenessState {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			observer.mu.Lock()
			var latest responsivenessState
			if len(observer.watches) > 0 {
				latest = observer.watches[len(observer.watches)-1]
			}
			observer.mu.Unlock()
			if latest.revision >= want {
				return latest
			}
			select {
			case <-observer.notify:
			case <-timer.C:
				t.Fatalf("native watch did not reach revision %d, last %+v", want, latest)
			}
		}
	}
	for sample := 0; sample < 20; sample++ {
		publicationStarted, publicationDone := make(chan struct{}), make(chan struct{})
		burstStop := make(chan struct{})
		var finalRevision int64
		streaming.Store(true)
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			defer close(publicationDone)
			defer streaming.Store(false)
			for n := 0; n < 1024; n++ {
				select {
				case <-ctx.Done():
					return
				case <-burstStop:
					return
				default:
				}
				f.service.MarkResourceRefreshPending()
				if n == 0 {
					close(publicationStarted)
				}
				finalRevision = f.store.Revision()
				time.Sleep(5 * time.Millisecond)
			}
		}()

		responsivenessAwait(t, publicationStarted)
		down := sendKey("j", "j", responsivenessSelected("ABC-151"))
		up := sendKey("k", "k", responsivenessSelected("ABC-150"))
		if down.selectedID != nextID || up.selectedID != selectedID || up.scroll < originalScroll {
			t.Fatalf("burst moved focus/scroll unexpectedly: down=%+v up=%+v", down, up)
		}
		sendKey("/", "/", responsivenessContains("/ ▏"))
		search := sendKey("W", "W", responsivenessContains("/ W▏"))
		if search.mode != "search" || search.query != "W" {
			t.Fatalf("search acknowledgement lost: %+v", search)
		}
		sendKey("esc", "\x1b", nil)

		detail := sendKey("i", "i", responsivenessContains("Inspect · ABC-150"))
		if detail.mode != "detail" || detail.selectedID != selectedID {
			t.Fatalf("Inspect lost pinned task: %+v", detail)
		}
		// Native daemon operation IDs are visible in Inspect and are volatile. They
		// qualify publication/watch/application/frame timing without confusing
		// durable source-status fsync time with Bubble Tea scheduling.
		if !streaming.Load() {
			t.Fatal("bounded revision burst ended before publication probe")
		}
		publicationStart := time.Now()
		id := f.store.BeginOperation("cleanup", selectedID, "")
		publicationReturn := time.Now()
		rendered := responsivenessAwait(t, output.published("Operation: "+id, publicationStart))
		observer.mu.Lock()
		var last responsivenessState
		for _, v := range observer.watches {
			if v.marker == id {
				last = v
				break
			}
		}
		var view time.Duration
		for _, v := range observer.views {
			if v.revision == last.revision && !v.start.Before(last.applied) {
				view = v.end.Sub(v.start)
				break
			}
		}
		observer.mu.Unlock()
		if last.revision == 0 || last.operationID != operationID || view <= 0 {
			t.Fatalf("native operation publication lost identity: %+v", last)
		}
		t.Logf("RESP_PUBLICATION sample=%d publication-call=%s entry-to-watch=%s update=%s view=%s entry-to-output=%s", sample, publicationReturn.Sub(publicationStart), last.entered.Sub(publicationStart), last.applied.Sub(last.entered), view, rendered.Sub(publicationStart))
		close(burstStop)
		responsivenessAwait(t, publicationDone)
		latest := waitRevision(finalRevision)
		if latest.operationID != operationID {
			t.Fatal("revision burst finished the parked local operation")
		}
		f.store.FinishOperation(id, protocol.ManagedOperation{Status: "failed"})
		sendKey("esc", "\x1b", nil)

		select {
		case <-providerDone:
			t.Fatal("provider stopped being deliberately stalled")
		default:
		}
	}
	if overlap == 0 {
		t.Fatal("no input acknowledged while the native revision burst was running")
	}
	sendKey("enter", "\r", responsivenessContains("Retry Enter when finished"))

	// Complete the correlated command, then prove a valid newer native snapshot
	// cannot erase its failure or attach it to the currently selected task B.
	releaseCleanup.Do(func() { close(cleanupRelease) })
	failure := responsivenessAwait(t, observer.terminal)
	if failure.failureTaskID != initiatingID {
		t.Fatalf("failure moved away from task A: %+v", failure)
	}
	f.service.MarkResourceRefreshPending()
	retained := waitRevision(f.store.Revision())
	if retained.failureTaskID != initiatingID || retained.selectedID != selectedID {
		t.Fatalf("native revision lost correlated failure/focus: %+v", retained)
	}
	program.Send(tea.QuitMsg{})
	end := responsivenessAwait(t, stopped)
	collected = true
	if end.err != nil {
		t.Fatal(end.err)
	}
	m := end.model.(responsivenessModel).model
	if m.cleanupOperation.id != 0 || len(m.taskFailures) != 1 || m.taskFailures[0].operation.id != operationID || m.taskFailures[0].operation.task.ID != initiatingID || m.tasks[m.cursor].ID != selectedID {
		t.Fatal("terminal failure, operation or selected identity changed across native revisions")
	}
	if len(m.managedOperations) != 20 {
		t.Fatalf("native operation history lost intermediate outcomes: %d", len(m.managedOperations))
	}
	seen := map[string]bool{}
	for _, op := range m.managedOperations {
		if op.ID == "" || seen[op.ID] || op.TaskID != selectedID || op.Kind != "cleanup" || op.Status != "failed" {
			t.Fatal("native burst lost ordered operation identity or error outcome")
		}
		seen[op.ID] = true
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	var revision int64
	for _, v := range observer.watches {
		if v.revision < revision {
			t.Fatal("native revision stream moved backward")
		}
		revision = v.revision
	}
	for _, v := range observer.views {
		if v.end.Before(v.start) {
			t.Fatal("invalid View observation")
		}
	}
	t.Logf("RESP_FIXTURE tasks=200 viewport=120x40 samples=20 revisions=%d views=%d inputs-during-bursts=%d boundary=pipe-to-default-renderer-output", len(observer.watches), len(observer.views), overlap)
}
