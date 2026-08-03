package state

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"radar/internal/linking"
	"radar/internal/protocol"
)

const maxStateFileSize = 50 * 1024 * 1024

// stateVersion changes only when the persisted format becomes intentionally
// incompatible. Additive, backward-readable state changes must keep the
// current version.
const stateVersion = 5
const doneTaskDisplayRetention = 3 * 24 * time.Hour

type Store struct {
	mu       sync.RWMutex
	saveMu   sync.Mutex
	state    persistedState
	items    []protocol.Task
	path     string
	logger   *slog.Logger
	revision int64
	notify   chan struct{}
}

type persistedState struct {
	Version    int                     `json:"version"`
	NextTaskID int                     `json:"next_task_id"`
	Records    []TaskRecord            `json:"task_records"`
	SourceRefs []SourceRefRecord       `json:"source_refs"`
	Sources    []protocol.SourceStatus `json:"sources,omitempty"`
}

type TaskRecord struct {
	ID               string        `json:"id"`
	NumericID        int           `json:"numeric_id"`
	CanonicalKey     string        `json:"canonical_key"`
	Kind             string        `json:"kind"`
	State            string        `json:"state"`
	Reason           string        `json:"reason,omitempty"`
	DoneAt           string        `json:"done_at,omitempty"`
	FirstSeen        string        `json:"first_seen"`
	LastSeen         string        `json:"last_seen"`
	UpdatedAt        string        `json:"updated_at"`
	SourceRefIDs     []string      `json:"source_ref_ids"`
	Intent           *ManualIntent `json:"intent,omitempty"`
	PriorityOverride string        `json:"priority_override,omitempty"`
	Ack              TaskAckState  `json:"ack,omitempty"`
	Snapshot         protocol.Task `json:"snapshot"`
}

type ManualIntent struct {
	Title            string                     `json:"title"`
	CreatedAt        string                     `json:"created_at"`
	UpdatedAt        string                     `json:"updated_at"`
	ManuallyComplete bool                       `json:"manually_complete"`
	Associations     []protocol.TaskAssociation `json:"associations,omitempty"`
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
		notify: make(chan struct{}),
	}
	if err := store.Load(); err != nil {
		return nil, fmt.Errorf("load state %s: %w", path, err)
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
		return fmt.Errorf("incompatible state version %d, expected %d; state left untouched", state.Version, stateVersion)
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

func (s *Store) SetTasksForSources(items []protocol.Task, sourceNames []string) {
	s.setTasks(items, sourceScope(sourceNames))
}

func sourceScope(sourceNames []string) map[string]bool {
	if len(sourceNames) == 0 {
		return nil
	}
	sources := make(map[string]bool, len(sourceNames))
	for _, name := range sourceNames {
		name = strings.TrimSpace(name)
		if name != "" {
			sources[name] = true
		}
	}
	return sources
}

func (s *Store) setTasks(items []protocol.Task, sources map[string]bool) {
	s.mu.Lock()
	s.state = reconcileStateForSources(s.state, items, time.Now().UTC(), sources)
	s.items = projectTasks(s.state)
	s.bumpRevisionLocked()
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		s.logger.Warn("could not save state", "path", s.path, "error", err)
	}
}

func (s *Store) Save() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}

	// Marshal while holding the read lock so nested pointers, slices, and maps
	// cannot change while JSON encoding is in progress. saveMu keeps snapshots
	// and replacements ordered, preventing an older save from winning a race.
	s.mu.RLock()
	data, err := json.MarshalIndent(s.state, "", "  ")
	recordCount := len(s.state.Records)
	sourceRefCount := len(s.state.SourceRefs)
	s.mu.RUnlock()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(s.path)); err != nil {
		return err
	}

	s.logger.Debug("state saved", "path", s.path, "records", recordCount, "source_refs", sourceRefCount)
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Store) Reset() error {
	nowText := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	retained := make([]TaskRecord, 0)
	for _, record := range s.state.Records {
		if record.Intent == nil && record.PriorityOverride == "" && record.Ack.GeneralCommentsAckAt == "" {
			continue
		}

		// Source observations are rebuildable. Keep only Radar-owned state and
		// enough durable identity to let refreshed refs rejoin the record.
		previousDoneAt := record.DoneAt
		record.State = "active"
		record.Reason = ""
		record.DoneAt = ""
		record.Snapshot = protocol.Task{}
		if record.Intent != nil {
			record.Snapshot = protocol.Task{Kind: "manual", Title: record.Intent.Title, Attention: "low_priority", Reason: "manual task"}
			if record.Intent.ManuallyComplete {
				record.State = "done"
				record.Reason = "manually completed"
				record.DoneAt = firstNonEmpty(previousDoneAt, nowText)
			}
		}
		retained = append(retained, record)
	}
	s.state = persistedState{
		Version:    stateVersion,
		NextTaskID: s.state.NextTaskID,
		Records:    retained,
		SourceRefs: []SourceRefRecord{},
		Sources:    []protocol.SourceStatus{},
	}
	s.items = projectTasks(s.state)
	s.bumpRevisionLocked()
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		return err
	}
	s.logger.Info("state reset", "path", s.path, "retained_records", len(retained))
	return nil
}

