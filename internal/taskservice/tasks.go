package taskservice

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/operationlock"
	"radar/internal/protocol"
	"radar/internal/state"
)

// Service coordinates cache publication with authored mutations. Slow source
// collection stays outside applyMu, so a note edit never waits for remote APIs
// or Git inspection. Completion/GC also perform a bounded runtime recheck under
// applyMu. Background refreshes are serialized by the daemon's collection mutex.
type Service struct {
	store               *state.Store
	logger              *slog.Logger
	integrations        integration.Registry
	applyMu             sync.Mutex
	authoringRevision   uint64
	resourceRevision    atomic.Uint64
	resourcePublication atomic.Pointer[resourcePublication]
}

type resourcePublication struct {
	epoch         uint64
	storeRevision int64
}

func New(store *state.Store, logger *slog.Logger, integrations integration.Registry) *Service {
	service := &Service{store: store, logger: logger, integrations: integrations}
	service.resourceRevision.Store(1)
	return service
}

func (s *Service) Refresh(ctx context.Context, localOnly bool) collector.Result {
	s.applyMu.Lock()
	revision := s.authoringRevision
	resourceRevision := s.resourceRevision.Load()
	s.applyMu.Unlock()

	var result collector.Result
	if localOnly {
		result = collector.CollectLocal(ctx, s.store.CollectionTasks(), s.logger, s.integrations.Sources())
	} else {
		result = collector.Collect(ctx, s.store.CollectionTasks(), s.logger, s.integrations.Sources())
	}

	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if resourceRevision != s.resourceRevision.Load() {
		// A completed resource mutation supersedes this entire observation.
		// Never publish it or clear the pending marker, even if every provider
		// reported success. The next scheduled collection starts a fresh read.
		s.logger.Debug("collection superseded by resource mutation")
		return collector.Result{Tasks: s.store.CollectionTasks(), Sources: s.store.Sources()}
	}
	if revision != s.authoringRevision {
		// A mutation won while collection was in flight. Re-read only the
		// author and activity sources: a pre-mutation busy snapshot must not
		// undo explicit Done. Retain every remote source's collected results.
		if provider, err := s.integrations.TaskAuthoring(); err == nil {
			for _, source := range append([]integration.Source{provider}, s.activitySources()...) {
				fresh := collector.Collect(ctx, s.store.CollectionTasks(), s.logger, []integration.Source{source})
				result.ReplaceSource(source.Descriptor().Name, fresh)
			}
		}
	}
	if localOnly {
		s.applyLocal(result)
		if collector.ReconcileAuthoredActivity(ctx, s.store.CollectionTasks(), &result, s.integrations.Sources(), s.logger) {
			s.applyLocal(result)
		} else {
			s.store.SetSources(mergeSourceStatuses(s.store.Sources(), result.Sources))
		}
	} else {
		s.store.SetTasks(result.Tasks)
		// New contributors must be durably bound before this refresh returns,
		// even when a mutation fenced its remote snapshot. Only lifecycle is
		// deferred: stale facts must not undo manual completion or reopening.
		reconcile := collector.ReconcileAuthoredTasks
		if revision != s.authoringRevision {
			reconcile = collector.ReconcileAuthoredBindings
		}
		if reconcile(ctx, s.store.CollectionTasks(), &result, s.integrations.Sources(), s.logger) {
			s.store.SetTasks(result.Tasks)
		}
		s.store.SetSources(result.Sources)
	}
	for _, taskID := range result.PendingAgentReviews {
		s.store.RequireAgentReview(taskID)
	}
	if ctx.Err() == nil && s.localCollectionComplete(result) {
		s.resourcePublication.Store(&resourcePublication{epoch: resourceRevision, storeRevision: s.store.Revision()})
	}
	return result
}

// MarkResourceRefreshPending acknowledges already-completed mutations without
// waiting for global collection. Existing snapshots remain explicitly pending.
func (s *Service) MarkResourceRefreshPending() {
	// Do not acquire the publication/authoring lock: a collection can be
	// reconciling unrelated work under it. Its captured epoch cannot certify
	// freshness after this atomic invalidation, even if publication is underway.
	s.resourceRevision.Add(1)
	s.store.NotifyChange()
}

