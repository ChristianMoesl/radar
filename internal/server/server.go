package server

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"strings"

	"radar.nvim/internal/filters"
	"radar.nvim/internal/protocol"
	"radar.nvim/internal/socket"
	"radar.nvim/internal/state"
)

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
			summary := s.store.Summary()
			_ = encoder.Encode(protocol.Response{OK: true, Summary: &summary, Tasks: s.store.Tasks(), Sources: s.store.Sources()})
			continue
		}
		switch req.Method {
		case "summary":
			tasks := s.filteredTasks()
			summary := filters.Summary(tasks)
			_ = encoder.Encode(protocol.Response{OK: true, Summary: &summary, Sources: s.store.Sources()})
		case "tasks":
			tasks := s.filteredTasks()
			summary := filters.Summary(tasks)
			_ = encoder.Encode(protocol.Response{OK: true, Summary: &summary, Tasks: tasks, Sources: s.store.Sources()})
		case "refresh":
			if s.refresh != nil {
				s.refresh()
			}
			tasks := s.filteredTasks()
			summary := filters.Summary(tasks)
			_ = encoder.Encode(protocol.Response{OK: true, Summary: &summary, Tasks: tasks, Sources: s.store.Sources()})
		case "reset":
			if s.reset != nil {
				if err := s.reset(); err != nil {
					s.logger.Warn("reset failed", "error", err)
					_ = encoder.Encode(protocol.Response{OK: false, Error: err.Error()})
					continue
				}
			}
			tasks := s.filteredTasks()
			summary := filters.Summary(tasks)
			_ = encoder.Encode(protocol.Response{OK: true, Summary: &summary, Tasks: tasks, Sources: s.store.Sources()})
		default:
			s.logger.Warn("unknown method", "method", req.Method)
			_ = encoder.Encode(protocol.Response{OK: false, Error: "unknown method: " + req.Method})
		}
	}

	if err := scanner.Err(); err != nil {
		s.logger.Warn("client read failed", "error", err)
	}
}

func (s *Server) filteredTasks() []protocol.Task {
	tasks := s.store.Tasks()
	cfg, err := filters.Load()
	if err != nil {
		s.logger.Warn("could not load filters", "error", err)
		return tasks
	}
	return filters.Apply(tasks, cfg)
}