func (s *Store) CreateManualTask(title string) (protocol.Task, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return protocol.Task{}, fmt.Errorf("task title must not be empty")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	s.state.NextTaskID++
	record := TaskRecord{
		ID:        fmt.Sprintf("task:%d", s.state.NextTaskID),
		NumericID: s.state.NextTaskID,
		Kind:      "manual",
		State:     "active",
		FirstSeen: now,
		LastSeen:  now,
		UpdatedAt: now,
		Intent: &ManualIntent{
			Title:        title,
			CreatedAt:    now,
			UpdatedAt:    now,
			Associations: []protocol.TaskAssociation{},
		},
		Snapshot: protocol.Task{Kind: "manual", Title: title, Attention: "low_priority", Reason: "manual task"},
	}
	s.state.Records = append(s.state.Records, record)
	s.items = projectTasks(s.state)
	s.bumpRevisionLocked()
	task, _ := taskByNumericID(s.items, record.NumericID)
	s.mu.Unlock()
	if err := s.Save(); err != nil {
		return protocol.Task{}, err
	}
	return task, nil
}

func (s *Store) CompleteManualTask(taskID int) (protocol.Task, error) {
	return s.setManualCompletion(taskID, true)
}

func (s *Store) ReopenManualTask(taskID int) (protocol.Task, error) {
	return s.setManualCompletion(taskID, false)
}

func (s *Store) setManualCompletion(taskID int, complete bool) (protocol.Task, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	record := recordByNumericID(s.state.Records, taskID)
	if record == nil {
		s.mu.Unlock()
		return protocol.Task{}, fmt.Errorf("task %d not found", taskID)
	}
	if record.Intent == nil {
		s.mu.Unlock()
		return protocol.Task{}, fmt.Errorf("task %d is not a manual task", taskID)
	}
	if manualLifecycleUsesWorkItem(*record, s.state.SourceRefs) {
		s.mu.Unlock()
		return protocol.Task{}, fmt.Errorf("task %d lifecycle is controlled by an attached work item", taskID)
	}
	if complete {
		record.State = "done"
		record.DoneAt = now
		record.Reason = "manually completed"
	} else {
		record.State = "active"
		record.DoneAt = ""
		record.Reason = ""
	}
	record.Intent.ManuallyComplete = complete
	record.Intent.UpdatedAt = now
	record.UpdatedAt = now
	s.items = projectTasks(s.state)
	s.bumpRevisionLocked()
	task, _ := taskByNumericID(s.items, taskID)
	s.mu.Unlock()
	if err := s.Save(); err != nil {
		return protocol.Task{}, err
	}
	return task, nil
}

