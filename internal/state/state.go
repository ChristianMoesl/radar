package state

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"radar.nvim/internal/protocol"
)

const maxStateFileSize = 50 * 1024 * 1024
const stateVersion = 2

type Store struct {
	mu     sync.RWMutex
	state  persistedState
	items  []protocol.Task
	path   string
	logger *slog.Logger
}

type persistedState struct {
	Version    int                     `json:"version"`
	NextTaskID int                     `json:"next_task_id"`
	Records    []TaskRecord            `json:"task_records"`
	SourceRefs []SourceRefRecord       `json:"source_refs"`
	Sources    []protocol.SourceStatus `json:"sources,omitempty"`
}

type TaskRecord struct {
	ID           string        `json:"id"`
	NumericID    int           `json:"numeric_id"`
	CanonicalKey string        `json:"canonical_key"`
	Kind         string        `json:"kind"`
	State        string        `json:"state"`
	Reason       string        `json:"reason,omitempty"`
	DoneAt       string        `json:"done_at,omitempty"`
	FirstSeen    string        `json:"first_seen"`
	LastSeen     string        `json:"last_seen"`
	UpdatedAt    string        `json:"updated_at"`
	SourceRefIDs []string      `json:"source_ref_ids"`
	Ack          TaskAckState  `json:"ack,omitempty"`
	Snapshot     protocol.Task `json:"snapshot"`
}

type TaskAckState struct {
	GeneralCommentsAckAt string `json:"general_comments_ack_at,omitempty"`
}

type SourceRefRecord struct {
	ID           string             `json:"id"`
	Source       string             `json:"source"`
	Kind         string             `json:"kind"`
	TaskRecordID string             `json:"task_record_id"`
	FirstSeen    string             `json:"first_seen"`
	LastSeen     string             `json:"last_seen"`
	ObservedAt   string             `json:"observed_at"`
	Active       bool               `json:"active"`
	Snapshot     protocol.SourceRef `json:"snapshot"`
}

func Path() (string, error) {
	if explicit := os.Getenv("RADAR_STATE"); explicit != "" {
		return explicit, nil
	}

	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}

	return filepath.Join(base, "radar", "tasks.json"), nil
}

func NewStore(logger *slog.Logger) (*Store, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	store := &Store{
		state:  persistedState{Version: stateVersion, Records: []TaskRecord{}, SourceRefs: []SourceRefRecord{}},
		items:  []protocol.Task{},
		path:   path,
		logger: logger,
	}
	if err := store.Load(); err != nil {
		logger.Warn("could not load state", "path", path, "error", err)
	}
	return store, nil
}

func (s *Store) Load() error {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.logger.Info("state file does not exist yet", "path", s.path)
			return nil
		}
		return err
	}
	if info.Size() > maxStateFileSize {
		return fmt.Errorf("state file is too large: %d bytes", info.Size())
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported state version %d; run radar reset", state.Version)
	}
	if state.Records == nil {
		state.Records = []TaskRecord{}
	}
	if state.SourceRefs == nil {
		state.SourceRefs = []SourceRefRecord{}
	}

	s.mu.Lock()
	s.state = state
	s.items = projectTasks(state)
	s.mu.Unlock()

	s.logger.Info("state loaded", "path", s.path, "records", len(state.Records), "source_refs", len(state.SourceRefs))
	return nil
}

func (s *Store) SetTasks(items []protocol.Task) {
	s.setTasks(items, nil)
}

func (s *Store) SetLocalTasks(items []protocol.Task) {
	s.setTasks(items, map[string]bool{"git": true, "tmux": true})
}

func (s *Store) setTasks(items []protocol.Task, sources map[string]bool) {
	s.mu.Lock()
	s.state = reconcileStateForSources(s.state, items, time.Now().UTC(), sources)
	s.items = projectTasks(s.state)
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		s.logger.Warn("could not save state", "path", s.path, "error", err)
	}
}

