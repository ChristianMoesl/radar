package logging

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func diagnosticLogFixture(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "radar.log")
	t.Setenv("RADAR_LOG", path)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nativeLog(t *testing.T, level slog.Level, message string, args ...any) string {
	t.Helper()
	t.Setenv("RADAR_ENV", "production")
	t.Setenv("RADAR_LOG_COLOR", "0")
	t.Setenv("RADAR_LOG_LEVEL", "debug")
	var buffer bytes.Buffer
	logger := NewWithWriter(&buffer)
	record := slog.NewRecord(time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC), level, message, 0)
	record.Add(args...)
	if err := logger.Handler().Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func TestDiagnosticLogsUseNativeEventsAndWithholdAllFreeFormPayloads(t *testing.T) {
	secret := "private token note conversation command environment"
	raw := nativeLog(t, slog.LevelInfo, "operation phase completed", "phase", "cleanup_execution", "duration", 17*time.Millisecond, "provider", secret, "path", "/private/"+secret, "operation_id", secret, "error", secret, "token", secret)
	path := diagnosticLogFixture(t, raw)
	page, err := DiagnosticLogs(LogQuery{Limit: 20})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	entry := page.Entries[0]
	if entry.Event != "operation_phase_completed" || entry.Phase != "cleanup_execution" || entry.DurationMS == nil || *entry.DurationMS != 17 || entry.Outcome != "unknown_not_recorded" || entry.Time != "2026-11-16 12:00:00" {
		t.Fatalf("entry=%+v", entry)
	}
	if len(entry.Identities) != 3 || entry.Identities[0].ID != logHash("provider", secret) {
		t.Fatal(entry.Identities)
	}
	output, _ := json.Marshal(page)
	for _, value := range []string{secret, "/private/", "token=", "error=", "operation phase completed"} {
		if strings.Contains(string(output), value) {
			t.Fatalf("private native field escaped: %q", value)
		}
	}
	after, _ := os.ReadFile(path)
	if string(after) != raw {
		t.Fatal("log query mutated native log")
	}
	for _, field := range []string{"provider", "path", "operation_id"} {
		q := LogQuery{Limit: 20}
		switch logIdentityKind(field) {
		case "provider":
			q.Provider = logHash(field, secret)
		case "entity":
			q.Entity = logHash(field, "/private/"+secret)
		case "operation":
			q.Operation = logHash(field, secret)
		}
		p, err := DiagnosticLogs(q)
		if err == nil && p.Total == 1 && p.Entries[0].ID != entry.ID {
			t.Fatal("log entry identity changed with filters")
		}
		if err != nil || p.Total != 1 {
			t.Fatalf("exact %s query: %+v %v", field, p, err)
		}
	}
}

func TestDiagnosticLogQueriesPreserveUnknownAndDoNotSearchMessagesOrErrors(t *testing.T) {
	text := nativeLog(t, slog.LevelError, "garbage collection failed", "error", `provider=github path=/private operation_id=abc`)
	text += nativeLog(t, slog.LevelWarn, "private provider=github", "provider", "github")
	text += "unrecognized private continuation provider=github\n"
	diagnosticLogFixture(t, text)
	page, err := DiagnosticLogs(LogQuery{Limit: 100})
	if err != nil || page.Total != 3 || page.UnparsedLines != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	for _, entry := range page.Entries {
		if len(entry.Identities) != 0 || entry.Outcome != "unknown_not_recorded" {
			t.Fatal(entry)
		}
	}
	filtered, err := DiagnosticLogs(LogQuery{Limit: 100, Provider: logHash("provider", "github")})
	if err != nil || filtered.Total != 0 || filtered.QueryUnknownLines != 3 {
		t.Fatalf("guessed identity: %+v %v", filtered, err)
	}
	diagnosticLogFixture(t, nativeLog(t, slog.LevelInfo, "source collection finished", "source", "different"))
	unlinked, err := DiagnosticLogs(LogQuery{Limit: 20, Provider: logHash("provider", "selected")})
	if err != nil || unlinked.Total != 0 || unlinked.QueryUnknownLines != 1 {
		t.Fatalf("cross-field identity guessed: %+v %v", unlinked, err)
	}
	diagnosticLogFixture(t, text)
	filtered, err = DiagnosticLogs(LogQuery{Limit: 100, Severity: "error", Since: "2026-11-16 12:00:00", Until: "2026-11-16 12:00:00"})
	if err != nil || filtered.Total != 1 || filtered.QueryUnknownLines != 1 || filtered.Entries[0].Event != "garbage_collection_reported_failure" {
		t.Fatalf("time/severity: %+v %v", filtered, err)
	}
}

