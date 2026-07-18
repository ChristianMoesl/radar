package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"radar/internal/cleanup"
	"radar/internal/config"
	"radar/internal/filters"
	"radar/internal/protocol"
	"radar/internal/socket"
	"radar/internal/state"
	"radar/internal/version"
)

var watchTimeout = 30 * time.Second

type Server struct {
	store          *state.Store
	logger         *slog.Logger
	refresh        func()
	reset          func() error
	garbageCollect func() (protocol.GarbageCollectionResult, error)
	cleanupService cleanup.Service
}

func New(store *state.Store, logger *slog.Logger, refresh func(), reset func() error, garbageCollect func() (protocol.GarbageCollectionResult, error), cleanupService cleanup.Service) *Server {
	return &Server{
		store:          store,
		logger:         logger,
		refresh:        refresh,
		reset:          reset,
		garbageCollect: garbageCollect,
		cleanupService: cleanupService,
	}
}

func (s *Server) ListenAndServe(path string) error {
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

	for {
		conn, err := listener.Accept()
		if err != nil {
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
			s.store.Acknowledge(taskID)
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
			currentRevision := s.store.WaitForRevision(ctx, revision)
			cancel()
			if currentRevision > revision {
				_ = encoder.Encode(s.tasksResponse())
				continue
			}
			_ = encoder.Encode(protocol.Response{OK: true, Revision: currentRevision})
			continue
		}
		switch req.Method {
		case "version":
			_ = encoder.Encode(protocol.Response{OK: true, Version: version.Current(), Revision: s.store.Revision()})
		case "summary":
			tasks := s.filteredTasks()
			summary := filters.Summary(tasks)
			_ = encoder.Encode(protocol.Response{OK: true, Revision: s.store.Revision(), Summary: &summary, Sources: s.store.Sources()})
		case "tasks":
			_ = encoder.Encode(s.tasksResponse())
		case "refresh":
			if s.refresh != nil {
				s.refresh()
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
					_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error(), Revision: s.store.Revision()})
					continue
				}
			}
			_ = encoder.Encode(protocol.Response{OK: true, Revision: s.store.Revision(), GarbageCollectionResult: &result})
		case "cleanup-preview":
			preview, err := s.cleanupPreview(context.Background(), req.TaskID)
			if err != nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error(), Revision: s.store.Revision()})
				continue
			}
			_ = encoder.Encode(protocol.Response{OK: true, Revision: s.store.Revision(), CleanupPreview: &preview})
		case "cleanup":
			result, err := s.cleanup(context.Background(), req.Cleanup)
			if err != nil {
				_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error(), Revision: s.store.Revision()})
				continue
			}
			_ = encoder.Encode(protocol.Response{OK: true, Revision: s.store.Revision(), CleanupResult: &result})
		default:
			s.logger.Warn("unknown method", "method", req.Method)
			_ = encoder.Encode(protocol.Response{OK: false, Error: "unknown method: " + req.Method})
		}
	}

	if err := scanner.Err(); err != nil {
		s.logger.Warn("client read failed", "error", err)
	}
}

func (s *Server) cleanupPreview(ctx context.Context, taskID int) (protocol.CleanupPreview, error) {
	task, ok := taskByID(s.filteredTasks(), taskID)
	if !ok {
		return protocol.CleanupPreview{}, fmt.Errorf("task %d not found", taskID)
	}
	return s.cleanupService.Preview(ctx, task)
}

func (s *Server) cleanup(ctx context.Context, preview *protocol.CleanupPreview) (protocol.CleanupResult, error) {
	if preview == nil {
		return protocol.CleanupResult{}, fmt.Errorf("cleanup targets are required")
	}
	return s.cleanupService.Execute(ctx, *preview, cleanup.ExecuteOptions{Force: true})
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
	tasks := s.filteredTasks()
	summary := filters.Summary(tasks)
	return protocol.Response{OK: true, Revision: s.store.Revision(), Summary: &summary, Tasks: tasks, Sources: s.store.Sources()}
}

func (s *Server) filteredTasks() []protocol.Task {
	tasks := s.store.Tasks()
	cfg, err := config.Load()
	if err != nil {
		s.logger.Warn("could not load config", "error", err)
		return tasks
	}
	return filters.Apply(tasks, cfg.Filters)
}
