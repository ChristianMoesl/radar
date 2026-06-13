package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"radar.nvim/internal/client"
	"radar.nvim/internal/collector"
	"radar.nvim/internal/filters"
	"radar.nvim/internal/github"
	"radar.nvim/internal/logging"
	"radar.nvim/internal/process"
	"radar.nvim/internal/server"
	"radar.nvim/internal/socket"
	"radar.nvim/internal/state"
)

func main() {
	command := "status"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	switch command {
	case "daemon":
		runDaemon()
	case "stop":
		stopDaemon()
	case "restart":
		restartDaemon()
	case "summary", "status":
		callDaemon("summary")
	case "tasks":
		callDaemon("tasks")
	case "refresh":
		callDaemon("refresh")
	case "ack":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: radar ack <task-id>")
			os.Exit(2)
		}
		callDaemon("ack:" + os.Args[2])
	case "log-path", "logs":
		printLogPath()
	case "state-path":
		printStatePath()
	case "filters-path":
		printFiltersPath()
	case "rate-limit", "rate-limits":
		printRateLimit()
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func runDaemon() {
	if pids, err := process.DaemonPIDs(); err == nil && len(pids) > 0 {
		fmt.Fprintf(os.Stderr, "radar daemon already running: %v\n", pids)
		return
	}

	logger, file, logPath, err := logging.New()
	if err != nil {
		fatal(err)
	}
	defer file.Close()

	path, err := socket.Path()
	if err != nil {
		logger.Error("could not determine socket path", "error", err)
		fatal(err)
	}

	fmt.Fprintf(os.Stderr, "radar daemon listening on %s\n", path)
	fmt.Fprintf(os.Stderr, "radar daemon logging to %s\n", logPath)
	pidPath, err := process.WritePID()
	if err != nil {
		logger.Error("could not write pid file", "error", err)
		fatal(err)
	}
	defer os.Remove(pidPath)

	logger.Info("daemon starting", "socket", path, "log", logPath, "pid", os.Getpid(), "pid_file", pidPath)

	if filtersPath, err := filters.EnsureFile(); err != nil {
		logger.Warn("could not initialize filters file", "error", err)
	} else {
		logger.Info("filters file ready", "path", filtersPath)
	}

	store, err := state.NewStore(logger)
	if err != nil {
		logger.Error("could not initialize state", "error", err)
		fatal(err)
	}
	refresh := refresher(context.Background(), store, logger)
	go refreshLoop(context.Background(), func() { refresh(false) })

	if err := server.New(store, logger, func() { refresh(true) }).ListenAndServe(path); err != nil {
		logger.Error("daemon stopped", "error", err)
		fatal(err)
	}
}

func stopDaemon() {
	pids, _ := process.DaemonPIDs()
	if err := process.Stop(); err != nil {
		fatal(err)
	}
	if len(pids) == 0 {
		fmt.Println("radar daemon was not running")
		return
	}
	fmt.Printf("radar daemon stopped: %v\n", pids)
}

func restartDaemon() {
	_ = process.Stop()
	cmd, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	if err := startDetached(cmd, "daemon"); err != nil {
		fatal(err)
	}
	fmt.Println("radar daemon restarted")
}

func startDetached(name string, args ...string) error {
	process, err := os.StartProcess(name, append([]string{name}, args...), &os.ProcAttr{
		Files: []*os.File{nil, os.Stderr, os.Stderr},
	})
	if err != nil {
		return err
	}
	return process.Release()
}

func refresher(ctx context.Context, store *state.Store, logger *slog.Logger) func(force bool) {
	var mu sync.Mutex
	var lastRefresh time.Time

	return func(force bool) {
		mu.Lock()
		defer mu.Unlock()

		if !force && time.Since(lastRefresh) < 5*time.Minute {
			logger.Debug("refresh skipped; recently refreshed")
			return
		}
		lastRefresh = time.Now()

		logger.Debug("refresh started", "force", force)
		previous := store.Tasks()
		result := collector.Collect(ctx, previous, logger)
		store.SetTasks(result.Tasks)
		store.SetServices(result.Services)
		logger.Debug("refresh finished", "tasks", len(result.Tasks), "services", len(result.Services))
	}
}

func refreshLoop(ctx context.Context, refresh func()) {
	refresh()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func callDaemon(method string) {
	path, err := socket.Path()
	if err != nil {
		fatal(err)
	}

	res, err := client.Call(path, method)
	if err != nil {
		fatal(err)
	}
	if !res.OK {
		fatal(errors.New(res.Error))
	}

	out, err := json.Marshal(res)
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(out))
}

func printLogPath() {
	path, err := logging.Path()
	if err != nil {
		fatal(err)
	}
	fmt.Println(path)
}

func printStatePath() {
	path, err := state.Path()
	if err != nil {
		fatal(err)
	}
	fmt.Println(path)
}

func printFiltersPath() {
	path, err := filters.EnsureFile()
	if err != nil {
		fatal(err)
	}
	fmt.Println(path)
}

func printRateLimit() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	summary, err := github.RateLimitSummary(context.Background(), logger)
	if err != nil {
		fatal(err)
	}
	fmt.Println(summary)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: radar [daemon|stop|restart|status|summary|tasks|refresh|ack <task-id>|log-path|state-path|filters-path|rate-limit]")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "radar:", err)
	os.Exit(1)
}
