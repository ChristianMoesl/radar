package server

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"radar.nvim/internal/config"
	"radar.nvim/internal/filters"
	"radar.nvim/internal/protocol"
	"radar.nvim/internal/socket"
	"radar.nvim/internal/state"
	"radar.nvim/internal/version"
)

var watchTimeout = 30 * time.Second

type Server struct {
	store   *state.Store
	logger  *slog.Logger
	refresh func()
	reset   func() error
}

func New(store *state.Store, logger *slog.Logger, refresh func(), reset func() error) *Server {
	return &Server{store: store, logger: logger, refresh: refresh, reset: reset}
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
		default:
			s.logger.Warn("unknown method", "method", req.Method)
			_ = encoder.Encode(protocol.Response{OK: false, Error: "unknown method: " + req.Method})
		}
	}

	if err := scanner.Err(); err != nil {
		s.logger.Warn("client read failed", "error", err)
	}
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
