package logging

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const diagnosticLogWindow = 1024 * 1024

// Leave headroom for the CLI envelope and loaded-extension version.
const diagnosticLogOutput = 30 * 1024

var ErrDiagnosticLogSnapshotChanged = errors.New("log snapshot changed; restart pagination")

// LogQuery selects metadata from the configured native daemon log, never a
// caller-provided path. Times are native daemon wall times, without a timezone.
// Identity selectors are opaque IDs returned by a previous query, not text searches.
type LogQuery struct {
	Since     string `json:"since,omitempty"`
	Until     string `json:"until,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Entity    string `json:"entity,omitempty"`
	Operation string `json:"operation,omitempty"`
	Limit     int    `json:"limit"`
	Cursor    string `json:"cursor,omitempty"`
}

type LogIdentity struct {
	Field string `json:"field"`
	ID    string `json:"id"`
}

type LogEntry struct {
	ID               string        `json:"id"`
	Time             string        `json:"time,omitempty"`
	Severity         string        `json:"severity,omitempty"`
	Event            string        `json:"event"`
	Phase            string        `json:"phase,omitempty"`
	DurationMS       *float64      `json:"duration_ms,omitempty"`
	Identities       []LogIdentity `json:"identities"`
	Outcome          string        `json:"outcome"`
	DetailsAvailable bool          `json:"details_available"`
}

type LogPage struct {
	Entries            []LogEntry `json:"entries"`
	Snapshot           string     `json:"snapshot"`
	NextCursor         string     `json:"next_cursor,omitempty"`
	PaginationComplete bool       `json:"pagination_complete"`
	WindowTruncated    bool       `json:"window_truncated"`
	PartialTail        bool       `json:"partial_tail_withheld"`
	ScannedBytes       int        `json:"scanned_bytes"`
	ScannedLines       int        `json:"scanned_lines"`
	UnparsedLines      int        `json:"unparsed_lines"`
	QueryUnknownLines  int        `json:"query_unknown_lines"`
	Total              int        `json:"matching_in_window"`
	Limitations        []string   `json:"limitations"`
}

func (q LogQuery) Validate() error {
	if q.Limit < 1 || q.Limit > 100 || len(q.Cursor) > 512 {
		return errors.New("invalid diagnostic log page selector")
	}
	for _, value := range []string{q.Since, q.Until} {
		if value != "" {
			if _, err := time.Parse(time.DateTime, value); err != nil || len(value) != 19 {
				return errors.New("log times require YYYY-MM-DD HH:MM:SS in the daemon's native wall clock")
			}
		}
	}
	if q.Since != "" && q.Until != "" && q.Since > q.Until {
		return errors.New("log since must not exceed until")
	}
	if q.Severity != "" && q.Severity != "debug" && q.Severity != "info" && q.Severity != "warn" && q.Severity != "error" {
		return errors.New("unsupported diagnostic log severity")
	}
	for _, value := range []string{q.Provider, q.Entity, q.Operation} {
		if value != "" {
			if len(value) != 64 {
				return errors.New("log identity filters require an opaque ID from log metadata")
			}
			if decoded, err := hex.DecodeString(value); err != nil || hex.EncodeToString(decoded) != value {
				return errors.New("invalid diagnostic log identity")
			}
		}
	}
	return nil
}

// DiagnosticLogs reads at most the latest 1 MiB of one regular, non-symlink
// configured file. It neither follows rotations nor reads raw private payloads
// into the response. A completed phase is explicitly not business success.
func DiagnosticLogs(query LogQuery) (LogPage, error) {
	if err := query.Validate(); err != nil {
		return LogPage{}, err
	}
	path, err := Path()
	if err != nil {
		return LogPage{}, errors.New("Radar log path unavailable")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return LogPage{}, errors.New("Radar log unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return LogPage{}, errors.New("Radar log is not a regular file")
	}
	offset := max(int64(0), before.Size()-diagnosticLogWindow)
	data := make([]byte, before.Size()-offset)
	n, err := file.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return LogPage{}, errors.New("Radar log read unavailable")
	}
	data = data[:n]
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(n) != before.Size()-offset {
		return LogPage{}, ErrDiagnosticLogSnapshotChanged
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		return LogPage{}, errors.New("Radar log file identity unavailable")
	}
	fileID := logHash("file", strconv.FormatUint(uint64(stat.Dev), 10)+":"+strconv.FormatUint(uint64(stat.Ino), 10))
	return diagnosticLogPage(query, data, offset, before.Size(), before.ModTime().UnixNano(), fileID)
}

func diagnosticLogPage(query LogQuery, data []byte, offset, size, modified int64, fileID string) (LogPage, error) {
	cursor := query.Cursor
	query.Cursor = ""
	selector, _ := json.Marshal(struct {
		Query                  LogQuery
		Offset, Size, Modified int64
		FileID                 string
	}{query, offset, size, modified, fileID})
	hash := sha256.New()
	hash.Write(selector)
	hash.Write(data)
	snapshot := hex.EncodeToString(hash.Sum(nil))
	start := 0
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		parts := strings.Split(string(raw), ":")
		if err != nil || len(parts) != 2 || parts[0] != snapshot {
			return LogPage{}, ErrDiagnosticLogSnapshotChanged
		}
		start, err = strconv.Atoi(parts[1])
		if err != nil || start < 0 {
			return LogPage{}, errors.New("invalid diagnostic log cursor")
		}
	}
	page := LogPage{Entries: []LogEntry{}, Snapshot: snapshot, WindowTruncated: offset > 0, ScannedBytes: len(data), Limitations: []string{
		"private_installation_wide_log_not_workspace_health", "latest_1_MiB_of_configured_file_only_no_rotation_history",
		"native_wall_clock_seconds_timezone_and_historical_zone_unknown", "unparsed_or_missing_filter_evidence_is_unknown_not_absence",
		"free_form_messages_errors_values_and_raw_identities_withheld", "native_text_not_authenticated_or_atomic_operation_history",
		"timing_completion_is_not_success", "outcomes_targets_retries_and_live_locks_unknown_unless_separately_observed",
	}}
	text := string(data)
	lineOffset := offset
	if offset > 0 {
		// Conservatively discard the first record, even if the window happened
		// to start exactly on a newline. Never interpret a partial record.
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = text[newline+1:]
			lineOffset += int64(newline + 1)
		} else {
			text = ""
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		page.PartialTail = true
		if newline := strings.LastIndexByte(text, '\n'); newline >= 0 {
			text = text[:newline+1]
		} else {
			text = ""
		}
	}
	// Scan newest-first without allocating one object per historical line.
	// Count every match, but retain at most one requested page in memory.
	items := []LogEntry{}
	remaining := strings.TrimSuffix(text, "\n")
	lineEnd := lineOffset + int64(len(text))
	for text != "" {
		newline := strings.LastIndexByte(remaining, '\n')
		line := remaining[newline+1:]
		entryOffset := lineEnd - int64(len(line)+1)
		lineEnd = entryOffset
		page.ScannedLines++
		entry, parsed := diagnosticLogEntry(line)
		if !parsed {
			page.UnparsedLines++
		}
		matches, unknown := logMatches(query, entry)
		if unknown {
			page.QueryUnknownLines++
		}
		if matches {
			if page.Total >= start && len(items) < query.Limit {
				entry.ID = logHash("entry", fileID+":"+strconv.FormatInt(entryOffset, 10)+":"+line)
				items = append(items, entry)
			}
			page.Total++
		}
		if newline < 0 {
			break
		}
		remaining = remaining[:newline]
	}
	if start > page.Total {
		return LogPage{}, errors.New("invalid diagnostic log cursor offset")
	}
	end := len(items)
	for {
		page.Entries = items[:end]
		page.PaginationComplete = start+end == page.Total
		page.NextCursor = ""
		if !page.PaginationComplete {
			page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(snapshot + ":" + strconv.Itoa(start+end)))
		}
		encoded, _ := json.Marshal(page)
		if len(encoded) <= diagnosticLogOutput {
			return page, nil
		}
		end--
		if end <= 0 {
			return LogPage{}, errors.New("diagnostic log entry exceeds output bound")
		}
	}
}

func logHash(field, value string) string {
	sum := sha256.Sum256([]byte(field + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func logMatches(q LogQuery, entry LogEntry) (bool, bool) {
	unknown := (q.Since != "" || q.Until != "") && entry.Time == "" || q.Severity != "" && entry.Severity == ""
	matches := !unknown && (q.Since == "" || entry.Time >= q.Since) && (q.Until == "" || entry.Time <= q.Until) && (q.Severity == "" || entry.Severity == q.Severity)
	for _, filter := range []struct{ kind, value string }{{"provider", q.Provider}, {"entity", q.Entity}, {"operation", q.Operation}} {
		if filter.value == "" {
			continue
		}
		found := false
		for _, identity := range entry.Identities {
			if logIdentityKind(identity.Field) == filter.kind {
				found = found || identity.ID == filter.value
			}
		}
		// An opaque selector does not reveal its field namespace. A row
		// with a different identity cannot prove that the selected field
		// was recorded; conservatively count unlinked rows as unknown.
		unknown = unknown || !found
		matches = matches && found
	}
	return matches, unknown
}

func logIdentityKind(field string) string {
	switch field {
	case "source", "provider":
		return "provider"
	case "operation", "operation_id", "correlation", "correlation_id":
		return "operation"
	case "task", "task_id", "entity", "entity_id", "path", "workspace", "workspace_id", "agent", "agent_id":
		return "entity"
	}
	return ""
}

var diagnosticLogEvents = []struct{ message, code string }{
	{"operation phase completed", "operation_phase_completed"},
	{"source collection finished", "source_collection_finished"},
	{"source collection skipped", "source_collection_skipped"},
	{"source reconciliation finished", "source_reconciliation_finished"},
	{"collection event persistence failed", "collection_event_persistence_reported_failure"},
	{"sbx sandbox collection failed", "sbx_collection_reported_failure"},
	{"github merged review collection failed", "github_merged_review_collection_reported_failure"},
	{"github pull request collection failed", "github_pull_request_collection_reported_failure"},
	{"github tracked pull request collection failed", "github_tracked_collection_reported_failure"},
	{"github rate limit low; pausing collection until reset", "github_rate_limit_pause_reported"},
	{"github collection paused until rate limit reset", "github_rate_limit_pause_reported"},
	{"workspace gc deleted workspace", "workspace_gc_reported_deletion"},
	{"workspace gc skipped", "workspace_gc_skipped"},
	{"garbage collection failed", "garbage_collection_reported_failure"},
	{"reset failed", "reset_reported_failure"},
	{"request received", "request_received"},
	{"collection superseded by resource mutation", "collection_superseded"},
	{"server listening", "server_listening"},
	{"accept failed", "accept_reported_failure"},
	{"invalid request", "invalid_request"},
	{"client read failed", "client_read_reported_failure"},
}

func diagnosticLogEntry(raw string) (LogEntry, bool) {
	entry := LogEntry{Event: "unparsed_details_withheld", Outcome: "unknown_not_recorded", DetailsAvailable: raw != "", Identities: []LogIdentity{}}
	if len(raw) > 16*1024 {
		return entry, false
	}
	line := ansi.Strip(raw)
	if len(line) < 24 {
		return entry, false
	}
	if _, err := time.Parse(time.DateTime, line[:19]); err != nil || line[19] != ' ' {
		return entry, false
	}
	entry.Time = line[:19]
	rest := line[20:]
	level, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return entry, false
	}
	switch level {
	case "DBG":
		entry.Severity = "debug"
	case "INF":
		entry.Severity = "info"
	case "WRN":
		entry.Severity = "warn"
	case "ERR":
		entry.Severity = "error"
	default:
		return entry, false
	}
	// Development logs add a source location. Do not expose it or guess arbitrary prefixes.
	if prefix, tail, ok := strings.Cut(rest, " "); ok {
		if file, number, ok := strings.Cut(prefix, ":"); ok && strings.HasSuffix(file, ".go") {
			if _, err := strconv.ParseUint(number, 10, 32); err == nil {
				rest = tail
			}
		}
	}
	entry.Event = "unclassified_details_withheld"
	for _, event := range diagnosticLogEvents {
		if rest != event.message && !strings.HasPrefix(rest, event.message+" ") {
			continue
		}
		attrs, err := logAttributes(strings.TrimPrefix(rest, event.message))
		if err != nil {
			return entry, false
		}
		entry.Event = event.code
		// Explicit namespaced hashes preserve the full native identity and
		// never infer cross-field ownership or operation correlation.
		for _, field := range []string{"source", "provider", "task", "task_id", "entity", "entity_id", "path", "workspace", "workspace_id", "agent", "agent_id", "operation", "operation_id", "correlation", "correlation_id"} {
			if value := attrs[field]; value != "" {
				entry.Identities = append(entry.Identities, LogIdentity{Field: field, ID: logHash(field, value)})
			}
		}
		if entry.Event == "operation_phase_completed" {
			switch phase := attrs["phase"]; phase {
			case "cleanup_preview", "cleanup_execution", "cleanup_bundle_preflight", "cleanup_runtime", "cleanup_worktree", "cleanup_anchor", "cleanup_resource", "cleanup_response", "operation_lock_wait", "note_lock_wait", "workspace_planning", "worktrees", "port_reconciliation", "workspace_setup", "workspace_apply", "workspace_create", "worktree_removal", "branch_publication_verification", "managed_worktree_removal", "cleanup_protected_scan", "cleanup_member_scan", "sandbox_readiness", "sbx_ls", "sbx_version", "sbx_rm", "sbx_create", "sbx_exec":
				entry.Phase = phase
			}
		}
		if entry.Event == "operation_phase_completed" || entry.Event == "source_collection_finished" || entry.Event == "source_collection_skipped" || entry.Event == "source_reconciliation_finished" {
			if duration, err := time.ParseDuration(attrs["duration"]); err == nil && duration >= 0 {
				ms := float64(duration) / float64(time.Millisecond)
				entry.DurationMS = &ms
			}
		}
		return entry, true
	}
	return entry, true
}

// Parse only native key=value attributes following an exact known static
// message. Never search free-form messages/errors for apparent identity fields.
func logAttributes(text string) (map[string]string, error) {
	attrs := map[string]string{}
	for text = strings.TrimSpace(text); text != ""; text = strings.TrimSpace(text) {
		equal := strings.IndexByte(text, '=')
		if equal < 1 {
			return nil, errors.New("invalid log attributes")
		}
		key := text[:equal]
		for _, r := range key {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
				return nil, errors.New("invalid log key")
			}
		}
		text = text[equal+1:]
		var value string
		if strings.HasPrefix(text, "\"") {
			end, escaped := 1, false
			for ; end < len(text); end++ {
				if !escaped && text[end] == '"' {
					break
				}
				if !escaped && text[end] == '\\' {
					escaped = true
				} else {
					escaped = false
				}
			}
			if end == len(text) {
				return nil, errors.New("invalid quoted log value")
			}
			var err error
			value, err = strconv.Unquote(text[:end+1])
			if err != nil {
				return nil, errors.New("invalid log escape")
			}
			text = text[end+1:]
			if text != "" && text[0] != ' ' {
				return nil, errors.New("invalid log separator")
			}
		} else {
			value, text, _ = strings.Cut(text, " ")
			if value == "" || strings.ContainsAny(value, "\t\r\n\"") {
				return nil, errors.New("invalid log value")
			}
		}
		if _, duplicate := attrs[key]; duplicate {
			return nil, errors.New("ambiguous log attributes")
		}
		attrs[key] = value
	}
	return attrs, nil
}
