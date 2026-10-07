package taskservice

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/operationlock"
	"radar/internal/protocol"
	"radar/internal/state"
)

// Service coordinates cache publication with authored mutations. Slow source
// collection stays outside applyMu, so a note edit never waits for remote APIs,
// Git inspection, or runtime discovery. Background refreshes are serialized by
// the daemon's collection mutex; mutations use only this short publication lock.
type Service struct {
	store             *state.Store
	logger            *slog.Logger
	integrations      integration.Registry
	applyMu           sync.Mutex
	authoringRevision uint64
}

func New(store *state.Store, logger *slog.Logger, integrations integration.Registry) *Service {
	return &Service{store: store, logger: logger, integrations: integrations}
}

func (s *Service) Refresh(ctx context.Context, localOnly bool) collector.Result {
	s.applyMu.Lock()
	revision := s.authoringRevision
	s.applyMu.Unlock()

	var result collector.Result
	if localOnly {
		result = collector.CollectLocal(ctx, s.store.CollectionTasks(), s.logger, s.integrations.Sources())
	} else {
		result = collector.Collect(ctx, s.store.CollectionTasks(), s.logger, s.integrations.Sources())
	}

	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if revision != s.authoringRevision {
		// A mutation won while collection was in flight. Re-read only the
		// authoring source, retaining every other source's collected results.
		if provider, err := s.integrations.TaskAuthoring(); err == nil {
			fresh := collector.Collect(ctx, s.store.CollectionTasks(), s.logger, []integration.Source{provider})
			result.ReplaceSource(provider.Descriptor().Name, fresh)
		}
	}
	if localOnly {
		s.applyLocal(result)
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
	return result
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
					if ref.ID == expected.ID && ref.Signal == "done" && ref.Metadata["completed_at"] == expected.Metadata["completed_at"] {
						verified = true
					}
				}
			}
			if !verified {
				return fmt.Errorf("task lifecycle changed or could not be verified before cleanup")
			}
		}
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
	case "task-done", "task-reopen", "task-priority":
		task, ok := taskByID(s.store.Tasks(), mutation.TaskID)
		if !ok {
			return protocol.Task{}, fmt.Errorf("task %d not found", mutation.TaskID)
		}
		ref, ok := authoredRef(task, provider.Descriptor().Name)
		if !ok {
			return protocol.Task{}, fmt.Errorf("task %d is not authored by %s", mutation.TaskID, provider.Descriptor().Label)
		}
		switch method {
		case "task-done":
			identity, err = provider.SetLifecycle(ctx, ref, "done")
		case "task-reopen":
			identity, err = provider.SetLifecycle(ctx, ref, "open")
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
	result := collector.Collect(ctx, s.store.CollectionTasks(), s.logger, []integration.Source{provider})
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