func (s *Store) SetTaskPriority(taskID int, priority string) (protocol.Task, error) {
	priority = strings.ToLower(strings.TrimSpace(priority))
	if priority != "urgent" && priority != "normal" {
		return protocol.Task{}, fmt.Errorf("unsupported task priority %q; use urgent or normal", priority)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	record := recordByNumericID(s.state.Records, taskID)
	if record == nil {
		s.mu.Unlock()
		return protocol.Task{}, fmt.Errorf("task %d not found", taskID)
	}
	if record.State == "done" {
		s.mu.Unlock()
		return protocol.Task{}, fmt.Errorf("task %d is done and cannot be made urgent", taskID)
	}
	if priority == "urgent" {
		record.PriorityOverride = "urgent"
	} else {
		record.PriorityOverride = ""
	}
	record.UpdatedAt = now
	if record.Intent != nil {
		record.Intent.UpdatedAt = now
	}
	s.items = projectTasks(s.state)
	s.bumpRevisionLocked()
	task, _ := taskByNumericID(s.items, taskID)
	s.mu.Unlock()
	if err := s.Save(); err != nil {
		return protocol.Task{}, err
	}
	return task, nil
}

func (s *Store) AttachAssociation(taskID int, association protocol.TaskAssociation) (protocol.Task, error) {
	association.Source = strings.TrimSpace(association.Source)
	association.ExternalID = strings.TrimSpace(association.ExternalID)
	association.CanonicalKey = strings.TrimSpace(association.CanonicalKey)
	association.LinkingKeys = mergeStringSet(nil, association.LinkingKeys)
	if association.Source == "" || association.ExternalID == "" || association.CanonicalKey == "" {
		return protocol.Task{}, fmt.Errorf("task association source, external ID, and canonical key are required")
	}
	if association.Lifecycle != protocol.SourceRefLifecycleWorkItem && association.Lifecycle != protocol.SourceRefLifecycleWorkspace && association.Lifecycle != protocol.SourceRefLifecycleResource {
		return protocol.Task{}, fmt.Errorf("task association has unsupported lifecycle %q", association.Lifecycle)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	record := recordByNumericID(s.state.Records, taskID)
	if record == nil {
		s.mu.Unlock()
		return protocol.Task{}, fmt.Errorf("task %d not found", taskID)
	}
	if record.Intent == nil {
		s.mu.Unlock()
		return protocol.Task{}, fmt.Errorf("task %d is not a manual task", taskID)
	}
	record.CanonicalKey = association.CanonicalKey
	record.Kind = recordKind(protocol.Task{}, association.CanonicalKey)
	record.State = "active"
	record.DoneAt = ""
	record.Reason = ""
	record.Intent.ManuallyComplete = false
	record.Intent.Associations = mergeAssociations(record.Intent.Associations, []protocol.TaskAssociation{association})
	record.Intent.UpdatedAt = now
	record.UpdatedAt = now
	s.state = mergeRecordIntoManual(s.state, record.ID, association.CanonicalKey)
	updateRecordLifecycles(s.state.Records, s.state.SourceRefs, now)
	s.items = projectTasks(s.state)
	s.bumpRevisionLocked()
	task, _ := taskByNumericID(s.items, taskID)
	s.mu.Unlock()
	if err := s.Save(); err != nil {
		return protocol.Task{}, err
	}
	return task, nil
}

func recordByNumericID(records []TaskRecord, id int) *TaskRecord {
	for i := range records {
		if records[i].NumericID == id {
			return &records[i]
		}
	}
	return nil
}

func taskByNumericID(tasks []protocol.Task, id int) (protocol.Task, bool) {
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return protocol.Task{}, false
}

func manualLifecycleUsesWorkItem(record TaskRecord, refs []SourceRefRecord) bool {
	if record.Intent != nil {
		for _, association := range record.Intent.Associations {
			if association.Lifecycle == protocol.SourceRefLifecycleWorkItem {
				return true
			}
		}
	}
	return hasLifecycleSource(record, refs, protocol.SourceRefLifecycleWorkItem)
}

func mergeRecordIntoManual(state persistedState, manualID, canonicalKey string) persistedState {
	manual := recordByID(state.Records, manualID)
	if manual == nil {
		return state
	}
	loserIDs := map[string]bool{}
	for i := range state.Records {
		record := &state.Records[i]
		if record.ID != manualID && record.CanonicalKey == canonicalKey {
			loserIDs[record.ID] = true
			manual.Snapshot = mergeTasks(manual.Snapshot, record.Snapshot)
			manual.SourceRefIDs = mergeStringSet(manual.SourceRefIDs, record.SourceRefIDs)
			if manual.PriorityOverride == "" {
				manual.PriorityOverride = record.PriorityOverride
			}
			if manual.Ack.GeneralCommentsAckAt == "" {
				manual.Ack = record.Ack
			}
		}
	}
	if len(loserIDs) == 0 {
		return state
	}
	for i := range state.SourceRefs {
		if loserIDs[state.SourceRefs[i].TaskRecordID] {
			state.SourceRefs[i].TaskRecordID = manualID
		}
	}
	kept := make([]TaskRecord, 0, len(state.Records)-len(loserIDs))
	for _, record := range state.Records {
		if !loserIDs[record.ID] {
			kept = append(kept, record)
		}
	}
	state.Records = kept
	manual = recordByID(state.Records, manualID)
	if manual != nil {
		manual.SourceRefIDs = sourceRefIDsForRecord(manualID, state.SourceRefs)
	}
	return state
}

func recordByID(records []TaskRecord, id string) *TaskRecord {
	for i := range records {
		if records[i].ID == id {
			return &records[i]
		}
	}
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
		s.bumpRevisionLocked()
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
	s.bumpRevisionLocked()
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

func (s *Store) Records() []TaskRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	records := make([]TaskRecord, len(s.state.Records))
	copy(records, s.state.Records)
	return records
}

func (s *Store) SourceRefs() []SourceRefRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	refs := make([]SourceRefRecord, len(s.state.SourceRefs))
	copy(refs, s.state.SourceRefs)
	return refs
}

func (s *Store) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

func (s *Store) WaitForRevision(ctx context.Context, after int64) int64 {
	for {
		s.mu.Lock()
		if s.revision > after {
			revision := s.revision
			s.mu.Unlock()
			return revision
		}
		if s.notify == nil {
			s.notify = make(chan struct{})
		}
		notify := s.notify
		s.mu.Unlock()

		select {
		case <-notify:
			continue
		case <-ctx.Done():
			return s.Revision()
		}
	}
}

func (s *Store) bumpRevisionLocked() {
	s.revision++
	if s.notify == nil {
		s.notify = make(chan struct{})
		return
	}
	close(s.notify)
	s.notify = make(chan struct{})
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
	recordsByNumericID := map[int]*TaskRecord{}
	recordsBySourceRef := map[string]*TaskRecord{}
	recordsByKey := map[string]*TaskRecord{}
	maxID := state.NextTaskID
	for i := range state.Records {
		record := &state.Records[i]
		if record.NumericID > maxID {
			maxID = record.NumericID
		}
		recordsByID[record.ID] = record
		recordsByNumericID[record.NumericID] = record
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
		task = taskWithSourceSignals(task)
		key := canonicalTaskKey(task)
		record := matchingRecord(task, key, recordsByNumericID, recordsBySourceRef, recordsByKey)
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
			recordsByNumericID[record.NumericID] = record
		} else if key != "" && record.CanonicalKey == "" {
			record.CanonicalKey = key
		}
		if record.CanonicalKey != "" {
			recordsByKey[record.CanonicalKey] = record
		}

		record.LastSeen = nowText
		record.UpdatedAt = nowText
		authoritative := taskHasAuthoritativeSource(task)
		if sourceScope == nil && authoritative {
			record.Snapshot = task
		} else {
			record.Snapshot = mergeTasks(record.Snapshot, task)
		}
		record.SourceRefIDs = mergeStringSet(record.SourceRefIDs, sourceRefIDs(task.SourceRefs))
		if authoritative {
			if record.Intent != nil && taskHasWorkItemSource(task) {
				record.Intent.ManuallyComplete = false
				record.Intent.UpdatedAt = nowText
			}
			if task.Attention == "done" {
				record.State = "done"
				record.DoneAt = firstNonEmpty(record.DoneAt, task.DoneAt, nowText)
				record.Reason = task.Reason
			} else {
				record.State = "active"
				record.DoneAt = ""
				record.Reason = ""
			}
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
			if sourceRef.Signal == "" {
				sourceRef.Signal = task.Attention
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

	demotedRecords := recomputeDerivedCanonicalKeys(state.Records, state.SourceRefs)
	resetDemotedRecordLifecycles(state.Records, demotedRecords)
	state = relinkState(state)
	updateRecordLifecycles(state.Records, state.SourceRefs, nowText)

	for i := range state.Records {
		record := &state.Records[i]
		if record.State != "active" || hasActiveSourceRef(*record, state.SourceRefs) {
			continue
		}
		if hasKnownLifecycleSource(*record, state.SourceRefs, protocol.SourceRefLifecycleWorkspace) && !hasLifecycleSource(*record, state.SourceRefs, protocol.SourceRefLifecycleWorkItem) {
			record.State = "done"
			record.DoneAt = nowText
			record.Reason = "workspace closed"
			record.UpdatedAt = nowText
		}
	}

	return state
}

func relinkState(state persistedState) persistedState {
	for _, group := range sourceRefLinkGroups(state.SourceRefs) {
		state = mergeRelatedRecords(state, uniqueTaskRecordIDs(group))
	}

	byCanonicalKey := map[string][]string{}
	for _, record := range state.Records {
		if record.CanonicalKey != "" {
			byCanonicalKey[record.CanonicalKey] = append(byCanonicalKey[record.CanonicalKey], record.ID)
		}
	}
	keys := make([]string, 0, len(byCanonicalKey))
	for key := range byCanonicalKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		state = mergeRelatedRecords(state, byCanonicalKey[key])
	}
	return state
}

func mergeRelatedRecords(state persistedState, recordIDs []string) persistedState {
	if len(recordIDs) < 2 {
		return state
	}
	winnerID := winningRecordID(state.Records, recordIDs)
	if winnerID == "" {
		return state
	}
	for i := range state.SourceRefs {
		if containsString(recordIDs, state.SourceRefs[i].TaskRecordID) {
			state.SourceRefs[i].TaskRecordID = winnerID
		}
	}
	state.Records = mergeTaskRecords(state.Records, recordIDs, winnerID, state.SourceRefs)
	return state
}

func recomputeDerivedCanonicalKeys(records []TaskRecord, refs []SourceRefRecord) map[string]bool {
	demoted := map[string]bool{}
	for i := range records {
		record := &records[i]
		if record.CanonicalKey == "" || manualAssociationContains(*record, record.CanonicalKey) {
			continue
		}
		active := activeSourceRefRecordsForRecord(record.ID, refs)
		if canonicalKeyPresent(record.CanonicalKey, active) || !hasAuthorityDemotion(record.ID, refs) {
			continue
		}
		snapshots := make([]protocol.SourceRef, 0, len(active))
		for _, ref := range active {
			snapshots = append(snapshots, ref.Snapshot)
		}
		record.CanonicalKey = canonicalTaskKey(protocol.Task{SourceRefs: snapshots})
		demoted[record.ID] = true
	}
	return demoted
}

func resetDemotedRecordLifecycles(records []TaskRecord, demoted map[string]bool) {
	for i := range records {
		if !demoted[records[i].ID] {
			continue
		}
		if records[i].Intent != nil && records[i].Intent.ManuallyComplete {
			records[i].State = "done"
			records[i].Reason = "manually completed"
			continue
		}
		records[i].State = "active"
		records[i].DoneAt = ""
		records[i].Reason = ""
	}
}

func hasAuthorityDemotion(recordID string, refs []SourceRefRecord) bool {
	informationalEntities := map[string]bool{}
	for _, ref := range refs {
		if ref.TaskRecordID == recordID && ref.Active && ref.Snapshot.Role == protocol.SourceRefRoleInformational && ref.Snapshot.EntityID != "" {
			informationalEntities[ref.Snapshot.Source+"\x00"+ref.Snapshot.EntityID] = true
		}
	}
	for _, ref := range refs {
		if ref.TaskRecordID == recordID && !ref.Active && authoritativeRef(ref.Snapshot) && informationalEntities[ref.Snapshot.Source+"\x00"+ref.Snapshot.EntityID] {
			return true
		}
	}
	return false
}

func manualAssociationContains(record TaskRecord, key string) bool {
	if record.Intent == nil {
		return false
	}
	for _, association := range record.Intent.Associations {
		if association.CanonicalKey == key || containsString(association.LinkingKeys, key) {
			return true
		}
	}
	return false
}

func canonicalKeyPresent(key string, refs []SourceRefRecord) bool {
	for _, ref := range refs {
		if ref.Snapshot.CanonicalKey == key || containsString(ref.Snapshot.LinkingKeys, key) {
			return true
		}
	}
	return false
}

func sourceRefLinkGroups(refs []SourceRefRecord) [][]SourceRefRecord {
	groups := make([][]SourceRefRecord, 0)
	used := make([]bool, len(refs))
	for i := range refs {
		if used[i] || !refs[i].Active || refs[i].TaskRecordID == "" || !authoritativeRef(refs[i].Snapshot) {
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
				if used[j] || !refs[j].Active || refs[j].TaskRecordID == "" || !authoritativeRef(refs[j].Snapshot) {
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
	if !authoritativeRef(ref) {
		return nil
	}
	keys := make([]string, 0, len(ref.LinkingKeys))
	seen := map[string]bool{}
	for _, key := range ref.LinkingKeys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys
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
	case record.Intent != nil:
		return -1
	case linking.IsMarkKey(record.CanonicalKey):
		return 0
	case strings.HasPrefix(record.CanonicalKey, "workspace:"):
		return 1
	default:
		return 2
	}
}

func mergeTaskRecords(records []TaskRecord, ids []string, winnerID string, refs []SourceRefRecord) []TaskRecord {
	merged := make([]TaskRecord, 0, len(records))
	winnerRecord := recordByID(records, winnerID)
	if winnerRecord == nil {
		return records
	}
	winner := *winnerRecord
	var loserSnapshots []protocol.Task
	for _, record := range records {
		if record.ID == winnerID {
			continue
		}
		if containsString(ids, record.ID) {
			loserSnapshots = append(loserSnapshots, record.Snapshot)
			if winner.Intent == nil && record.Intent != nil {
				winner.Intent = record.Intent
			}
			if winner.PriorityOverride == "" {
				winner.PriorityOverride = record.PriorityOverride
			}
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

func updateRecordLifecycles(records []TaskRecord, sourceRefs []SourceRefRecord, nowText string) {
	for i := range records {
		refs := activeSourceRefRecordsForRecord(records[i].ID, sourceRefs)
		if len(refs) == 0 {
			continue
		}
		allRefs := sourceRefRecordsForRecord(records[i].ID, sourceRefs)
		fallback := records[i].Snapshot.Attention
		if (hasDoneSignal(refs, fallback) && !hasNonDoneSignal(refs, fallback)) || workItemsDoneWithSupportingRefs(allRefs, fallback) {
			records[i].State = "done"
			records[i].DoneAt = firstNonEmpty(records[i].DoneAt, nowText)
			records[i].Reason = firstDoneReason(refs, fallback)
			records[i].UpdatedAt = nowText
			continue
		}
		records[i].State = "active"
		records[i].DoneAt = ""
		records[i].Reason = ""
	}
}

func activeSourceRefRecordsForRecord(recordID string, refs []SourceRefRecord) []SourceRefRecord {
	active := make([]SourceRefRecord, 0)
	for _, ref := range refs {
		if ref.TaskRecordID == recordID && ref.Active && authoritativeRef(ref.Snapshot) {
			active = append(active, ref)
		}
	}
	return active
}

func sourceRefRecordsForRecord(recordID string, refs []SourceRefRecord) []SourceRefRecord {
	matched := make([]SourceRefRecord, 0)
	for _, ref := range refs {
		if ref.TaskRecordID == recordID {
			matched = append(matched, ref)
		}
	}
	return matched
}

func hasDoneSignal(refs []SourceRefRecord, fallback string) bool {
	for _, ref := range refs {
		if sourceSignal(ref.Snapshot, fallback) == "done" {
			return true
		}
	}
	return false
}

func hasNonDoneSignal(refs []SourceRefRecord, fallback string) bool {
	for _, ref := range refs {
		signal := sourceSignal(ref.Snapshot, fallback)
		if signal != "" && signal != "done" {
			return true
		}
	}
	return false
}

func firstDoneReason(refs []SourceRefRecord, fallback string) string {
	for _, ref := range refs {
		if sourceSignal(ref.Snapshot, fallback) == "done" && ref.Snapshot.Status != "" {
			return ref.Snapshot.Status
		}
	}
	return "done"
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

func matchingRecord(task protocol.Task, key string, byNumericID map[int]*TaskRecord, bySourceRef map[string]*TaskRecord, byKey map[string]*TaskRecord) *TaskRecord {
	if task.TargetTaskID != 0 {
		if record := byNumericID[task.TargetTaskID]; record != nil {
			return record
		}
	}
	if key != "" {
		if record := byKey[key]; record != nil {
			return record
		}
	}
	for _, sourceRef := range task.SourceRefs {
		if !authoritativeRef(sourceRef) {
			continue
		}
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

func taskWithSourceSignals(task protocol.Task) protocol.Task {
	for i := range task.SourceRefs {
		if authoritativeRef(task.SourceRefs[i]) && task.SourceRefs[i].Signal == "" {
			task.SourceRefs[i].Signal = task.Attention
		}
	}
	return task
}

func applySourceSignals(task *protocol.Task, record TaskRecord, refs []protocol.SourceRef) {
	fallback := record.Snapshot.Attention
	if signal, reason := firstSignal(refs, "immediate", fallback); signal != "" {
		task.Attention = signal
		if reason != "" {
			task.Reason = reason
		}
		return
	}
	if signal, reason := firstSignal(refs, "attention", fallback); signal != "" {
		task.Attention = signal
		if reason != "" {
			task.Reason = reason
		}
		return
	}
	if signal, reason := firstSignal(refs, "in_progress", fallback); signal != "" {
		task.Attention = signal
		if task.Reason == "" || record.Snapshot.Attention != signal {
			task.Reason = reason
		}
		return
	}
	if signal, reason := firstSignal(refs, "low_priority", fallback); signal != "" {
		task.Attention = signal
		if reason != "" {
			task.Reason = reason
		}
	}
}

func firstSignal(refs []protocol.SourceRef, want string, fallback string) (string, string) {
	for _, ref := range refs {
		if sourceSignal(ref, fallback) == want {
			return want, sourceReason(ref, want)
		}
	}
	return "", ""
}

func sourceSignal(ref protocol.SourceRef, fallback string) string {
	if !authoritativeRef(ref) {
		return ""
	}
	if ref.Signal != "" {
		return ref.Signal
	}
	return fallback
}

func sourceReason(ref protocol.SourceRef, signal string) string {
	if ref.Status != "" {
		return ref.Status
	}
	switch signal {
	case "immediate":
		return "immediate attention"
	case "attention":
		return "needs attention"
	case "in_progress":
		return ref.Source + " " + ref.Kind
	case "done":
		return "done"
	default:
		return ""
	}
}

func workItemsDoneWithSupportingRefs(refs []SourceRefRecord, fallback string) bool {
	snapshots := make([]protocol.SourceRef, 0, len(refs))
	for _, ref := range refs {
		snapshots = append(snapshots, ref.Snapshot)
	}
	return workItemsDone(snapshots, fallback)
}

func workItemsDone(refs []protocol.SourceRef, fallback string) bool {
	hasDoneWorkItem := false
	hasSupportingRef := false
	for _, ref := range refs {
		if !authoritativeRef(ref) {
			continue
		}
		if ref.Lifecycle == protocol.SourceRefLifecycleWorkItem {
			if sourceSignal(ref, fallback) != "done" {
				return false
			}
			hasDoneWorkItem = true
			continue
		}
		if ref.Lifecycle == protocol.SourceRefLifecycleWorkspace || ref.Lifecycle == protocol.SourceRefLifecycleResource {
			hasSupportingRef = true
		}
	}
	return hasDoneWorkItem && hasSupportingRef
}

func authoritativeRef(ref protocol.SourceRef) bool {
	return ref.Role == protocol.SourceRefRoleAuthoritative
}

func hasAuthoritativeProtocolRef(refs []protocol.SourceRef) bool {
	for _, ref := range refs {
		if authoritativeRef(ref) {
			return true
		}
	}
	return false
}

func localCleanupSource(ref protocol.SourceRef) bool {
	return ref.Lifecycle == protocol.SourceRefLifecycleWorkspace || ref.Lifecycle == protocol.SourceRefLifecycleResource
}

func projectTasks(state persistedState) []protocol.Task {
	activeSourceRefsByRecord := map[string][]protocol.SourceRef{}
	doneSourceRefsByRecord := map[string][]protocol.SourceRef{}
	for _, ref := range state.SourceRefs {
		if ref.TaskRecordID == "" || ref.ID == "" || ref.Snapshot.ID == "" {
			continue
		}
		if ref.Active {
			activeSourceRefsByRecord[ref.TaskRecordID] = append(activeSourceRefsByRecord[ref.TaskRecordID], ref.Snapshot)
		}
		if ref.Active || !localCleanupSource(ref.Snapshot) {
			doneSourceRefsByRecord[ref.TaskRecordID] = append(doneSourceRefsByRecord[ref.TaskRecordID], ref.Snapshot)
		}
	}

	tasks := make([]protocol.Task, 0, len(state.Records))
	for _, record := range state.Records {
		if record.State == "done" && olderThan(record.DoneAt, doneTaskDisplayRetention) {
			continue
		}
		task := cloneTask(record.Snapshot)
		task.ID = record.NumericID
		refs := activeSourceRefsByRecord[record.ID]
		if record.State == "done" {
			refs = doneSourceRefsByRecord[record.ID]
		}
		if !hasAuthoritativeProtocolRef(refs) && record.Intent == nil && record.State != "done" {
			continue
		}
		task.TargetTaskID = 0
		task.SourceRefs = cloneSourceRefs(sortSourceRefs(mergeSourceRefs(nil, refs)))
		if record.Intent != nil {
			task.Title = record.Intent.Title
		}
		if title := preferredTitle(refs); title != "" {
			task.Title = title
		}
		if record.State == "done" {
			task.Attention = "done"
			task.DoneAt = record.DoneAt
			if record.Reason != "" {
				task.Reason = record.Reason
			}
		} else if !hasAuthoritativeProtocolRef(refs) {
			task.Attention = "low_priority"
			task.Reason = "manual task"
		} else {
			applySourceSignals(&task, record, refs)
		}
		if record.State != "done" && record.PriorityOverride == "urgent" {
			task.Attention = "immediate"
			task.Reason = "manually urgent"
		}
		if task.Metadata == nil {
			task.Metadata = map[string]string{}
		}
		if record.PriorityOverride == "urgent" {
			task.Metadata["priority_override"] = "urgent"
		}
		if record.Intent != nil {
			task.Associations = cloneAssociations(record.Intent.Associations)
			task.Metadata["manual_task"] = "true"
			task.Metadata["manual_title"] = record.Intent.Title
			task.Metadata["manual_created_at"] = record.Intent.CreatedAt
			task.Metadata["manual_updated_at"] = record.Intent.UpdatedAt
			task.Metadata["manual_complete"] = fmt.Sprint(record.Intent.ManuallyComplete)
			task.Metadata["manual_lifecycle_available"] = fmt.Sprint(!manualLifecycleUsesWorkItem(record, state.SourceRefs))
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

func preferredTitle(refs []protocol.SourceRef) string {
	title := ""
	bestOrder := int(^uint(0) >> 1)
	for _, ref := range refs {
		if !authoritativeRef(ref) || !ref.Presentation.PreferTitle || strings.TrimSpace(ref.Title) == "" {
			continue
		}
		if ref.Presentation.TitleOrder == nil {
			if title == "" {
				title = ref.Title
			}
			continue
		}
		if *ref.Presentation.TitleOrder < bestOrder {
			title = ref.Title
			bestOrder = *ref.Presentation.TitleOrder
		}
	}
	return title
}

func taskHasAuthoritativeSource(task protocol.Task) bool {
	return hasAuthoritativeProtocolRef(task.SourceRefs)
}

func taskHasWorkItemSource(task protocol.Task) bool {
	for _, ref := range task.SourceRefs {
		if authoritativeRef(ref) && ref.Lifecycle == protocol.SourceRefLifecycleWorkItem {
			return true
		}
	}
	return false
}

func cloneTask(task protocol.Task) protocol.Task {
	task.SourceRefs = cloneSourceRefs(task.SourceRefs)
	task.Associations = cloneAssociations(task.Associations)
	if task.Metadata != nil {
		task.Metadata = cloneMetadata(task.Metadata)
	}
	return task
}

func cloneAssociations(associations []protocol.TaskAssociation) []protocol.TaskAssociation {
	cloned := make([]protocol.TaskAssociation, len(associations))
	for i, association := range associations {
		cloned[i] = association
		cloned[i].LinkingKeys = append([]string(nil), association.LinkingKeys...)
	}
	return cloned
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
		if signal, reason := firstSignal(task.SourceRefs, "in_progress", ""); signal != "" {
			task.Attention = signal
			task.Reason = reason
			return true
		}
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
	if key := firstLinkingMarkKey(task); key != "" {
		return key
	}
	for _, sourceRef := range task.SourceRefs {
		if !authoritativeRef(sourceRef) {
			continue
		}
		if key := strings.TrimSpace(sourceRef.CanonicalKey); key != "" {
			return key
		}
	}
	for _, sourceRef := range task.SourceRefs {
		if authoritativeRef(sourceRef) && sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	if task.URL != "" && hasAuthoritativeProtocolRef(task.SourceRefs) {
		return "url:" + task.URL
	}
	return ""
}

func recordKind(task protocol.Task, key string) string {
	if linking.IsMarkKey(key) {
		return "linking_mark"
	}
	if strings.HasPrefix(key, "workspace:") {
		return "workspace"
	}
	return task.Kind
}

func firstLinkingMarkKey(task protocol.Task) string {
	for _, sourceRef := range task.SourceRefs {
		if !authoritativeRef(sourceRef) {
			continue
		}
		for _, key := range sourceRef.LinkingKeys {
			key = strings.TrimSpace(key)
			if linking.IsMarkKey(key) {
				return key
			}
		}
	}
	return ""
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

func mergeAssociations(left, right []protocol.TaskAssociation) []protocol.TaskAssociation {
	merged := cloneAssociations(left)
	indexes := map[string]int{}
	for i, association := range merged {
		indexes[association.Source+"\x00"+association.ExternalID] = i
	}
	for _, association := range right {
		key := association.Source + "\x00" + association.ExternalID
		if index, ok := indexes[key]; ok {
			merged[index] = cloneAssociations([]protocol.TaskAssociation{association})[0]
			continue
		}
		indexes[key] = len(merged)
		merged = append(merged, cloneAssociations([]protocol.TaskAssociation{association})[0])
	}
	return merged
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
		return refs[i].DisplayOrder < refs[j].DisplayOrder
	})
	return refs
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
		if ids[sourceRef.ID] && sourceRef.Active && authoritativeRef(sourceRef.Snapshot) {
			return true
		}
	}
	return false
}

func hasLifecycleSource(record TaskRecord, sourceRefs []SourceRefRecord, lifecycle protocol.SourceRefLifecycle) bool {
	ids := map[string]bool{}
	for _, id := range record.SourceRefIDs {
		ids[id] = true
	}
	for _, sourceRef := range sourceRefs {
		if ids[sourceRef.ID] && sourceRef.Active && authoritativeRef(sourceRef.Snapshot) && sourceRef.Snapshot.Lifecycle == lifecycle {
			return true
		}
	}
	return false
}

func hasKnownLifecycleSource(record TaskRecord, sourceRefs []SourceRefRecord, lifecycle protocol.SourceRefLifecycle) bool {
	ids := map[string]bool{}
	for _, id := range record.SourceRefIDs {
		ids[id] = true
	}
	for _, sourceRef := range sourceRefs {
		if ids[sourceRef.ID] && authoritativeRef(sourceRef.Snapshot) && sourceRef.Snapshot.Lifecycle == lifecycle {
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