func TestDiagnosticLogsPaginateCompleteWindowAndRejectDrift(t *testing.T) {
	text := ""
	for range 127 {
		text += nativeLog(t, slog.LevelInfo, "request received", "method", "private command")
	}
	path := diagnosticLogFixture(t, text)
	q := LogQuery{Limit: 11}
	seen := map[string]bool{}
	for {
		page, err := DiagnosticLogs(q)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 127 || page.WindowTruncated {
			t.Fatalf("incomplete window: %+v", page)
		}
		for _, entry := range page.Entries {
			if seen[entry.ID] {
				t.Fatal("duplicate identity")
			}
			seen[entry.ID] = true
		}
		if page.PaginationComplete {
			if page.NextCursor != "" {
				t.Fatal("terminal cursor")
			}
			break
		}
		if q.Cursor == "" {
			for _, changed := range []LogQuery{{Limit: 12, Cursor: page.NextCursor}, {Limit: 11, Cursor: page.NextCursor, Severity: "info"}} {
				if _, err := DiagnosticLogs(changed); err == nil {
					t.Fatal("cursor accepted changed selector")
				}
			}
		}
		q.Cursor = page.NextCursor
	}
	if len(seen) != 127 {
		t.Fatal(len(seen))
	}
	if err := os.WriteFile(path, []byte(text+nativeLog(t, slog.LevelInfo, "server listening", "socket", "private")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiagnosticLogs(q); err == nil {
		t.Fatal("accepted append drift")
	}
	if _, err := DiagnosticLogs(LogQuery{Limit: 11, Cursor: base64.RawURLEncoding.EncodeToString([]byte("private:100000"))}); err == nil {
		t.Fatal("accepted forged cursor")
	}
}

func TestDiagnosticLogBoundsAndIncompleteRecordsAreExplicit(t *testing.T) {
	line := nativeLog(t, slog.LevelInfo, "server listening", "socket", strings.Repeat("x", 300))
	text := strings.Repeat(line, 5000) + "2026-11-16 12:00:00 INF operation phase completed phase=cleanup_execution duration=1s"
	diagnosticLogFixture(t, text)
	page, err := DiagnosticLogs(LogQuery{Limit: 100})
	if err != nil || !page.WindowTruncated || !page.PartialTail || page.ScannedBytes != diagnosticLogWindow || len(page.Entries) != 100 || page.PaginationComplete {
		t.Fatalf("bound: %+v %v", page, err)
	}
	oversized := strings.Repeat("x", 20000) + "\n"
	diagnosticLogFixture(t, oversized)
	page, err = DiagnosticLogs(LogQuery{Limit: 20})
	if err != nil || page.UnparsedLines != 1 || page.Entries[0].Event != "unparsed_details_withheld" {
		t.Fatalf("oversized: %+v %v", page, err)
	}
	diagnosticLogFixture(t, strings.Repeat("x", diagnosticLogWindow+1))
	page, err = DiagnosticLogs(LogQuery{Limit: 20})
	if err != nil || page.Total != 0 || !page.WindowTruncated {
		t.Fatalf("giant partial line: %+v %v", page, err)
	}
}

func TestDiagnosticLogIdentityUsesFullFieldValueAndOutputIsBounded(t *testing.T) {
	prefix := strings.Repeat("a", 600)
	text := nativeLog(t, slog.LevelInfo, "request received", "provider", prefix+"one") + nativeLog(t, slog.LevelInfo, "request received", "provider", prefix+"two")
	diagnosticLogFixture(t, text)
	page, err := DiagnosticLogs(LogQuery{Limit: 100})
	if err != nil || page.Entries[0].Identities[0].ID == page.Entries[1].Identities[0].ID {
		t.Fatal("clipped native identity", err)
	}
	args := []any{}
	for _, field := range []string{"source", "provider", "task", "task_id", "entity", "entity_id", "path", "workspace", "workspace_id", "agent", "agent_id", "operation", "operation_id", "correlation", "correlation_id"} {
		args = append(args, field, "native-"+field)
	}
	diagnosticLogFixture(t, strings.Repeat(nativeLog(t, slog.LevelInfo, "request received", args...), 120))
	page, err = DiagnosticLogs(LogQuery{Limit: 100})
	data, _ := json.Marshal(page)
	if err != nil || len(data) > diagnosticLogOutput || len(page.Entries) >= 100 || len(page.Entries) == 0 || page.NextCursor == "" {
		t.Fatalf("output bound %d entries=%d err=%v", len(data), len(page.Entries), err)
	}
}

func TestDiagnosticLogsRejectUnsafeFilesAndInvalidSelectors(t *testing.T) {
	path := diagnosticLogFixture(t, "private\n")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{link, fifo, t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		t.Setenv("RADAR_LOG", candidate)
		if _, err := DiagnosticLogs(LogQuery{Limit: 20}); err == nil {
			t.Fatal("accepted unsafe file")
		}
	}
	for _, query := range []LogQuery{{}, {Limit: 101}, {Limit: 20, Severity: "fatal"}, {Limit: 20, Since: "2026-02-30 12:00:00"}, {Limit: 20, Since: "2026-11-16T12:00:00Z"}, {Limit: 20, Since: "2026-11-17 00:00:00", Until: "2026-11-16 00:00:00"}, {Limit: 20, Entity: "/private"}, {Limit: 20, Operation: strings.Repeat("g", 64)}, {Limit: 20, Cursor: strings.Repeat("x", 513)}} {
		if err := query.Validate(); err == nil {
			t.Fatalf("accepted %+v", query)
		}
	}
}

func TestDiagnosticLogNativeColorSourceDurationAndAmbiguousAttributes(t *testing.T) {
	for _, test := range []struct {
		tail   string
		parsed bool
		phase  string
	}{
		{"operation phase completed phase=operation_lock_wait duration=1.25ms", true, "operation_lock_wait"},
		{"operation phase completed phase=cleanup_worktree duration=1ms", true, "cleanup_worktree"},
		{"operation phase completed phase=cleanup_anchor duration=1ms", true, "cleanup_anchor"},
		{"operation phase completed phase=cleanup_resource duration=1ms", true, "cleanup_resource"},
		{"operation phase completed phase=private duration=-1s", true, ""},
		{"operation phase completed phase=cleanup_execution provider=one provider=two", false, ""},
		{"operation phase completed phase=cleanup_execution error=\"private provider=x\"", true, "cleanup_execution"},
		{"operation phase completed phase=cleanup_execution error=\"broken", false, ""},
		{"operation phase completed phase=cleanup_execution error=\"x\"provider=x", false, ""},
	} {
		entry, parsed := diagnosticLogEntry("\x1b[2m2026-11-16 12:00:00\x1b[0m \x1b[92mINF\x1b[0m server/server.go:342 " + test.tail)
		if parsed != test.parsed || parsed && entry.Phase != test.phase || entry.Outcome != "unknown_not_recorded" {
			t.Fatalf("%s: %+v %v", test.tail, entry, parsed)
		}
	}
	for _, raw := range []string{"{}", "time=2026-11-16T12:00:00Z level=INFO msg=private", "2026-11-16 12:00:00 INF+1 private"} {
		if _, ok := diagnosticLogEntry(raw); ok {
			t.Fatal("guessed unsupported format")
		}
	}
	// Every complete line is counted, even when identical or unparsed.
	data := []byte("private\nprivate\n")
	page, err := diagnosticLogPage(LogQuery{Limit: 1}, data, 0, int64(len(data)), 1, "file")
	if err != nil || page.Total != 2 || page.UnparsedLines != 2 {
		t.Fatal(page, err)
	}
	cursor := base64.RawURLEncoding.EncodeToString([]byte(page.Snapshot + ":" + strconv.Itoa(999)))
	if _, err := diagnosticLogPage(LogQuery{Limit: 1, Cursor: cursor}, data, 0, int64(len(data)), 1, "file"); err == nil {
		t.Fatal("accepted out-of-window offset")
	}
}

func TestDiagnosticLogEntryIdentitySurvivesWindowChangesButNotFileRotation(t *testing.T) {
	raw := nativeLog(t, slog.LevelInfo, "source collection finished", "source", "github")
	one, err := diagnosticLogPage(LogQuery{Limit: 20}, []byte(raw), 0, int64(len(raw)), 1, "file-one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := diagnosticLogPage(LogQuery{Limit: 20}, []byte(raw+raw), 0, int64(len(raw)*2), 2, "file-one")
	if err != nil {
		t.Fatal(err)
	}
	if one.Entries[0].ID != two.Entries[1].ID || two.Entries[0].ID == two.Entries[1].ID {
		t.Fatal("entry lifetime/offset identity changed")
	}
	rotated, err := diagnosticLogPage(LogQuery{Limit: 20}, []byte(raw), 0, int64(len(raw)), 1, "file-two")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Entries[0].ID == one.Entries[0].ID || rotated.Snapshot == one.Snapshot {
		t.Fatal("rotated file reused identity")
	}
	// A shifted scan discards its first partial record but keeps the next complete record's absolute offset.
	shift := int64(3)
	window, err := diagnosticLogPage(LogQuery{Limit: 20}, []byte((raw + raw)[shift:]), shift, int64(len(raw)*2), 2, "file-one")
	if err != nil {
		t.Fatal(err)
	}
	if window.Entries[0].ID != two.Entries[0].ID {
		t.Fatal("bounded window changed complete record identity")
	}
}

func TestDiagnosticLogDenseWindowCountsAllLinesAndRetainsOnePage(t *testing.T) {
	data := []byte(strings.Repeat("\n", diagnosticLogWindow))
	page, err := diagnosticLogPage(LogQuery{Limit: 3}, data, 0, int64(len(data)), 1, "file")
	if err != nil || page.ScannedLines != diagnosticLogWindow || page.Total != diagnosticLogWindow || page.UnparsedLines != diagnosticLogWindow || len(page.Entries) != 3 || page.PaginationComplete {
		t.Fatalf("dense window: %+v %v", page, err)
	}
	next, err := diagnosticLogPage(LogQuery{Limit: 3, Cursor: page.NextCursor}, data, 0, int64(len(data)), 1, "file")
	if err != nil || len(next.Entries) != 3 || next.Entries[0].ID == page.Entries[0].ID {
		t.Fatal("dense pagination", err)
	}
}
