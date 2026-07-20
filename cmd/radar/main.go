package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	exec "os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"radar/internal/app"
	"radar/internal/cleanup"
	"radar/internal/client"
	"radar/internal/collector"
	"radar/internal/config"
	"radar/internal/filters"
	"radar/internal/integration"
	"radar/internal/integration/github"
	"radar/internal/logging"
	"radar/internal/notification"
	"radar/internal/process"
	"radar/internal/protocol"
	"radar/internal/sbxauth"
	"radar/internal/server"
	"radar/internal/socket"
	"radar/internal/state"
	"radar/internal/tui"
	"radar/internal/version"
	"radar/internal/workspacegc"
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
	case "fork":
		runFork(os.Args[2:])
	case "cleanup":
		runCleanup(os.Args[2:])
	case "gc":
		runGarbageCollection(os.Args[2:])
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
	case "config-path":
		printConfigPath()
	case "rate-limit", "rate-limits":
		printRateLimit()
	case "version":
		printVersion()
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func runTUI() {
	runTUIWithMode("")
}

func runTUIWithMode(mode string) {
	path, err := socket.Path()
	if err != nil {
		fatal(err)
	}
	if err := ensureDaemonCurrent(path); err != nil {
		fatal(err)
	}
	response, err := client.Call(path, "tasks")
	if err != nil {
		if err := startDaemonAndWait(path); err != nil {
			fatal(err)
		}
		response, err = client.Call(path, "tasks")
		if err != nil {
			fatal(err)
		}
	}
	sbxLoggedIn := false
	if shouldEnsureSBXLogin(mode, response.Sources) {
		sbxLoggedIn, err = ensureSBXLogin(context.Background())
		if err != nil {
			fatal(err)
		}
	}
	if sbxLoggedIn {
		if res, err := client.Call(path, "refresh"); err != nil {
			fatal(err)
		} else if !res.OK {
			fatal(errors.New(res.Error))
		}
	}
	if mode == "create" {
		if err := tui.RunCreate(path); err != nil {
			fatal(err)
		}
		return
	}
	if mode == "fork" {
		if err := tui.RunFork(path); err != nil {
			fatal(err)
		}
		return
	}
	if err := tui.Run(path); err != nil {
		fatal(err)
	}
}

