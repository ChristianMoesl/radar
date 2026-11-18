package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"radar/internal/cleanup"
	"radar/internal/collector"
	"radar/internal/config"
	"radar/internal/diagnostics"
	"radar/internal/integration"
	"radar/internal/phasetiming"
	"radar/internal/protocol"
	"radar/internal/socket"
	"radar/internal/state"
	"radar/internal/version"
	"sync"
)

var watchTimeout = 30 * time.Second

type Server struct {
	diagnosticRefresh    func(context.Context) (collector.Result, error)
	maintenanceMu        sync.Mutex
	refreshPlan          *diagnosticRefreshPlan
	store                *state.Store
	logger               *slog.Logger
	refresh              func()
	localRefresh         func()
	onReady              func(context.Context)
	taskMutation         func(context.Context, string, *protocol.TaskMutation) (protocol.Task, error)
	previewTaskDeletion  func(context.Context, int) (protocol.TaskDeletionPreview, error)
	deleteTask           func(context.Context, *protocol.TaskDeletionPreview) (protocol.TaskDeletionResult, error)
	reset                func() error
	garbageCollect       func() (protocol.GarbageCollectionResult, error)
	integrations         integration.Registry
	cleanupService       cleanup.Service
	markResourcesPending func()
	resourceSnapshot     func() protocol.Response
}

func New(store *state.Store, logger *slog.Logger, refresh func(), reset func() error, garbageCollect func() (protocol.GarbageCollectionResult, error), integrations integration.Registry, cleanupService cleanup.Service) *Server {
	return &Server{
		store:          store,
		logger:         logger,
		refresh:        refresh,
		reset:          reset,
		garbageCollect: garbageCollect,
		integrations:   integrations,
		cleanupService: cleanupService,
	}
}

func (s *Server) SetLocalRefresh(refresh func()) *Server {
	s.localRefresh = refresh
	return s
}

func (s *Server) SetResourcePublication(markPending func(), snapshot func() protocol.Response) *Server {
	s.markResourcesPending, s.resourceSnapshot = markPending, snapshot
	return s
}

func (s *Server) SetTaskMutation(mutate func(context.Context, string, *protocol.TaskMutation) (protocol.Task, error)) *Server {
	s.taskMutation = mutate
	return s
}

func (s *Server) SetTaskDeletion(preview func(context.Context, int) (protocol.TaskDeletionPreview, error), deleteTask func(context.Context, *protocol.TaskDeletionPreview) (protocol.TaskDeletionResult, error)) *Server {
	s.previewTaskDeletion, s.deleteTask = preview, deleteTask
	return s
}

// SetOnReady runs background startup work only after the socket is listening.
// ListenAndServe cancels and joins this work before returning on shutdown.
func (s *Server) SetOnReady(ready func(context.Context)) *Server {
	s.onReady = ready
	return s
}