func (s *Store) Save() error {
	s.mu.RLock()
	state := s.state
	state.Records = append([]TaskRecord(nil), s.state.Records...)
	state.SourceRefs = append([]SourceRefRecord(nil), s.state.SourceRefs...)
	state.Sources = append([]protocol.SourceStatus(nil), s.state.Sources...)
	s.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}

	s.logger.Debug("state saved", "path", s.path, "records", len(state.Records), "source_refs", len(state.SourceRefs))
	return nil
}

func (s *Store) Reset() error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}

	s.mu.Lock()
	s.state = persistedState{Version: stateVersion, Records: []TaskRecord{}, SourceRefs: []SourceRefRecord{}}
	s.items = []protocol.Task{}
	s.mu.Unlock()

	s.logger.Info("state reset", "path", s.path)
	return nil
}

func (s *Store) Acknowledge(itemID string) bool {
	s.mu.Lock()
	changed := false
	ackAt := time.Now().UTC().Format(time.RFC3339)
	for i := range s.state.Records {
		if fmt.Sprint(s.state.Records[i].NumericID) != itemID {
			continue
		}
		for _, sourceRef := range s.state.Records[i].Snapshot.SourceRefs {
			if sourceRef.Metadata == nil {
				continue
			}
			if latest := sourceRef.Metadata["latest_general_comment_at"]; latest != "" && latest > ackAt {
				ackAt = latest
			}
		}
		s.state.Records[i].Ack.GeneralCommentsAckAt = ackAt
		s.state.Records[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		changed = true
		break
	}
	if changed {
		s.items = projectTasks(s.state)
	}
	s.mu.Unlock()

	if changed {
		if err := s.Save(); err != nil {
			s.logger.Warn("could not save acknowledged state", "path", s.path, "error", err)
		}
	}
	return changed
}

func (s *Store) Tasks() []protocol.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]protocol.Task, len(s.items))
	copy(items, s.items)
	return items
}

func (s *Store) SetSources(sources []protocol.SourceStatus) {
	s.mu.Lock()
	s.state.Sources = make([]protocol.SourceStatus, len(sources))
	copy(s.state.Sources, sources)
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		s.logger.Warn("could not save source status", "path", s.path, "error", err)
	}
}

func (s *Store) Sources() []protocol.SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sources := make([]protocol.SourceStatus, len(s.state.Sources))
	copy(sources, s.state.Sources)
	return sources
}

func (s *Store) Summary() protocol.Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var summary protocol.Summary
	for _, item := range s.items {
		switch item.Attention {
		case "immediate":
			summary.Immediate++
		case "attention":
			summary.Attention++
		case "in_progress":
			summary.InProgress++
		case "done":
			summary.Done++
		case "low_priority":
			summary.LowPriority++
		}
	}
	return summary
}

var ticketPattern = regexp.MustCompile(`(?i)[A-Z][A-Z0-9]+-[0-9]+`)

func reconcileState(previous persistedState, observed []protocol.Task, now time.Time) persistedState {
	return reconcileStateForSources(previous, observed, now, nil)
}