func runCreate(args []string) {
	flags := flag.NewFlagSet("radar create", flag.ExitOnError)
	repo := flags.String("repo", "", "repository path")
	base := flags.String("base", "", "base branch or revision")
	name := flags.String("name", "", "workspace name")
	_ = flags.Parse(args)

	if *repo == "" && *base == "" && *name == "" {
		runTUIWithMode("create")
		return
	}
	if *repo == "" || *base == "" || *name == "" {
		createUsage()
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	if cfg.Sandbox != nil {
		if _, err := ensureSBXLogin(context.Background()); err != nil {
			fatal(err)
		}
	}
	integrations := app.DefaultIntegrationSet()
	result, err := integrations.Workspace.Create(context.Background(), integration.CreateWorkspaceRequest{
		Repo:            *repo,
		Base:            *base,
		Name:            *name,
		Model:           cfg.Model,
		Thinking:        cfg.Thinking,
		Sandbox:         cfg.Sandbox != nil,
		SandboxTemplate: cfg.SandboxTemplate,
		Switch:          os.Getenv("TMUX") != "",
	})
	if err != nil {
		fatal(err)
	}
	printJSON(result)
}

func runFork(args []string) {
	flags := flag.NewFlagSet("radar fork", flag.ExitOnError)
	_ = flags.Parse(args)
	if flags.NArg() != 0 {
		forkUsage()
		os.Exit(2)
	}
	runTUIWithMode("fork")
}

func shouldEnsureSBXLogin(mode string, sources []protocol.SourceStatus) bool {
	if mode == "create" || mode == "fork" {
		return true
	}
	for _, source := range sources {
		if source.Name == "sbx" && source.Status == "error" && sbxauth.IsRequired(source.Detail) {
			return true
		}
	}
	return false
}

func ensureSBXLogin(ctx context.Context) (bool, error) {
	if _, err := exec.LookPath("sbx"); err != nil {
		return false, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	check := exec.CommandContext(checkCtx, "sbx", "ls", "--json")
	output, err := check.CombinedOutput()
	if err == nil {
		return false, nil
	}
	if !sbxauth.IsRequired(string(output) + "\n" + err.Error()) {
		return false, nil
	}
	fmt.Fprintln(os.Stderr, "radar: sbx is not signed in; starting sbx login")
	login := exec.CommandContext(ctx, "sbx", "login")
	login.Stdin = os.Stdin
	login.Stdout = os.Stdout
	login.Stderr = os.Stderr
	if err := login.Run(); err != nil {
		return false, fmt.Errorf("sbx login failed: %w", err)
	}
	return true, nil
}

func runCleanup(args []string) {
	if len(args) != 1 {
		cleanupUsage()
		os.Exit(2)
	}
	taskID, err := strconv.Atoi(args[0])
	if err != nil || taskID <= 0 {
		cleanupUsage()
		os.Exit(2)
	}
	path, err := socket.Path()
	if err != nil {
		fatal(err)
	}
	if err := ensureDaemonCurrent(path); err != nil {
		fatal(err)
	}
	response, err := client.CallRequest(path, protocol.Request{Method: "cleanup-preview", TaskID: taskID})
	if err != nil {
		if startErr := startDaemonAndWait(path); startErr != nil {
			fatal(startErr)
		}
		response, err = client.CallRequest(path, protocol.Request{Method: "cleanup-preview", TaskID: taskID})
		if err != nil {
			fatal(err)
		}
	}
	if !response.OK {
		fatal(errors.New(response.Error))
	}
	if response.CleanupPreview == nil {
		fatal(errors.New("cleanup preview response was empty"))
	}
	preview := response.CleanupPreview
	for _, target := range preview.Targets {
		if target.Source == "sbx" {
			if _, err := ensureSBXLogin(context.Background()); err != nil {
				fatal(err)
			}
			break
		}
	}
	fmt.Fprintf(os.Stderr, "Local resources linked to %q:\n", preview.TaskTitle)
	for _, target := range preview.Targets {
		fmt.Fprintln(os.Stderr, "  - "+cleanupTargetDescription(target))
	}
	fmt.Fprintf(os.Stderr, "Clean up all %d local resource(s)? [y/N] ", len(preview.Targets))
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		fmt.Fprintln(os.Stderr, "Cleanup cancelled")
		return
	}
	response, err = client.CallRequest(path, protocol.Request{Method: "cleanup", Cleanup: preview})
	if err != nil {
		fatal(err)
	}
	if !response.OK {
		fatal(errors.New(response.Error))
	}
	if response.CleanupResult == nil {
		fatal(errors.New("cleanup response was empty"))
	}
	printJSON(response.CleanupResult)
}

func runGarbageCollection(args []string) {
	if len(args) != 0 {
		garbageCollectionUsage()
		os.Exit(2)
	}
	path, err := socket.Path()
	if err != nil {
		fatal(err)
	}
	if err := ensureDaemonCurrent(path); err != nil {
		fatal(err)
	}
	response, err := client.Call(path, "gc")
	if err != nil {
		if startErr := startDaemonAndWait(path); startErr != nil {
			fatal(startErr)
		}
		response, err = client.Call(path, "gc")
		if err != nil {
			fatal(err)
		}
	}
	if !response.OK {
		fatal(errors.New(response.Error))
	}
	if response.GarbageCollectionResult == nil {
		fatal(errors.New("garbage collection response was empty"))
	}
	printJSON(response.GarbageCollectionResult)
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

	logger.Info("daemon starting", "socket", path, "log", logPath, "pid", os.Getpid(), "pid_file", pidPath, "version", version.Current())

	if configPath, err := config.EnsureFile(); err != nil {
		logger.Warn("could not initialize config file", "error", err)
	} else {
		logger.Info("config file ready", "path", configPath)
	}

	store, err := state.NewStore(logger)
	if err != nil {
		logger.Error("could not initialize state", "error", err)
		fatal(err)
	}
	integrations := app.DefaultIntegrationSet()
	cleanupService := cleanup.New(integrations.CleanupProviders)
	notificationService := notification.New(logger)
	collectionMu := &sync.Mutex{}
	refresh := refresher(context.Background(), store, logger, collectionMu, integrations, cleanupService, notificationService)
	garbageCollect := garbageCollector(context.Background(), store, logger, collectionMu, integrations, cleanupService)
	if collectionDisabled() {
		logger.Info("source collection disabled", "env", "RADAR_DISABLE_COLLECTION")
	} else {
		go refreshLoop(context.Background(), refresh)
	}

	if err := server.New(store, logger, func() { refresh(refreshFull, true) }, resetter(context.Background(), store, logger, collectionMu, integrations), garbageCollect, cleanupService).ListenAndServe(path); err != nil {
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
	if err := restartDaemonAndWait(""); err != nil {
		fatal(err)
	}
	fmt.Println("radar daemon restarted")
}

func ensureDaemonCurrent(socketPath string) error {
	res, callErr := client.Call(socketPath, "version")
	if callErr == nil {
		if res.OK && res.Version == version.Current() {
			return nil
		}
		return restartDaemonAndWait(socketPath)
	}

	pids, pidErr := process.DaemonPIDs()
	if pidErr != nil {
		return pidErr
	}
	if len(pids) > 0 {
		return restartDaemonAndWait(socketPath)
	}
	return nil
}

func restartDaemonAndWait(socketPath string) error {
	_ = process.Stop()
	return startDaemonAndWait(socketPath)
}

func startDaemonAndWait(socketPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := startDetached(executable, "daemon"); err != nil {
		return err
	}
	if socketPath == "" {
		return nil
	}
	for range 100 {
		time.Sleep(50 * time.Millisecond)
		res, err := client.Call(socketPath, "version")
		if err == nil && res.OK && res.Version == version.Current() {
			return nil
		}
	}
	return fmt.Errorf("radar daemon did not start with matching version")
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

type refreshScope string

const (
	refreshFull  refreshScope = "full"
	refreshLocal refreshScope = "local"
)

func collectionDisabled() bool {
	return os.Getenv("RADAR_DISABLE_COLLECTION") == "1"
}

func refresher(ctx context.Context, store *state.Store, logger *slog.Logger, mu *sync.Mutex, integrations integration.Set, cleanupService cleanup.Service, notificationService notification.Service) func(refreshScope, bool) {
	var lastFullRefresh time.Time
	var lastWorkspaceGC time.Time

	return func(scope refreshScope, force bool) {
		if collectionDisabled() {
			logger.Debug("refresh skipped; source collection disabled", "scope", scope, "force", force)
			return
		}
		mu.Lock()

		if scope == refreshFull && !force && time.Since(lastFullRefresh) < 5*time.Minute {
			mu.Unlock()
			logger.Debug("full refresh skipped; recently refreshed")
			return
		}
		if scope == refreshFull {
			lastFullRefresh = time.Now()
		}

		logger.Debug("refresh started", "scope", scope, "force", force)
		previous := store.Tasks()
		var result collector.Result
		if scope == refreshLocal {
			result = collector.CollectLocal(ctx, previous, logger, integrations.Sources)
			store.SetTasksForSources(result.Tasks, result.SourceNames)
			store.SetSources(mergeSourceStatuses(store.Sources(), result.Sources))
		} else {
			result = collector.Collect(ctx, previous, logger, integrations.Sources)
			store.SetTasks(result.Tasks)
			store.SetSources(result.Sources)
		}
		if time.Since(lastWorkspaceGC) >= time.Hour {
			lastWorkspaceGC = time.Now()
			gcResult, err := workspacegc.Run(ctx, store, cleanupService, logger, time.Now(), workspacegc.Options{})
			if err != nil {
				logger.Warn("workspace gc failed", "error", err)
			} else if len(gcResult.Deleted) > 0 {
				result = collector.CollectLocal(ctx, store.Tasks(), logger, integrations.Sources)
				store.SetTasksForSources(result.Tasks, result.SourceNames)
				store.SetSources(mergeSourceStatuses(store.Sources(), result.Sources))
				logger.Debug("workspace gc refresh finished", "deleted", len(gcResult.Deleted), "tasks", len(result.Tasks))
			}
		}
		current := store.Tasks()
		mu.Unlock()

		notifyActionableTransitions(ctx, previous, current, logger, notificationService)
		logger.Debug("refresh finished", "scope", scope, "tasks", len(result.Tasks), "sources", len(result.Sources))
	}
}

func garbageCollector(ctx context.Context, store *state.Store, logger *slog.Logger, mu *sync.Mutex, integrations integration.Set, cleanupService cleanup.Service) func() (protocol.GarbageCollectionResult, error) {
	return func() (protocol.GarbageCollectionResult, error) {
		mu.Lock()
		defer mu.Unlock()

		result, err := workspacegc.Run(ctx, store, cleanupService, logger, time.Now(), workspacegc.Options{})
		if err != nil {
			return protocol.GarbageCollectionResult{}, err
		}
		if len(result.Deleted) > 0 {
			collected := collector.CollectLocal(ctx, store.Tasks(), logger, integrations.Sources)
			store.SetTasksForSources(collected.Tasks, collected.SourceNames)
			store.SetSources(mergeSourceStatuses(store.Sources(), collected.Sources))
			logger.Debug("manual workspace gc refresh finished", "deleted", len(result.Deleted), "tasks", len(collected.Tasks))
		}
		return garbageCollectionResult(result), nil
	}
}

func garbageCollectionResult(result workspacegc.Result) protocol.GarbageCollectionResult {
	converted := protocol.GarbageCollectionResult{
		Deleted: make([]protocol.GarbageCollectionItem, 0, len(result.Deleted)),
		Skipped: make([]protocol.GarbageCollectionItem, 0, len(result.Skipped)),
	}
	for _, deleted := range result.Deleted {
		converted.Deleted = append(converted.Deleted, protocol.GarbageCollectionItem{TaskID: deleted.TaskID, Path: deleted.Path})
	}
	for _, skipped := range result.Skipped {
		converted.Skipped = append(converted.Skipped, protocol.GarbageCollectionItem{TaskID: skipped.TaskID, Path: skipped.Path, Reason: skipped.Reason})
	}
	return converted
}

func notifyActionableTransitions(ctx context.Context, previous, current []protocol.Task, logger *slog.Logger, notificationService notification.Service) {
	cfg, err := config.Load()
	if err != nil {
		logger.Warn("notifications skipped; could not load config", "error", err)
		return
	}
	notificationService.NotifyTransitions(ctx, filters.Apply(previous, cfg.Filters), filters.Apply(current, cfg.Filters))
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

func resetter(ctx context.Context, store *state.Store, logger *slog.Logger, mu *sync.Mutex, integrations integration.Set) func() error {
	return func() error {
		mu.Lock()
		defer mu.Unlock()

		logger.Debug("reset started")
		if err := store.Reset(); err != nil {
			return err
		}
		if collectionDisabled() {
			logger.Debug("reset finished without collection; source collection disabled")
			return nil
		}
		result := collector.Collect(ctx, nil, logger, integrations.Sources)
		store.SetTasks(result.Tasks)
		store.SetSources(result.Sources)
		logger.Debug("reset finished", "tasks", len(result.Tasks), "sources", len(result.Sources))
		return nil
	}
}

func refreshLoop(ctx context.Context, refresh func(refreshScope, bool)) {
	refresh(refreshFull, false)
	localTicker := time.NewTicker(15 * time.Second)
	defer localTicker.Stop()
	fullTicker := time.NewTicker(5 * time.Minute)
	defer fullTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-localTicker.C:
			refresh(refreshLocal, false)
		case <-fullTicker.C:
			refresh(refreshFull, false)
		}
	}
}

func callDaemon(method string) {
	path, err := socket.Path()
	if err != nil {
		fatal(err)
	}

	if err := ensureDaemonCurrent(path); err != nil {
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

func printConfigPath() {
	path, err := config.EnsureFile()
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

func printVersion() {
	fmt.Println(version.Text())
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: radar [command]

Interactive:
  radar                         open the terminal UI

Workspaces:
  radar create
  radar create --repo <repo> --base <branch> --name <name>
  radar fork
  radar cleanup <task-id>
  radar gc

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
  radar config-path
  radar rate-limit
  radar version`)
}

func createUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar create
       radar create --repo <repo> --base <branch> --name <name>

Options:
  --repo   repository path
  --base   base branch or revision, for example origin/main
  --name   workspace name; also used to derive a sanitized branch name`)
}

func forkUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar fork

Fork the current tmux workspace into a sibling workspace and fork its Pi session.`)
}

func cleanupTargetDescription(target protocol.CleanupTarget) string {
	switch target.Kind {
	case "worktree":
		description := "worktree " + target.Path
		if target.Dirty {
			description += " (dirty; uncommitted changes will be discarded)"
		}
		return description
	case "session":
		return "tmux session " + target.SessionName
	case "sandbox":
		return "SBX sandbox " + target.SandboxName
	default:
		return target.Source + " " + target.Title
	}
}

func cleanupUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar cleanup <task-id>

Clean up every local worktree, tmux session, and SBX sandbox linked to the task.`)
}

func garbageCollectionUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar gc

Garbage-collect eligible local workspaces using the conservative automatic cleanup rules.`)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "radar:", err)
	os.Exit(1)
}