func (s *Server) ListenAndServe(ctx context.Context, path string) error {
	if err := socket.EnsureDir(path); err != nil {
		return err
	}
	_ = os.Remove(path)

	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()

	s.logger.Info("server listening", "socket", path)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if s.onReady != nil {
			s.onReady(ctx)
		}
	}()
	defer func() { cancel(); <-done }()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.logger.Error("accept failed", "error", err)
			return err
		}
		s.logger.Debug("client connected")
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	defer s.logger.Debug("client disconnected")

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), protocol.AgentMaxFrameBytes+4096)
	encoder := json.NewEncoder(conn)

	for scanner.Scan() {
		var req protocol.Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			s.logger.Warn("invalid request", "error", err)
			_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error()})
			continue
		}

		s.logger.Debug("request received", "method", req.Method)
		if taskID, ok := strings.CutPrefix(req.Method, "ack:"); ok {
			if err := s.store.Acknowledge(taskID); err != nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error()})
				continue
			}
			_ = encoder.Encode(s.tasksResponse())
			continue
		}
		if revisionText, ok := strings.CutPrefix(req.Method, "watch:"); ok {
			revision, err := strconv.ParseInt(revisionText, 10, 64)
			if err != nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "invalid revision: " + revisionText, Revision: s.store.Revision()})
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), watchTimeout)
			s.store.WaitForRevision(ctx, revision)
			cancel()
			response := s.tasksResponse()
			if response.Revision <= revision {
				response.Tasks, response.Sources, response.Summary = nil, nil, nil
				response.Operations = nil
			}
			_ = encoder.Encode(response)
			continue
		}
		switch req.Method {
		case "resources-mutated":
			if s.markResourcesPending == nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "resource publication is unavailable", ResourceRefreshPending: true})
				continue
			}
			if req.ResourceMutation != nil && (req.ResourceMutation.TaskID < 0 || len(req.ResourceMutation.WorkspaceID) > 256) {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "invalid resource mutation target"})
				continue
			}
			s.markResourcesPending()
			operationID := ""
			if req.ResourceMutation != nil && (req.ResourceMutation.TaskID != 0 || req.ResourceMutation.WorkspaceID != "") {
				operationID = s.store.ReportResourceMutation(*req.ResourceMutation)
			}
			response := s.tasksResponse()
			response.OperationID = operationID
			_ = encoder.Encode(response)
		case "agent":
			if req.Agent == nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "agent request is required"})
				continue
			}
			result, err := s.integrations.ManageAgent(context.Background(), *req.Agent)
			if err != nil {
				code := 0
				var rpcErr *protocol.AgentRPCError
				if errors.As(err, &rpcErr) {
					code = rpcErr.Code
				}
				_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error(), AgentErrorCode: code})
				continue
			}
			_ = encoder.Encode(protocol.Response{OK: true, AgentResult: result})
		case "version":
			_ = encoder.Encode(protocol.Response{OK: true, Version: version.Current(), PID: os.Getpid(), Revision: s.store.Revision()})
		case "diagnostics-maintenance":
			// Unlike the general legacy RPC, maintenance accepts only this exact
			// envelope and typed fields, including no silently ignored selectors.
			var boundary struct {
				Method      string                                 `json:"method"`
				Maintenance *protocol.DiagnosticMaintenanceRequest `json:"maintenance"`
			}
			decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&boundary); err != nil || boundary.Maintenance == nil || req != (protocol.Request{Method: "diagnostics-maintenance", Maintenance: req.Maintenance}) {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "exact typed maintenance request required"})
				continue
			}
			view, err := s.maintainDiagnostics(context.Background(), *req.Maintenance)
			if err != nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error()})
				continue
			}
			_ = encoder.Encode(protocol.Response{OK: true, Maintenance: &view})
		case "diagnostics-probe":
			if req.Diagnostics == nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "explicit registered runtime, agent probe or retirement preview selectors are required"})
				continue
			}
			pending := s.resourceSnapshot != nil && s.resourceSnapshot().ResourceRefreshPending
			page, err := diagnostics.Probe(context.Background(), s.store, *req.Diagnostics, pending, s.integrations)
			if err != nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error()})
				continue
			}
			_ = encoder.Encode(protocol.Response{OK: true, Revision: page.Revision, Diagnostics: &page})
		case "diagnostics":
			if req.Diagnostics == nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "diagnostic selectors are required"})
				continue
			}
			pending := s.resourceSnapshot != nil && s.resourceSnapshot().ResourceRefreshPending
			if req.Diagnostics.Section == "overview" {
				// Overview has no scope/path/entity selectors. Read the daemon's
				// own fixed config location, not any path supplied by a client.
				if *req.Diagnostics != (protocol.DiagnosticsRequest{Section: "overview"}) {
					_ = encoder.Encode(protocol.Response{OK: false, Error: "diagnostic overview does not accept selectors"})
					continue
				}
				policy := config.DiagnosticSettings()
				policy.Scope = "daemon_process_global_defaults"
				// Do not call tasksResponse: overview must not invoke visibility
				// filters or inspect/export task or source payloads.
				statuses, revision := s.store.SourceStatusSnapshot()
				if len(statuses) > 32 {
					_ = encoder.Encode(protocol.Response{OK: false, Error: "diagnostic provider status exceeds bounds"})
					continue
				}
				sources := make([]protocol.DiagnosticProviderStatus, 0, len(statuses))
				valid := true
				for _, status := range statuses {
					if status.Name == "" {
						valid = false
						break
					}
					state := "unknown"
					switch status.Status {
					case "ok", "partial", "error", "disabled":
						state = status.Status
					}
					sources = append(sources, protocol.DiagnosticProviderStatus{Name: protocol.DiagnosticText(status.Name), Status: state, DetailsAvailable: status.Detail != ""})
				}
				if !valid {
					_ = encoder.Encode(protocol.Response{OK: false, Error: "diagnostic provider identity unavailable"})
					continue
				}
				response := protocol.Response{OK: true, Version: version.Current(), Revision: revision, ResourceRefreshPending: pending, DiagnosticOverview: &protocol.DiagnosticOverview{Schema: protocol.DiagnosticsVersion, Settings: policy, Providers: sources}}
				encoded, err := json.Marshal(response)
				if err != nil || len(encoded) > 32*1024 {
					_ = encoder.Encode(protocol.Response{OK: false, Error: "diagnostic overview exceeds output bound"})
					continue
				}
				_ = encoder.Encode(response)
				continue
			}
			page, err := diagnostics.Read(s.store, *req.Diagnostics, pending, s.integrations)
			if err != nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error()})
				continue
			}
			_ = encoder.Encode(protocol.Response{OK: true, Revision: page.Revision, Diagnostics: &page})
		case "summary":
			response := s.tasksResponse()
			response.Tasks = nil
			_ = encoder.Encode(response)
		case "tasks":
			_ = encoder.Encode(s.tasksResponse())
		case "refresh":
			if s.refresh != nil {
				s.refresh()
			}
			_ = encoder.Encode(s.tasksResponse())
		case "refresh-local":
			if s.localRefresh != nil {
				s.localRefresh()
			}
			_ = encoder.Encode(s.tasksResponse())
		case "reset":
			if s.reset != nil {
				if err := s.reset(); err != nil {
					s.logger.Warn("reset failed", "error", err)
					_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error(), Revision: s.store.Revision()})
					continue
				}
			}
			_ = encoder.Encode(s.tasksResponse())
		case "gc":
			result := protocol.GarbageCollectionResult{}
			if s.garbageCollect != nil {
				var err error
				result, err = s.garbageCollect()
				if err != nil {
					s.logger.Warn("garbage collection failed", "error", err)
					response := s.tasksResponse()
					response.OK, response.Error, response.GarbageCollectionResult = false, err.Error(), &result
					_ = encoder.Encode(response)
					continue
				}
			}
			response := s.tasksResponse()
			response.GarbageCollectionResult = &result
			_ = encoder.Encode(response)
		case "task-create", "task-done", "task-reopen", "task-priority", "task-mute", "task-unmute", "task-title", "task-note":
			if s.taskMutation == nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "task mutations are not configured", Revision: s.store.Revision()})
				continue
			}
			taskID := 0
			if req.TaskMutation != nil {
				taskID = req.TaskMutation.TaskID
			}
			op := s.store.BeginOperation(req.Method, taskID, "")
			task, err := s.taskMutation(context.Background(), req.Method, req.TaskMutation)
			if err != nil && s.markResourcesPending != nil {
				// Authoring may have succeeded before a later persistence or
				// recollection failure. Reconcile without replaying the command.
				s.markResourcesPending()
			}
			s.finishOperation(op, task.ID, err)
			if err != nil {
				response := s.tasksResponse()
				response.OperationID = op
				response.OK, response.Error = false, err.Error()
				_ = encoder.Encode(response)
				continue
			}
			response := s.taskMutationResponse(task)
			response.OperationID = op
			_ = encoder.Encode(response)
		case "task-delete-preview":
			if s.previewTaskDeletion == nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "task deletion is not configured", Revision: s.store.Revision()})
				continue
			}
			preview, err := s.previewTaskDeletion(context.Background(), req.TaskID)
			if err != nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error(), Revision: s.store.Revision()})
				continue
			}
			_ = encoder.Encode(protocol.Response{OK: true, Revision: s.store.Revision(), TaskDeletionPreview: &preview})
		case "task-delete":
			if s.deleteTask == nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: "task deletion is not configured", Revision: s.store.Revision()})
				continue
			}
			taskID := 0
			if req.TaskDeletion != nil {
				taskID = req.TaskDeletion.TaskID
			}
			op := s.store.BeginOperation(req.Method, taskID, "")
			result, err := s.deleteTask(context.Background(), req.TaskDeletion)
			if err != nil && s.markResourcesPending != nil {
				s.markResourcesPending()
			}
			s.finishOperation(op, taskID, err)
			if err != nil {
				response := s.tasksResponse()
				response.OperationID = op
				response.OK, response.Error = false, err.Error()
				_ = encoder.Encode(response)
				continue
			}
			response := s.tasksResponse()
			response.TaskDeletionResult = &result
			response.OperationID = op
			_ = encoder.Encode(response)
		case "cleanup-preview":
			preview, err := s.cleanupPreview(context.Background(), req.TaskID)
			if err != nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error(), Revision: s.store.Revision()})
				continue
			}
			_ = encoder.Encode(protocol.Response{OK: true, Revision: s.store.Revision(), CleanupPreview: &preview})
		case "cleanup":
			result, err := s.cleanup(context.Background(), req.Cleanup)
			response := s.tasksResponse()
			if s.resourceSnapshot != nil {
				// A queued reread may already have finished. Do not pair its
				// fresh revision with an obsolete pending result marker.
				result.ResourceRefreshPending = response.ResourceRefreshPending
			} else {
				response.ResourceRefreshPending = result.ResourceRefreshPending
			}
			response.CleanupResult = &result
			response.OperationID = result.OperationID
			if err != nil {
				response.OK, response.Error = false, err.Error()
			}
			_ = encoder.Encode(response)
		default:
			s.logger.Warn("unknown method", "method", req.Method)
			_ = encoder.Encode(protocol.Response{OK: false, Error: "unknown method: " + req.Method})
		}
	}

	if err := scanner.Err(); err != nil {
		s.logger.Warn("client read failed", "error", err)
	}
}