// ResourceSnapshot pairs the projection revision with its epoch certificate,
// so an old snapshot cannot be paired with a newer "fresh" marker.
func (s *Service) ResourceSnapshot() protocol.Response {
	epoch := s.resourceRevision.Load()
	tasks, sources, revision := s.store.Snapshot()
	published := s.resourcePublication.Load()
	pending := published == nil || published.epoch != epoch || revision < published.storeRevision || epoch != s.resourceRevision.Load()
	return protocol.Response{Tasks: tasks, Sources: sources, Revision: revision, ResourceRefreshPending: pending}
}

func (s *Service) localCollectionComplete(result collector.Result) bool {
	for _, source := range collector.LocalSources(s.integrations.Sources()) {
		name := source.Descriptor().Name
		if result.Complete[name] {
			continue
		}
		disabled := false
		for _, status := range result.Sources {
			if status.Name == name && status.Status == "disabled" {
				disabled = true
			}
		}
		if !disabled {
			return false
		}
	}
	return true
}

func (s *Service) applyLocal(result collector.Result) {
	s.store.SetTasksForSources(result.Tasks, result.SourceNames)
	s.store.SetSources(mergeSourceStatuses(s.store.Sources(), result.Sources))
}

// GuardCleanup lets a reopen win during preview, but prevents lifecycle changes
// between GC's final eligibility check and the destructive provider operations.
func (s *Service) GuardCleanup(ctx context.Context, task protocol.Task, execute func() error) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if current, ok := taskByID(s.store.CollectionTasks(), task.ID); ok && current.AgentReviewPending {
		return fmt.Errorf("agent result review could not be persisted; refresh before cleanup")
	}
	// A note can be reopened outside Radar, or a mutation may have written the
	// note before returning an error. Re-read only the local authoring source;
	// cached completion alone must not authorize destructive expiry.
	if author, err := s.integrations.TaskAuthoring(); err == nil {
		if expected, relevant := authoredRef(task, author.Descriptor().Name); relevant {
			// No previous snapshots: an unreadable note must not be supplied
			// from cache. An unrelated invalid note must not block this one.
			fresh := collector.Collect(ctx, nil, s.logger, []integration.Source{author})
			verified := false
			for _, observed := range fresh.Tasks {
				for _, ref := range observed.SourceRefs {
					if ref.ID == expected.ID && ref.Signal == "done" && ref.Metadata["completed_at"] == expected.Metadata["completed_at"] && ref.Metadata["note_error"] == "" {
						verified = true
					}
				}
			}
			if !verified {
				return fmt.Errorf("task lifecycle changed or could not be verified before cleanup")
			}
		}
	}
	if err := s.requireIdle(ctx, task); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return execute()
}

func (s *Service) Reset() error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	s.authoringRevision++
	s.resourceRevision.Add(1)
	return s.store.Reset()
}

