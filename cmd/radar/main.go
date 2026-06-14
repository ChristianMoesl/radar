package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"
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
	"radar.nvim/internal/tui"
	"radar.nvim/internal/workstream"
)

func main() {
	if len(os.Args) == 1 {
		runTUI()
		return
	}

	command := os.Args[1]
	switch command {
	case "create":
		runCreate(os.Args[2:])
	case "delete":
		runDelete(os.Args[2:])
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
	case "reset":
		callDaemon("reset")
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

func runTUI() {
	path, err := socket.Path()
	if err != nil {
		fatal(err)
	}
	if _, err := client.Call(path, "tasks"); err != nil {
		executable, executableErr := os.Executable()
		if executableErr != nil {
			fatal(executableErr)
		}
		if err := startDetached(executable, "daemon"); err != nil {
			fatal(err)
		}
		for range 20 {
			time.Sleep(50 * time.Millisecond)
			if _, err := client.Call(path, "tasks"); err == nil {
				break
			}
		}
	}
	if err := tui.Run(path); err != nil {
		fatal(err)
	}
}

func runCreate(args []string) {
	flags := flag.NewFlagSet("radar create", flag.ExitOnError)
	repo := flags.String("repo", "", "repository path")
	base := flags.String("base", "", "base branch or revision")
	name := flags.String("name", "", "workstream name")
	_ = flags.Parse(args)

	if *repo == "" || *base == "" || *name == "" {
		createUsage()
		os.Exit(2)
	}

	result, err := workstream.Create(context.Background(), workstream.ExecRunner{}, workstream.CreateOptions{
		Repo:   *repo,
		Base:   *base,
		Name:   *name,
		Switch: os.Getenv("TMUX") != "",
	})
	if err != nil {
		fatal(err)
	}
	printJSON(result)
}

func runDelete(args []string) {
	flags := flag.NewFlagSet("radar delete", flag.ExitOnError)
	path := flags.String("path", "", "workstream path")
	_ = flags.Parse(args)

	if *path == "" {
		deleteUsage()
		os.Exit(2)
	}

	result, err := workstream.Delete(context.Background(), workstream.ExecRunner{}, *path, "", false)
	if err != nil {
		fatal(err)
	}
	printJSON(result)
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
	collectionMu := &sync.Mutex{}
	refresh := refresher(context.Background(), store, logger, collectionMu)
	go refreshLoop(context.Background(), func() { refresh(false) })

	if err := server.New(store, logger, func() { refresh(true) }, resetter(context.Background(), store, logger, collectionMu)).ListenAndServe(path); err != nil {
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
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	process, err := os.StartProcess(name, append([]string{name}, args...), &os.ProcAttr{
		Files: []*os.File{devNull, devNull, devNull},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return err
	}
	return process.Release()
}

func refresher(ctx context.Context, store *state.Store, logger *slog.Logger, mu *sync.Mutex) func(force bool) {
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
		store.SetSources(result.Sources)
		logger.Debug("refresh finished", "tasks", len(result.Tasks), "sources", len(result.Sources))
	}
}

func resetter(ctx context.Context, store *state.Store, logger *slog.Logger, mu *sync.Mutex) func() error {
	return func() error {
		mu.Lock()
		defer mu.Unlock()

		logger.Debug("reset started")
		if err := store.Reset(); err != nil {
			return err
		}
		result := collector.Collect(ctx, nil, logger)
		store.SetTasks(result.Tasks)
		store.SetSources(result.Sources)
		logger.Debug("reset finished", "tasks", len(result.Tasks), "sources", len(result.Sources))
		return nil
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

func printJSON(value any) {
	out, err := json.Marshal(value)
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
	fmt.Fprintln(os.Stderr, `usage: radar [command]

Interactive:
  radar                         open the terminal UI

Workstreams:
  radar create --repo <repo> --base <branch> --name <name>
  radar delete --path <workstream-path>

Daemon and status:
  radar daemon
  radar status
  radar tasks
  radar refresh
  radar reset
  radar stop
  radar restart

Other:
  radar ack <task-id>
  radar log-path
  radar state-path
  radar filters-path
  radar rate-limit`)
}

func createUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar create --repo <repo> --base <branch> --name <name>

Options:
  --repo   repository path
  --base   base branch or revision, for example origin/main
  --name   workstream name; also used as the branch name`)
}

func deleteUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar delete --path <workstream-path>

Options:
  --path   workstream path to delete`)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "radar:", err)
	os.Exit(1)
}