func reconcileStateForSources(previous persistedState, observed []protocol.Task, now time.Time, sourceScope map[string]bool) persistedState {
	state := previous
	state.Version = stateVersion
	nowText := now.Format(time.RFC3339)
	if state.Records == nil {
		state.Records = []TaskRecord{}
	}
	if state.SourceRefs == nil {
		state.SourceRefs = []SourceRefRecord{}
	}

	recordsByID := map[string]*TaskRecord{}
	recordsBySourceRef := map[string]*TaskRecord{}
	recordsByKey := map[string]*TaskRecord{}
	maxID := state.NextTaskID
	for i := range state.Records {
		record := &state.Records[i]
		if record.NumericID > maxID {
			maxID = record.NumericID
		}
		recordsByID[record.ID] = record
		if record.CanonicalKey != "" {
			recordsByKey[record.CanonicalKey] = record
		}
		for _, id := range record.SourceRefIDs {
			recordsBySourceRef[id] = record
		}
	}
	state.NextTaskID = maxID

	for i := range state.SourceRefs {
		if sourceScope == nil || sourceScope[state.SourceRefs[i].Source] {
			state.SourceRefs[i].Active = false
		}
	}
	sourceRefsByID := map[string]*SourceRefRecord{}
	for i := range state.SourceRefs {
		sourceRefsByID[state.SourceRefs[i].ID] = &state.SourceRefs[i]
	}

	for _, task := range mergeObservedTasks(observed) {
		key := canonicalTaskKey(task)
		record := matchingRecord(task, key, recordsBySourceRef, recordsByKey)
		if record == nil {
			state.NextTaskID++
			record = &TaskRecord{
				ID:           fmt.Sprintf("task:%d", state.NextTaskID),
				NumericID:    state.NextTaskID,
				CanonicalKey: key,
				Kind:         recordKind(task, key),
				State:        "active",
				FirstSeen:    nowText,
			}
			state.Records = append(state.Records, *record)
			record = &state.Records[len(state.Records)-1]
			recordsByID[record.ID] = record
		} else if key != "" && record.CanonicalKey == "" {
			record.CanonicalKey = key
		}
		if record.CanonicalKey != "" {
			recordsByKey[record.CanonicalKey] = record
		}

		record.LastSeen = nowText
		record.UpdatedAt = nowText
		if sourceScope == nil {
			record.Snapshot = task
		} else {
			record.Snapshot = mergeTasks(record.Snapshot, task)
		}
		record.SourceRefIDs = mergeStringSet(record.SourceRefIDs, sourceRefIDs(task.SourceRefs))
		if task.Attention == "done" {
			record.State = "done"
			record.DoneAt = firstNonEmpty(task.DoneAt, nowText)
			record.Reason = task.Reason
		} else {
			record.State = "active"
			record.DoneAt = ""
			record.Reason = ""
		}

		for _, sourceRef := range task.SourceRefs {
			if sourceRef.ID == "" {
				continue
			}
			refRecord := sourceRefsByID[sourceRef.ID]
			if refRecord == nil {
				state.SourceRefs = append(state.SourceRefs, SourceRefRecord{ID: sourceRef.ID, FirstSeen: nowText})
				refRecord = &state.SourceRefs[len(state.SourceRefs)-1]
				sourceRefsByID[sourceRef.ID] = refRecord
			}
			refRecord.Source = sourceRef.Source
			refRecord.Kind = sourceRef.Kind
			refRecord.TaskRecordID = record.ID
			refRecord.LastSeen = nowText
			refRecord.ObservedAt = nowText
			refRecord.Active = true
			refRecord.Snapshot = sourceRef
			recordsBySourceRef[sourceRef.ID] = record
		}
	}

	state = relinkState(state)

	for i := range state.Records {
		record := &state.Records[i]
		if record.State != "active" || hasActiveSourceRef(*record, state.SourceRefs) {
			continue
		}
		if hasWorktreeSource(*record, state.SourceRefs) && !hasRemoteSource(*record, state.SourceRefs) {
			record.State = "done"
			record.DoneAt = nowText
			record.Reason = "workspace closed"
			record.UpdatedAt = nowText
		}
	}

	return state
}

func relinkState(state persistedState) persistedState {
	groups := sourceRefLinkGroups(state.SourceRefs)
	for _, group := range groups {
		recordIDs := uniqueTaskRecordIDs(group)
		if len(recordIDs) < 2 {
			continue
		}
		winnerID := winningRecordID(state.Records, recordIDs)
		if winnerID == "" {
			continue
		}
		for i := range state.SourceRefs {
			if containsString(recordIDs, state.SourceRefs[i].TaskRecordID) {
				state.SourceRefs[i].TaskRecordID = winnerID
			}
		}
		state.Records = mergeTaskRecords(state.Records, recordIDs, winnerID, state.SourceRefs)
	}
	return state
}