func (s *Service) MutateTask(ctx context.Context, method string, mutation *protocol.TaskMutation) (protocol.Task, error) {
	release, lockErr := operationlock.Acquire(false)
	if lockErr != nil {
		return protocol.Task{}, lockErr
	}
	defer release()
	if mutation == nil {
		return protocol.Task{}, fmt.Errorf("task mutation is required")
	}
	if method == "task-mute" || method == "task-unmute" {
		return s.SetMuted(ctx, mutation.TaskID, method == "task-mute")
	}
	provider, err := s.integrations.TaskAuthoring()
	if err != nil {
		return protocol.Task{}, err
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	var identity integration.AuthoredTaskIdentity
	switch method {
	case "task-create":
		identity, err = provider.Create(ctx, mutation.Title)
	case "task-done", "task-reopen", "task-priority", "task-title", "task-note":
		task, ok := taskByID(s.store.Tasks(), mutation.TaskID)
		if !ok {
			return protocol.Task{}, fmt.Errorf("task %d not found", mutation.TaskID)
		}
		ref, ok := authoredRef(task, provider.Descriptor().Name)
		if !ok {
			return protocol.Task{}, fmt.Errorf("task %d is not authored by %s", mutation.TaskID, provider.Descriptor().Label)
		}
		switch method {
		case "task-title", "task-note":
			details, ok := provider.(integration.TaskDetailsProvider)
			if !ok {
				return protocol.Task{}, fmt.Errorf("task author does not support note/title changes")
			}
			if method == "task-title" {
				identity, err = details.SetTitle(ctx, ref, mutation.Title)
			} else {
				identity, err = details.ReassociateNote(ctx, ref, mutation.NotePath)
			}
		case "task-done":
			if task.AgentReviewPending {
				return protocol.Task{}, fmt.Errorf("agent result review could not be saved; refresh to persist the handoff before marking Done")
			}
			if err := s.requireIdle(ctx, task); err != nil {
				return protocol.Task{}, err
			}
			// Include retained contributors that are absent from the served
			// projection, not just currently visible source refs.
			tracked, ok := taskByID(s.store.CollectionTasks(), task.ID)
			if !ok {
				return protocol.Task{}, fmt.Errorf("task %d is no longer tracked", task.ID)
			}
			identity, err = provider.SetLifecycle(ctx, ref, "done", tracked.SourceRefs)
		case "task-reopen":
			identity, err = provider.SetLifecycle(ctx, ref, "open", nil)
		case "task-priority":
			if task.Attention == "done" {
				return protocol.Task{}, fmt.Errorf("task %d is done and cannot change priority", mutation.TaskID)
			}
			identity, err = provider.SetPriority(ctx, ref, mutation.Priority)
		}
	default:
		return protocol.Task{}, fmt.Errorf("unknown task mutation: %s", method)
	}
	// Even an error may follow a successful note write (for example, an archive
	// failure). Fence any collection that started before this mutation.
	s.authoringRevision++
	if err != nil {
		return protocol.Task{}, err
	}
	refreshSources := []integration.Source{provider}
	if method == "task-done" {
		refreshSources = append(refreshSources, s.activitySources()...)
	}
	result := collector.Collect(ctx, s.store.CollectionTasks(), s.logger, refreshSources)
	s.applyLocal(result)
	for _, task := range s.store.Tasks() {
		for _, ref := range task.SourceRefs {
			if ref.ID == identity.SourceRefID {
				return task, nil
			}
		}
	}
	return protocol.Task{}, fmt.Errorf("authored task %q was not collected after mutation", identity.SourceRefID)
}

func authoredRef(task protocol.Task, source string) (protocol.SourceRef, bool) {
	for _, ref := range task.SourceRefs {
		if ref.Source == source && ref.Lifecycle == protocol.SourceRefLifecycleWorkItem && ref.Authority == protocol.SourceRefAuthorityPrimary {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func taskByID(tasks []protocol.Task, id int) (protocol.Task, bool) {
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return protocol.Task{}, false
}

func mergeSourceStatuses(previous []protocol.SourceStatus, updates []protocol.SourceStatus) []protocol.SourceStatus {
	byName := map[string]protocol.SourceStatus{}
	order := make([]string, 0, len(previous)+len(updates))
	for _, source := range previous {
		if _, ok := byName[source.Name]; !ok {
			order = append(order, source.Name)
		}
		byName[source.Name] = source
	}
	for _, source := range updates {
		if _, ok := byName[source.Name]; !ok {
			order = append(order, source.Name)
		}
		byName[source.Name] = source
	}
	merged := make([]protocol.SourceStatus, 0, len(order))
	for _, name := range order {
		merged = append(merged, byName[name])
	}
	return merged
}

func (s *Service) activitySources() []integration.Source {
	var sources []integration.Source
	for _, source := range s.integrations.Sources() {
		if _, ok := source.(integration.ActivityReader); ok {
			sources = append(sources, source)
		}
	}
	return sources
}

func (s *Service) requireIdle(ctx context.Context, task protocol.Task) error {
	activity, err := integration.ReadTaskActivity(ctx, task, s.integrations.Sources())
	if err != nil {
		return err
	}
	if activity != protocol.ActivityIdle {
		return fmt.Errorf("task %d has an agent %s; wait for agents to finish and review the result before marking Done or automatic cleanup", task.ID, activity.String())
	}
	return nil
}
