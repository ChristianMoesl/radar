package workspacegc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/phasetiming"
	"radar/internal/protocol"
	"radar/internal/state"
)

// Frozen production orchestration from 5de93a2e13ed, not the new helpers.
// Keep this independent reference when changing per-candidate preparation or
// execution. It validates preserved classification, native calls and guard order.
func frozenRunBeforeCandidateBoundary(ctx context.Context, store *state.Store, cleanupService cleanup.Service, logger *slog.Logger, now time.Time, options Options) (Result, error) {
	ctx = phasetiming.WithLogger(ctx, logger)
	plan, err := BuildPlan(store, now, options)
	if err != nil {
		return Result{}, err
	}
	result := Result{Skipped: append([]Skipped(nil), plan.Skipped...)}
	for _, candidate := range plan.Candidates {
		mode, err := CandidateMode(candidate, now)
		if err != nil {
			result.skip(candidate, err, logger)
			continue
		}
		preview, err := cleanupService.Preview(ctx, candidate.Task, mode)
		alreadyMissing := mode != integration.CleanupRetire && errors.Is(err, cleanup.ErrNoResources) && pathMissing(candidate.Path)
		if err != nil && !alreadyMissing {
			result.skip(candidate, err, logger)
			continue
		}
		selected, workspaceTarget := SelectTargetsForCandidate(preview, candidate)
		if !alreadyMissing && workspaceTarget == nil && (candidate.WorkspaceID != "" || !pathMissing(candidate.Path)) {
			result.skip(candidate, fmt.Errorf("matching workspace cleanup target was not found"), logger)
			continue
		}
		alreadyMissing = alreadyMissing || (mode != integration.CleanupRetire && len(selected.Targets) == 0 && pathMissing(candidate.Path))
		if messages := cleanup.AutomaticBlockingMessages(selected.Targets, mode == integration.CleanupExpired); len(messages) > 0 {
			result.skip(candidate, errors.New(messages[0]), logger)
			continue
		}
		runtimeChanged := false
		execute := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !candidateStillCurrent(store, candidate, now, options) {
				return fmt.Errorf("task completion changed or workspace candidate changed during cleanup; preview again")
			}
			if alreadyMissing {
				// No provider dispatch is needed, but stale lifecycle or path
				// evidence must not become a successful deletion result either.
				if !pathMissing(candidate.Path) {
					return fmt.Errorf("workspace path is no longer missing or cannot be inspected; preview again")
				}
				return ctx.Err()
			}
			cleaned, err := cleanupService.Execute(ctx, selected, cleanup.ExecuteOptions{Mode: mode})
			for _, target := range cleaned.Targets {
				runtimeChanged = runtimeChanged || target.Operation["runtime_retired"] == "true"
			}
			return err
		}
		var executeErr error
		if options.GuardExecution != nil {
			executeErr = options.GuardExecution(ctx, candidate.Task, execute)
		} else {
			executeErr = execute()
		}
		if err := executeErr; err != nil {
			result.skip(candidate, err, logger)
			continue
		}
		if mode == integration.CleanupRetire {
			if runtimeChanged {
				result.Retired = append(result.Retired, candidate)
			}
			continue
		}
		result.Deleted = append(result.Deleted, candidate)
		if logger != nil && !alreadyMissing {
			logger.Info("workspace gc deleted workspace", "task", candidate.TaskID, "path", candidate.Path, "expired", mode == integration.CleanupExpired)
		}
	}
	return result, nil
}

type parityReturnedProvider struct {
	gcProvider
	returned *map[string]string
}

func (p parityReturnedProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	target, err := p.gcProvider.Cleanup(ctx, req)
	*p.returned = target.Operation
	return target, err
}

func TestGCPerCandidateExtractionMatchesFrozenRun(t *testing.T) {
	for _, stage := range []string{"retire", "expire", "standalone"} {
		for _, scenario := range []string{"stable", "no-resources", "no-matching-anchor", "preview-error", "structural-blocker", "provider-error", "guard-denied", "guard-cancelled", "guard-post-return-mutation"} {
			t.Run(stage+"/"+scenario, func(t *testing.T) {
				store, root, done, tasks := expiryFixture(t)
				t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
				now := done.Add(25 * time.Hour)
				base := Options{WorkspaceRoot: root}
				if stage == "expire" {
					now = done.Add(ExpiryRetention)
				}
				if stage == "standalone" {
					tasks[1] = makeTask("in_progress", "worktree", worktreeRef(filepath.Join(root, "standalone"), "acme/app", "work"))
					tasks[2].SourceRefs[0].WorkspaceID = ""
					base.IgnoreRetention = true
					store.SetTasks(tasks)
				}
				var results []Result
				var allCalls [][]cleanupCall
				var allGuards []int
				var errorsReturned []string
				for _, run := range []func(context.Context, *state.Store, cleanup.Service, *slog.Logger, time.Time, Options) (Result, error){frozenRunBeforeCandidateBoundary, Run} {
					ctx, cancel := context.WithCancel(context.Background())
					var calls []cleanupCall
					var returned map[string]string
					guards := 0
					options := base
					options.GuardExecution = func(_ context.Context, _ protocol.Task, execute func() error) error {
						guards++
						if scenario == "guard-denied" {
							return errors.New("authored admission denied")
						}
						if scenario == "guard-cancelled" {
							cancel()
						}
						err := execute()
						if scenario == "guard-post-return-mutation" && returned != nil {
							returned["runtime_retired"] = "false"
						}
						return err
					}
					workspace := expiryProvider{gcProvider: gcProvider{name: "workspace", calls: &calls}}
					if scenario == "preview-error" {
						workspace.previewErr = errors.New("native preview unavailable")
					}
					if scenario == "structural-blocker" {
						workspace.hardBlock = true
					}
					runtime := gcProvider{name: "tmux", calls: &calls}
					if scenario == "provider-error" {
						runtime.failPath = tasks[1].SourceRefs[0].Path
					}
					captured := parityReturnedProvider{gcProvider: runtime, returned: &returned}
					providers := []integration.CleanupProvider{captured, gcProvider{name: "git", calls: &calls}, workspace}
					if scenario == "no-resources" {
						providers = nil
					}
					if scenario == "no-matching-anchor" {
						providers = []integration.CleanupProvider{captured}
					}
					result, err := run(ctx, store, cleanup.New(providers), nil, now, options)
					cancel()
					results = append(results, result)
					allCalls = append(allCalls, calls)
					allGuards = append(allGuards, guards)
					errorsReturned = append(errorsReturned, fmt.Sprint(err))
				}
				if !reflect.DeepEqual(results[0], results[1]) || !reflect.DeepEqual(allCalls[0], allCalls[1]) || allGuards[0] != allGuards[1] || errorsReturned[0] != errorsReturned[1] {
					t.Fatalf("native GC orchestration contract changed: results=%+v calls=%+v guards=%+v errors=%+v", results, allCalls, allGuards, errorsReturned)
				}
			})
		}
	}
}