func sourceRefLinkGroups(refs []SourceRefRecord) [][]SourceRefRecord {
	groups := make([][]SourceRefRecord, 0)
	used := make([]bool, len(refs))
	for i := range refs {
		if used[i] || !refs[i].Active || refs[i].TaskRecordID == "" {
			continue
		}
		group := make([]SourceRefRecord, 0)
		queue := []int{i}
		used[i] = true
		for len(queue) > 0 {
			idx := queue[0]
			queue = queue[1:]
			group = append(group, refs[idx])
			for j := range refs {
				if used[j] || !refs[j].Active || refs[j].TaskRecordID == "" {
					continue
				}
				if sourceRefRecordsRelated(refs[idx], refs[j]) {
					used[j] = true
					queue = append(queue, j)
				}
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func sourceRefRecordsRelated(left, right SourceRefRecord) bool {
	return matchesAnyString(linkKeysForSourceRef(left.Snapshot), linkKeysForSourceRef(right.Snapshot))
}

func linkKeysForSourceRef(ref protocol.SourceRef) []string {
	keys := make([]string, 0)
	seen := map[string]bool{}
	add := func(key string) {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}
	for _, key := range extractTicketKeys(ref.ID, ref.Title, ref.Branch, ref.Path, ref.Repo, ref.URL) {
		add("ticket:" + key)
	}
	if ref.Source == "jira" && ref.Kind == "issue" {
		if key := jiraIssueKey(ref); key != "" {
			add("ticket:" + key)
		}
	}
	if ref.Source == "git" && ref.Kind == "worktree" && ref.Path != "" {
		add("workspace:" + cleanPath(ref.Path))
	}
	if ref.Source == "github" && ref.Kind == "pull_request" && ref.ID != "" {
		add(ref.ID)
	}
	if branch := normalizedLinkBranch(ref.Branch); branch != "" {
		if repo := normalizedLinkRepo(ref.Repo); repo != "" && branchLinkableSource(ref) {
			add("branch:" + repo + ":" + branch)
		}
	}
	return keys
}

func branchLinkableSource(ref protocol.SourceRef) bool {
	return (ref.Source == "github" && ref.Kind == "pull_request") || (ref.Source == "git" && ref.Kind == "worktree")
}

func normalizedLinkBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "origin/")
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return strings.ReplaceAll(branch, "/", "-")
}

func normalizedLinkRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "http://github.com/")
	repo = strings.TrimPrefix(repo, "git@github.com:")
	if strings.Contains(repo, "://") || strings.Contains(repo, "@") {
		return ""
	}
	return repo
}

func jiraIssueKey(ref protocol.SourceRef) string {
	if ref.Metadata != nil {
		if key := strings.TrimSpace(ref.Metadata["key"]); key != "" {
			return strings.ToUpper(key)
		}
	}
	if key, ok := strings.CutPrefix(ref.ID, "jira:issue:"); ok {
		return strings.ToUpper(key)
	}
	return ""
}

func uniqueTaskRecordIDs(group []SourceRefRecord) []string {
	ids := make([]string, 0)
	seen := map[string]bool{}
	for _, ref := range group {
		if ref.TaskRecordID == "" || seen[ref.TaskRecordID] {
			continue
		}
		seen[ref.TaskRecordID] = true
		ids = append(ids, ref.TaskRecordID)
	}
	return ids
}

func winningRecordID(records []TaskRecord, ids []string) string {
	var winner *TaskRecord
	for i := range records {
		if !containsString(ids, records[i].ID) {
			continue
		}
		if winner == nil || recordMergeRank(records[i]) < recordMergeRank(*winner) || (recordMergeRank(records[i]) == recordMergeRank(*winner) && records[i].NumericID < winner.NumericID) {
			winner = &records[i]
		}
	}
	if winner == nil {
		return ""
	}
	return winner.ID
}