func (s *Server) taskMutationResponse(task protocol.Task) protocol.Response {
	response := s.tasksResponse()
	response.Task = &task
	return response
}

func (s *Server) cleanupPreview(ctx context.Context, taskID int) (protocol.CleanupPreview, error) {
	if s.resourceSnapshot != nil && s.resourceSnapshot().ResourceRefreshPending {
		return protocol.CleanupPreview{}, fmt.Errorf("resource list refresh pending; refresh Radar before previewing cleanup")
	}
	ctx = phasetiming.WithLogger(ctx, s.logger)
	task, ok := taskByID(s.filteredTasks(), taskID)
	if !ok {
		return protocol.CleanupPreview{}, fmt.Errorf("task %d not found", taskID)
	}
	return s.cleanupService.Preview(ctx, task, integration.CleanupConfirmed)
}

func (s *Server) cleanup(ctx context.Context, preview *protocol.CleanupPreview) (protocol.CleanupResult, error) {
	if preview == nil {
		return protocol.CleanupResult{}, fmt.Errorf("cleanup targets are required")
	}
	if s.resourceSnapshot != nil && s.resourceSnapshot().ResourceRefreshPending {
		return protocol.CleanupResult{TaskID: preview.TaskID, ResourceRefreshPending: true}, fmt.Errorf("resource list changed or is pending refresh; refresh and preview cleanup again")
	}
	ctx = phasetiming.WithLogger(ctx, s.logger)
	defer phasetiming.Start(ctx, "cleanup_response")()
	op := s.store.BeginOperation("cleanup", preview.TaskID, "")
	ctx = phasetiming.WithObserver(ctx, func(phase string, elapsed time.Duration) {
		s.store.RecordOperationPhase(op, phase, elapsed)
	})
	result, err := s.cleanupService.Execute(ctx, *preview, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
	result.OperationID = op
	// A provider can fail after changing part of its target. Mark pending even
	// on failure; successful prefix removals remain visible in the response.
	result.ResourceRefreshPending = true
	if s.markResourcesPending != nil {
		s.markResourcesPending()
	}
	status := "succeeded"
	if err != nil {
		status = "failed"
		if len(result.Targets) > 0 {
			status = "partial"
		}
	}
	s.store.FinishCleanupOperation(op, protocol.ManagedOperation{Status: status, TaskID: preview.TaskID, CompletedTargets: len(result.Targets), TotalTargets: len(preview.Targets)}, preview.Targets, result.ExecutionObservation)
	return result, err
}

func (s *Server) finishOperation(id string, taskID int, err error) {
	status := "succeeded"
	if err != nil {
		status = "failed"
	}
	s.store.FinishOperation(id, protocol.ManagedOperation{Status: status, TaskID: taskID})
}

func taskByID(tasks []protocol.Task, id int) (protocol.Task, bool) {
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return protocol.Task{}, false
}

func (s *Server) tasksResponse() protocol.Response {
	response := s.store.DashboardSnapshot()
	if s.resourceSnapshot != nil {
		response = s.resourceSnapshot()
	}
	tasks := s.integrations.FilterTasks(response.Tasks, s.logger)
	summary := protocol.SummarizeTasks(tasks)
	response.OK, response.Version, response.Summary, response.Tasks = true, version.Current(), &summary, tasks
	return response
}

func (s *Server) filteredTasks() []protocol.Task {
	return s.integrations.FilterTasks(s.store.Tasks(), s.logger)
}