func recordMergeRank(record TaskRecord) int {
	switch {
	case strings.HasPrefix(record.CanonicalKey, "ticket:"):
		return 0
	case strings.HasPrefix(record.CanonicalKey, "github:pr:"):
		return 1
	case strings.HasPrefix(record.CanonicalKey, "workspace:"):
		return 2
	default:
		return 3
	}
}

func mergeTaskRecords(records []TaskRecord, ids []string, winnerID string, refs []SourceRefRecord) []TaskRecord {
	merged := make([]TaskRecord, 0, len(records))
	var winner TaskRecord
	var loserSnapshots []protocol.Task
	for _, record := range records {
		if record.ID == winnerID {
			winner = record
			continue
		}
		if containsString(ids, record.ID) {
			loserSnapshots = append(loserSnapshots, record.Snapshot)
			if winner.Ack.GeneralCommentsAckAt == "" {
				winner.Ack = record.Ack
			}
			continue
		}
		merged = append(merged, record)
	}
	for _, snapshot := range loserSnapshots {
		winner.Snapshot = mergeTasks(winner.Snapshot, snapshot)
	}
	winner.SourceRefIDs = sourceRefIDsForRecord(winnerID, refs)
	merged = append(merged, winner)
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].NumericID < merged[j].NumericID })
	return merged
}

func sourceRefIDsForRecord(recordID string, refs []SourceRefRecord) []string {
	ids := make([]string, 0)
	for _, ref := range refs {
		if ref.TaskRecordID == recordID && ref.ID != "" {
			ids = append(ids, ref.ID)
		}
	}
	return mergeStringSet(nil, ids)
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func matchingRecord(task protocol.Task, key string, bySourceRef map[string]*TaskRecord, byKey map[string]*TaskRecord) *TaskRecord {
	if key != "" {
		if record := byKey[key]; record != nil {
			return record
		}
	}
	for _, sourceRef := range task.SourceRefs {
		if record := bySourceRef[sourceRef.ID]; record != nil {
			return record
		}
	}
	return nil
}

func mergeObservedTasks(tasks []protocol.Task) []protocol.Task {
	merged := make([]protocol.Task, 0, len(tasks))
	byKey := map[string]int{}
	for _, task := range tasks {
		key := canonicalTaskKey(task)
		if key != "" {
			if idx, ok := byKey[key]; ok {
				merged[idx] = mergeTasks(merged[idx], task)
				continue
			}
			byKey[key] = len(merged)
		}
		merged = append(merged, task)
	}
	return merged
}

func mergeTasks(left, right protocol.Task) protocol.Task {
	if attentionRank(right.Attention) > attentionRank(left.Attention) || left.Title == "" {
		left.Kind = right.Kind
		left.Title = right.Title
		left.Repo = right.Repo
		left.URL = right.URL
		left.Attention = right.Attention
		left.Reason = right.Reason
		left.DoneAt = right.DoneAt
		left.Metadata = right.Metadata
	}
	left.SourceRefs = mergeSourceRefs(left.SourceRefs, right.SourceRefs)
	return left
}

func projectTasks(state persistedState) []protocol.Task {
	activeSourceRefsByRecord := map[string][]protocol.SourceRef{}
	allSourceRefsByRecord := map[string][]protocol.SourceRef{}
	for _, ref := range state.SourceRefs {
		if ref.TaskRecordID == "" || ref.ID == "" || ref.Snapshot.ID == "" {
			continue
		}
		allSourceRefsByRecord[ref.TaskRecordID] = append(allSourceRefsByRecord[ref.TaskRecordID], ref.Snapshot)
		if ref.Active {
			activeSourceRefsByRecord[ref.TaskRecordID] = append(activeSourceRefsByRecord[ref.TaskRecordID], ref.Snapshot)
		}
	}

	tasks := make([]protocol.Task, 0, len(state.Records))
	for _, record := range state.Records {
		if record.State == "done" && olderThan(record.DoneAt, 30*24*time.Hour) {
			continue
		}
		task := cloneTask(record.Snapshot)
		task.ID = record.NumericID
		refs := activeSourceRefsByRecord[record.ID]
		if record.State == "done" {
			refs = allSourceRefsByRecord[record.ID]
		}
		if len(refs) == 0 && record.State != "done" {
			continue
		}
		if len(refs) > 0 {
			task.SourceRefs = cloneSourceRefs(sortSourceRefs(mergeSourceRefs(nil, refs)))
		}
		if record.State == "done" {
			task.Attention = "done"
			task.DoneAt = record.DoneAt
			if record.Reason != "" {
				task.Reason = record.Reason
			}
		}
		if task.Metadata == nil {
			task.Metadata = map[string]string{}
		}
		if record.Ack.GeneralCommentsAckAt != "" {
			task.Metadata["general_comments_ack_at"] = record.Ack.GeneralCommentsAckAt
		}
		if !applyAck(&task, record.Ack) {
			continue
		}
		tasks = append(tasks, task)
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks
}

func cloneTask(task protocol.Task) protocol.Task {
	task.SourceRefs = cloneSourceRefs(task.SourceRefs)
	if task.Metadata != nil {
		task.Metadata = cloneMetadata(task.Metadata)
	}
	return task
}

func cloneSourceRefs(sourceRefs []protocol.SourceRef) []protocol.SourceRef {
	cloned := make([]protocol.SourceRef, len(sourceRefs))
	for i, sourceRef := range sourceRefs {
		cloned[i] = sourceRef
		if sourceRef.Metadata != nil {
			cloned[i].Metadata = cloneMetadata(sourceRef.Metadata)
		}
	}
	return cloned
}

func cloneMetadata(metadata map[string]string) map[string]string {
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func applyAck(task *protocol.Task, ack TaskAckState) bool {
	if ack.GeneralCommentsAckAt == "" || task.Attention == "done" {
		return true
	}
	hasUnresolved := false
	hasNewComments := false
	for i := range task.SourceRefs {
		metadata := task.SourceRefs[i].Metadata
		if metadata == nil {
			continue
		}
		if metadata["unresolved_review_threads"] != "" {
			hasUnresolved = true
		}
		if latest := metadata["latest_general_comment_at"]; latest != "" && latest <= ack.GeneralCommentsAckAt {
			delete(metadata, "new_general_comments")
		}
		if metadata["new_general_comments"] != "" {
			hasNewComments = true
		}
	}
	if hasUnresolved || hasNewComments {
		return true
	}
	if task.Kind == "github_pr_activity" {
		return false
	}
	if task.Kind == "github_own_pr" {
		task.Attention = "in_progress"
		task.Reason = baseReason(*task)
		for i := range task.SourceRefs {
			task.SourceRefs[i].Status = task.Reason
		}
	}
	return true
}

func canonicalTaskKey(task protocol.Task) string {
	if key := firstTicketKey(task); key != "" {
		return "ticket:" + key
	}
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.Source == "git" && sourceRef.Kind == "worktree" && sourceRef.Path != "" {
			return "workspace:" + cleanPath(sourceRef.Path)
		}
	}
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.Source == "github" && sourceRef.Kind == "pull_request" && sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.Source == "jira" && sourceRef.Kind == "issue" && sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	if task.URL != "" {
		return "url:" + task.URL
	}
	return ""
}

func recordKind(task protocol.Task, key string) string {
	if strings.HasPrefix(key, "ticket:") {
		return "ticket"
	}
	if strings.HasPrefix(key, "workspace:") {
		return "workspace"
	}
	if strings.HasPrefix(key, "github:pr:") {
		return "pull_request"
	}
	if strings.HasPrefix(key, "jira:issue:") {
		return "issue"
	}
	return task.Kind
}

func firstTicketKey(task protocol.Task) string {
	values := []string{task.Title, task.Repo, task.URL}
	for _, sourceRef := range task.SourceRefs {
		values = append(values, sourceRef.ID, sourceRef.Title, sourceRef.Branch, sourceRef.Path, sourceRef.Repo, sourceRef.URL)
		for _, value := range sourceRef.Metadata {
			values = append(values, value)
		}
	}
	keys := extractTicketKeys(values...)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func extractTicketKeys(values ...string) []string {
	keys := make([]string, 0)
	seen := map[string]bool{}
	for _, value := range values {
		for _, match := range ticketPattern.FindAllString(value, -1) {
			key := strings.ToUpper(match)
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

func matchesAnyString(left []string, right []string) bool {
	for _, l := range left {
		for _, r := range right {
			if l == r {
				return true
			}
		}
	}
	return false
}

func sourceRefIDs(sourceRefs []protocol.SourceRef) []string {
	ids := make([]string, 0, len(sourceRefs))
	for _, sourceRef := range sourceRefs {
		if sourceRef.ID != "" {
			ids = append(ids, sourceRef.ID)
		}
	}
	return ids
}

func mergeStringSet(left, right []string) []string {
	seen := map[string]bool{}
	merged := make([]string, 0, len(left)+len(right))
	for _, value := range append(left, right...) {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		merged = append(merged, value)
	}
	return merged
}

func mergeSourceRefs(left []protocol.SourceRef, right []protocol.SourceRef) []protocol.SourceRef {
	seen := map[string]bool{}
	merged := make([]protocol.SourceRef, 0, len(left)+len(right))
	for _, sourceRef := range append(left, right...) {
		if sourceRef.ID != "" && seen[sourceRef.ID] {
			continue
		}
		merged = append(merged, sourceRef)
		if sourceRef.ID != "" {
			seen[sourceRef.ID] = true
		}
	}
	return merged
}

func sortSourceRefs(refs []protocol.SourceRef) []protocol.SourceRef {
	sort.SliceStable(refs, func(i, j int) bool {
		return sourceOrder(refs[i].Source) < sourceOrder(refs[j].Source)
	})
	return refs
}

func sourceOrder(source string) int {
	switch source {
	case "jira":
		return 0
	case "github":
		return 1
	case "git":
		return 2
	case "tmux":
		return 3
	default:
		return 9
	}
}

func attentionRank(attention string) int {
	switch attention {
	case "immediate":
		return 5
	case "attention":
		return 4
	case "in_progress":
		return 3
	case "done":
		return 2
	case "low_priority":
		return 1
	default:
		return 0
	}
}

func hasActiveSourceRef(record TaskRecord, sourceRefs []SourceRefRecord) bool {
	ids := map[string]bool{}
	for _, id := range record.SourceRefIDs {
		ids[id] = true
	}
	for _, sourceRef := range sourceRefs {
		if ids[sourceRef.ID] && sourceRef.Active {
			return true
		}
	}
	return false
}

func hasWorktreeSource(record TaskRecord, sourceRefs []SourceRefRecord) bool {
	return hasRecordSource(record, sourceRefs, "git", "worktree")
}

func hasRemoteSource(record TaskRecord, sourceRefs []SourceRefRecord) bool {
	ids := map[string]bool{}
	for _, id := range record.SourceRefIDs {
		ids[id] = true
	}
	for _, sourceRef := range sourceRefs {
		if !ids[sourceRef.ID] {
			continue
		}
		if sourceRef.Source == "github" || sourceRef.Source == "jira" {
			return true
		}
	}
	return false
}

func hasRecordSource(record TaskRecord, sourceRefs []SourceRefRecord, source, kind string) bool {
	ids := map[string]bool{}
	for _, id := range record.SourceRefIDs {
		ids[id] = true
	}
	for _, sourceRef := range sourceRefs {
		if ids[sourceRef.ID] && sourceRef.Source == source && sourceRef.Kind == kind {
			return true
		}
	}
	return false
}

func olderThan(value string, age time.Duration) bool {
	if value == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	return time.Since(parsed) > age
}

func baseReason(item protocol.Task) string {
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Metadata != nil && sourceRef.Metadata["base_reason"] != "" {
			return sourceRef.Metadata["base_reason"]
		}
	}
	return "open PR"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}
