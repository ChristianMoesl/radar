package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"radar/internal/app"
	"radar/internal/cleanup"
	"radar/internal/client"
	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/onboarding"
	"radar/internal/logging"
	"radar/internal/notification"
	"radar/internal/operationlock"
	"radar/internal/process"
	"radar/internal/protocol"
	"radar/internal/server"
	"radar/internal/socket"
	"radar/internal/state"
	"radar/internal/taskservice"
	"radar/internal/tui"
	"radar/internal/version"
	"radar/internal/workspacegc"
)

func main() {
	_ = version.Current()
	flags := outputFlags("radar")
	flags.Usage = usage
	_ = flags.Parse(os.Args[1:])
	args := flags.Args()
	if len(args) == 0 {
		rejectJSON("(interactive UI)")
		runTUI()
		return
	}
	command, args := args[0], args[1:]
	switch command {
	case "update":
		runUpdate(args)
	case "setup":
		flags := outputFlags("radar setup")
		_ = parseFlags(flags, args)
		rejectJSON(command)
		if flags.NArg() == 1 && flags.Arg(0) == "notifications" {
			if err := notification.Setup(os.Stdin, os.Stdout); err != nil {
				fatal(err)
			}
			return
		}
		if flags.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "usage: radar setup [notifications]")
			os.Exit(2)
		}
		runOnboarding()
	case "create":
		runCreate(args)
	case "reconcile-workspace":
		runReconcileWorkspace(args)
	case "workspace-context":
		runWorkspaceContext(args)
	case "repository-refs":
		runRepositoryRefs(args)
	case "task":
		runTask(args)
	case "fork":
		runFork(args)
	case "cleanup":
		runCleanup(args)
	case "gc":
		runGarbageCollection(args)
	case "activity":
		runActivity(args)
	case "ack":
		args = positionalArgs(command, args, 1)
		callDaemon("ack:" + strconv.Itoa(parseTaskID(args[0])))
	default:
		positionalArgs(command, args, 0)
		switch command {
		case "daemon":
			rejectJSON(command)
			runDaemon()
		case "stop":
			stopDaemon()
		case "restart":
			restartDaemon()
		case "summary", "status":
			callDaemon("summary")
		case "tasks", "refresh", "reset":
			callDaemon(command)
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
		case "help":
			usage()
		default:
			usage()
			os.Exit(2)
		}
	}
}

func runTUI() {
	runTUIWithMode("")
}

func runTUIWithMode(mode string) {
	ensureOnboarding()
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
	operation := "startup"
	if mode == "create" || mode == "fork" {
		operation = mode
	}
	// Authenticate before Bubble Tea takes ownership of the terminal. Background
	// collection only reports failures; the foreground owns interactive login.
	authentication, err := app.DefaultIntegrations().EnsureAuthentication(context.Background(), integration.AuthenticationRequest{Operation: operation, SourceStatuses: response.Sources})
	if err != nil {
		fatal(err)
	}
	if authentication.Changed {
		if refreshed, err := client.Call(path, "refresh-local"); err != nil {
			fatal(err)
		} else if !refreshed.OK {
			fatal(errors.New(refreshed.Error))
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
		if errors.Is(err, tui.ErrRelaunch) {
			executable, e := os.Executable()
			if e != nil {
				fatal(e)
			}
			fatal(syscall.Exec(executable, os.Args, os.Environ()))
		}
		fatal(err)
	}
}

func runActivity(args []string) {
	args = positionalArgs("activity", args, 1)
	activity, err := protocol.ParseActivity(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := app.DefaultIntegrations().PublishActivity(ctx, activity); err != nil {
		fatal(err)
	}
	// Publication remains useful without a daemon. The normal local poll can
	// recover a missed refresh; never delay an approval on stalled collection.
	if path, err := socket.Path(); err == nil {
		_, _ = client.CallWithTimeout(path, "refresh-local", time.Second)
	}
	if jsonOutput {
		printResult(map[string]any{"ok": true, "activity": activity})
	}
}

func multiplexerClientActive(integrations integration.Registry) bool {
	multiplexer, err := integrations.Multiplexer()
	return err == nil && multiplexer.ClientActive()
}

func runTask(args []string) {
	flags := outputFlags("radar task")
	flags.Usage = taskUsage
	_ = flags.Parse(args)
	args = flags.Args()
	if len(args) == 0 {
		taskUsage()
		os.Exit(2)
	}
	command, args := args[0], args[1:]
	var request protocol.Request
	switch command {
	case "create":
		flags := outputFlags("radar task create")
		title := flags.String("title", "", "task title")
		_ = parseFlags(flags, args)
		if flags.NArg() != 0 || strings.TrimSpace(*title) == "" {
			taskUsage()
			os.Exit(2)
		}
		request = protocol.Request{Method: "task-create", TaskMutation: &protocol.TaskMutation{Title: *title}}
	case "delete":
		args = positionalArgs("task delete", args, 1)
		result, err := deleteTask(parseTaskID(args[0]), os.Stdin, os.Stderr, callDaemonRequest)
		if err != nil {
			fatal(err)
		}
		if result != nil {
			printResult(result)
		}
		return
	case "done", "reopen", "mute", "unmute":
		args = positionalArgs("task "+command, args, 1)
		request = protocol.Request{Method: "task-" + command, TaskMutation: &protocol.TaskMutation{TaskID: parseTaskID(args[0])}}
	case "priority":
		args = positionalArgs("task priority", args, 2)
		if args[1] != "urgent" && args[1] != "normal" {
			taskUsage()
			os.Exit(2)
		}
		request = protocol.Request{Method: "task-priority", TaskMutation: &protocol.TaskMutation{TaskID: parseTaskID(args[0]), Priority: args[1]}}
	default:
		taskUsage()
		os.Exit(2)
	}
	response := callDaemonRequest(request)
	if response.Task == nil {
		fatal(errors.New("task mutation response was empty"))
	}
	printResult(response.Task)
}

func parseTaskID(value string) int {
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		taskUsage()
		os.Exit(2)
	}
	return id
}

func callDaemonRequest(request protocol.Request) protocol.Response {
	path, err := socket.Path()
	if err != nil {
		fatal(err)
	}
	if err := ensureDaemonCurrent(path); err != nil {
		fatal(err)
	}
	response, err := client.CallRequest(path, request)
	if err != nil {
		if startErr := startDaemonAndWait(path); startErr != nil {
			fatal(startErr)
		}
		response, err = client.CallRequest(path, request)
		if err != nil {
			fatal(err)
		}
	}
	if !response.OK {
		fatal(errors.New(response.Error))
	}
	return response
}

func runCreate(args []string) {
	flags := outputFlags("radar create")
	repo := flags.String("repo", "", "repository path")
	base := flags.String("base", "", "base branch or revision")
	name := flags.String("name", "", "workspace name")
	_ = parseFlags(flags, args)

	if flags.NArg() != 0 {
		createUsage()
		os.Exit(2)
	}
	if *repo == "" && *base == "" && *name == "" {
		if jsonOutput {
			createUsage()
			os.Exit(2)
		}
		runTUIWithMode("create")
		return
	}
	if *name == "" || (*repo == "") != (*base == "") {
		createUsage()
		os.Exit(2)
	}

	ensureOnboarding()
	integrations := app.DefaultIntegrations()
	if _, err := integrations.EnsureAuthentication(context.Background(), integration.AuthenticationRequest{Operation: "create"}); err != nil {
		fatal(err)
	}
	manager, err := integrations.WorkspaceManager()
	if err != nil {
		fatal(err)
	}
	result, err := manager.CreateWorkspace(context.Background(), integration.ManagedWorkspaceRequest{
		Repo: *repo, BranchMode: integration.WorkspaceBranchNew, Base: *base, Name: *name,
		Switch: multiplexerClientActive(integrations),
	})
	if err != nil {
		fatal(err)
	}
	printResult(result)
}

func runReconcileWorkspace(args []string) {
	flags := outputFlags("radar reconcile-workspace")
	current := flags.String("workspace", "", "path inside the current Radar workspace")
	requestJSON := flags.String("request", "", "JSON workspace reconciliation request")
	planID := flags.String("plan", "", "confirmed preview plan ID")
	planChanges := flags.Int("plan-changes", -1, "confirmed preview change count for diagnostics")
	preview := flags.Bool("preview", false, "validate and print the plan without changes")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 || strings.TrimSpace(*requestJSON) == "" {
		reconcileWorkspaceUsage()
		os.Exit(2)
	}
	var request integration.WorkspaceReconcileRequest
	decoder := json.NewDecoder(strings.NewReader(*requestJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		fatal(fmt.Errorf("invalid workspace reconciliation request: %w", err))
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		fatal(fmt.Errorf("invalid workspace reconciliation request: expected one JSON object"))
	}
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	manager, err := app.DefaultIntegrations().WorkspaceManager()
	if err != nil {
		fatal(err)
	}
	request.Workspace = *current
	request.WorkspaceRoot = manager.ExpandPath(cfg.Workspace.RootDir)
	request.ExpectedPlanID = strings.TrimSpace(*planID)
	if *planChanges >= 0 {
		request.ExpectedPlanChangeCount = planChanges
	}
	request.AdditionalSandboxMounts = cfg.SBX.AdditionalMounts
	logger, logFile, _, err := logging.New()
	if err != nil {
		fatal(err)
	}
	closeLog := func() {
		if closeErr := logFile.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "close Radar log: %v\n", closeErr)
		}
	}
	if *preview {
		plan, err := manager.PreviewReconcile(context.Background(), request)
		if err != nil {
			attributes := []any{"workspace", request.Workspace, "error", err}
			if problem, ok := manager.ReconcileErrorDetails(err); ok {
				attributes = append(attributes, "reason", problem.Reason, "path", problem.Path, "change_count", problem.ChangeCount)
			}
			logger.Error("workspace reconciliation preview failed", attributes...)
			closeLog()
			fatal(err)
		}
		plan.AutoConfirm = cfg.Workspace.AutoConfirm
		logger.Info("workspace reconciliation planned",
			"workspace_id", plan.WorkspaceID, "workspace_name", plan.WorkspaceName,
			"plan_id", plan.PlanID, "revision", plan.Revision, "change_count", len(plan.Changes), "auto_confirm", plan.AutoConfirm,
			"effective_mount_count", plan.EffectiveMountCount)
		closeLog()
		printResult(plan)
		return
	}
	result, err := manager.ApplyReconcile(context.Background(), logger, request)
	if result.Plan != nil {
		result.Plan.AutoConfirm = cfg.Workspace.AutoConfirm
	}
	closeLog()
	if err != nil {
		fatal(err)
	}
	if result.OK {
		if err := refreshLocalSourcesAfterReconcile(); err != nil {
			warning := "workspace was reconciled, but Radar could not refresh local sources immediately: " + err.Error()
			if result.Warning == "" {
				result.Warning = warning
			} else {
				result.Warning += "; " + warning
			}
		}
	}
	printResult(result)
}

func refreshLocalSourcesAfterReconcile() error {
	path, err := socket.Path()
	if err != nil {
		return err
	}
	response, err := client.Call(path, "refresh-local")
	if err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func runWorkspaceContext(args []string) {
	flags := outputFlags("radar workspace-context")
	current := flags.String("workspace", "", "path inside the current Radar workspace")
	registrationOnly := flags.Bool("registration-only", false, "only check registered workspace membership, without inspecting Git or sandbox resources")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 {
		workspaceContextUsage()
		os.Exit(2)
	}
	manager, err := app.DefaultIntegrations().WorkspaceManager()
	if err != nil {
		fatal(err)
	}
	if *registrationOnly {
		registration, found, err := manager.RegisteredWorkspace(*current)
		if err != nil {
			fatal(err)
		}
		printResult(registrationResult{Registered: found, WorkspacePath: registration.Path})
		return
	}
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	result, err := manager.InspectWorkspace(context.Background(), *current, manager.ExpandPath(cfg.Workspace.RootDir))
	if err != nil {
		fatal(err)
	}
	printResult(result)
}

func runRepositoryRefs(args []string) {
	flags := outputFlags("radar repository-refs")
	repo := flags.String("repo", "", "repository path")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 || strings.TrimSpace(*repo) == "" {
		repositoryRefsUsage()
		os.Exit(2)
	}
	manager, err := app.DefaultIntegrations().WorkspaceManager()
	if err != nil {
		fatal(err)
	}
	result, err := manager.InspectRepositoryRefs(context.Background(), *repo)
	if err != nil {
		fatal(err)
	}
	printResult(result)
}

func runFork(args []string) {
	flags := outputFlags("radar fork")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 {
		forkUsage()
		os.Exit(2)
	}
	rejectJSON("fork")
	runTUIWithMode("fork")
}

func runCleanup(args []string) {
	args = positionalArgs("cleanup", args, 1)
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
	if _, err := app.DefaultIntegrations().EnsureAuthentication(context.Background(), integration.AuthenticationRequest{Operation: "cleanup", CleanupTargets: preview.Targets}); err != nil {
		fatal(err)
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
	printResult(response.CleanupResult)
}

func runGarbageCollection(args []string) {
	positionalArgs("gc", args, 0)
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
	printResult(response.GarbageCollectionResult)
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

	store, err := state.NewStore(logger)
	if err != nil {
		logger.Error("could not initialize state", "error", err)
		fatal(err)
	}
	integrations := app.DefaultIntegrations()
	cleanupService := cleanup.New(integrations.CleanupProviders())
	notificationService := notification.New(logger)
	collectionMu := &sync.Mutex{}
	tasks := taskservice.New(store, logger, integrations)
	refresh := refresher(context.Background(), store, logger, collectionMu, integrations, cleanupService, notificationService, tasks)
	garbageCollect := garbageCollector(context.Background(), store, logger, collectionMu, integrations, cleanupService, notificationService, tasks)
	if collectionDisabled() {
		logger.Info("source collection disabled", "env", "RADAR_DISABLE_COLLECTION")
	} else {
		go refreshLoop(context.Background(), refresh)
	}

	localRefresh := localRefresher(context.Background(), collectionMu, tasks)
	if err := server.New(store, logger, func() { refresh(refreshFull, true) }, resetter(context.Background(), logger, collectionMu, tasks), garbageCollect, integrations, cleanupService).SetLocalRefresh(localRefresh).SetTaskMutation(tasks.MutateTask).SetTaskDeletion(tasks.PreviewDeleteTask, tasks.DeleteTask).ListenAndServe(path); err != nil {
		logger.Error("daemon stopped", "error", err)
		fatal(err)
	}
}

func stopDaemon() {
	release, err := operationlock.Acquire(false)
	if err != nil {
		fatal(err)
	}
	defer release()
	pids, _ := process.DaemonPIDs()
	if err := process.Stop(); err != nil {
		fatal(err)
	}
	if len(pids) == 0 {
		printResult(daemonResult{OK: true, Status: "not running", PIDs: pids})
		return
	}
	printResult(daemonResult{OK: true, Status: "stopped", PIDs: pids})
}

func restartDaemon() {
	release, err := operationlock.Acquire(false)
	if err != nil {
		fatal(err)
	}
	defer release()
	if err := restartDaemonAndWait(""); err != nil {
		fatal(err)
	}
	printResult(daemonResult{OK: true, Status: "restarted"})
}

func ensureDaemonCurrent(socketPath string) error {
	release, err := operationlock.Acquire(false)
	if err != nil {
		return err
	}
	defer release()
	if runtime.GOOS == "darwin" {
		if err := version.CheckInstalled(); err != nil {
			return err
		}
	}
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
	// Reap while this parent remains alive (notably a long-lived TUI). If the
	// parent exits first, the detached daemon is adopted by the OS.
	go func() { _, _ = process.Wait() }()
	return nil
}

type refreshScope string

const (
	refreshFull          refreshScope = "full"
	refreshLocal         refreshScope = "local"
	localRefreshInterval              = 15 * time.Second
	fullRefreshInterval               = 2 * time.Minute
)

func collectionDisabled() bool {
	return os.Getenv("RADAR_DISABLE_COLLECTION") == "1"
}

func refresher(ctx context.Context, store *state.Store, logger *slog.Logger, mu *sync.Mutex, integrations integration.Registry, cleanupService cleanup.Service, notificationService notification.Service, tasks *taskservice.Service) func(refreshScope, bool) {
	var lastFullRefresh time.Time
	var lastWorkspaceGC time.Time

	return func(scope refreshScope, force bool) {
		if collectionDisabled() {
			logger.Debug("refresh skipped; source collection disabled", "scope", scope, "force", force)
			return
		}
		mu.Lock()

		if scope == refreshFull && !force && time.Since(lastFullRefresh) < fullRefreshInterval {
			mu.Unlock()
			logger.Debug("full refresh skipped; recently refreshed")
			return
		}
		if scope == refreshFull {
			lastFullRefresh = time.Now()
		}

		logger.Debug("refresh started", "scope", scope, "force", force)
		previous := store.Tasks()
		result := tasks.Refresh(ctx, scope == refreshLocal)
		var gcNotification *protocol.GarbageCollectionResult
		if time.Since(lastWorkspaceGC) >= time.Hour {
			lastWorkspaceGC = time.Now()
			manager, managerErr := integrations.WorkspaceManager()
			if managerErr != nil {
				logger.Warn("workspace gc failed", "error", managerErr)
			} else if root, rootErr := manager.DefaultRoot(); rootErr != nil {
				logger.Warn("workspace gc failed", "error", rootErr)
			} else {
				gcResult, err := workspacegc.Run(ctx, store, cleanupService, logger, time.Now(), workspacegc.Options{WorkspaceRoot: root, GuardExecution: tasks.GuardCleanup})
				if err != nil {
					logger.Warn("workspace gc failed", "error", err)
				} else if len(gcResult.Deleted) > 0 {
					converted := garbageCollectionResult(gcResult)
					gcNotification = &converted
					result = tasks.Refresh(ctx, true)
					logger.Debug("workspace gc refresh finished", "deleted", len(gcResult.Deleted), "tasks", len(result.Tasks))
				}
			}
		}
		current := store.Tasks()
		mu.Unlock()

		if gcNotification != nil {
			notificationService.NotifyGarbageCollection(ctx, *gcNotification)
		}
		notifyActionableTransitions(ctx, previous, current, logger, integrations, notificationService)
		logger.Debug("refresh finished", "scope", scope, "tasks", len(result.Tasks), "sources", len(result.Sources))
	}
}

func localRefresher(ctx context.Context, mu *sync.Mutex, tasks *taskservice.Service) func() {
	return func() {
		mu.Lock()
		defer mu.Unlock()
		tasks.Refresh(ctx, true)
	}
}

func garbageCollector(ctx context.Context, store *state.Store, logger *slog.Logger, mu *sync.Mutex, integrations integration.Registry, cleanupService cleanup.Service, notificationService notification.Service, tasks *taskservice.Service) func() (protocol.GarbageCollectionResult, error) {
	return func() (protocol.GarbageCollectionResult, error) {
		mu.Lock()

		manager, err := integrations.WorkspaceManager()
		if err != nil {
			mu.Unlock()
			return protocol.GarbageCollectionResult{}, err
		}
		root, err := manager.DefaultRoot()
		if err != nil {
			mu.Unlock()
			return protocol.GarbageCollectionResult{}, err
		}
		result, err := workspacegc.Run(ctx, store, cleanupService, logger, time.Now(), workspacegc.Options{WorkspaceRoot: root, IgnoreRetention: true, GuardExecution: tasks.GuardCleanup})
		if err != nil {
			mu.Unlock()
			return protocol.GarbageCollectionResult{}, err
		}
		if len(result.Deleted) > 0 {
			collected := tasks.Refresh(ctx, true)
			logger.Debug("manual workspace gc refresh finished", "deleted", len(result.Deleted), "tasks", len(collected.Tasks))
		}
		converted := garbageCollectionResult(result)
		mu.Unlock()
		notificationService.NotifyGarbageCollection(ctx, converted)
		return converted, nil
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

func notifyActionableTransitions(ctx context.Context, previous, current []protocol.Task, logger *slog.Logger, integrations integration.Registry, notificationService notification.Service) {
	previous = integrations.FilterTasks(previous, logger)
	current = integrations.FilterTasks(current, logger)
	previouslyMuted := make(map[int]bool, len(previous))
	for _, task := range previous {
		previouslyMuted[task.ID] = task.Muted
	}
	eligible := make([]protocol.Task, 0, len(current))
	for _, task := range current {
		// A refresh can span an explicit unmute. It must not turn that
		// preference change into a self-notification, or notify muted work
		// whose underlying source attention remains active.
		if !task.Muted && !previouslyMuted[task.ID] {
			eligible = append(eligible, task)
		}
	}
	notificationService.NotifyTransitions(ctx, previous, eligible)
}

func resetter(ctx context.Context, logger *slog.Logger, mu *sync.Mutex, tasks *taskservice.Service) func() error {
	return func() error {
		mu.Lock()
		defer mu.Unlock()

		logger.Debug("reset started")
		if err := tasks.Reset(); err != nil {
			return err
		}
		if collectionDisabled() {
			logger.Debug("reset finished without collection; source collection disabled")
			return nil
		}
		result := tasks.Refresh(ctx, false)
		logger.Debug("reset finished", "tasks", len(result.Tasks), "sources", len(result.Sources))
		return nil
	}
}

func refreshLoop(ctx context.Context, refresh func(refreshScope, bool)) {
	refresh(refreshFull, false)
	localTicker := time.NewTicker(localRefreshInterval)
	defer localTicker.Stop()
	fullTicker := time.NewTicker(fullRefreshInterval)
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

	if jsonOutput {
		printResult(res)
	} else {
		if err := writeDaemonResult(os.Stdout, method, res); err != nil {
			fatal(err)
		}
	}
}

func printLogPath() {
	path, err := logging.Path()
	if err != nil {
		fatal(err)
	}
	printResult(pathResult{Path: path})
}

func printStatePath() {
	path, err := state.Path()
	if err != nil {
		fatal(err)
	}
	printResult(pathResult{Path: path})
}

func printConfigPath() {
	path, err := config.Path()
	if err != nil {
		fatal(err)
	}
	printResult(pathResult{Path: path})
}

func printRateLimit() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	reporter, err := app.DefaultIntegrations().RateLimitReporter()
	if err != nil {
		fatal(err)
	}
	summary, err := reporter.RateLimitSummary(context.Background(), logger)
	if err != nil {
		fatal(err)
	}
	printResult(rateLimitResult{Summary: summary})
}

func printVersion() {
	if jsonOutput {
		printResult(map[string]string{"version": version.Number, "commit": version.Commit, "built": version.Date})
	} else {
		fmt.Println(version.Text())
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: radar [--json] [command]

Interactive:
  radar                         open the terminal UI

Tasks:
  radar task create --title <title>
  radar task done <task-id>
  radar task reopen <task-id>
  radar task mute <task-id>
  radar task unmute <task-id>
  radar task delete <task-id>
  radar task priority <task-id> urgent|normal

Workspaces:
  radar create
  radar create --repo <repo> --base <branch> --name <name>
  radar reconcile-workspace --request <json> [--workspace <path>] [--preview]
  radar workspace-context [--workspace <path>]
  radar repository-refs --repo <repo>
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

Setup and maintenance:
  radar setup
  radar setup notifications  macOS: optional notifier setup/test
  radar update               macOS: review, adopt/update, or recover a release

Other:
  radar ack <task-id>
  radar log-path
  radar state-path
  radar config-path
  radar rate-limit
  radar version

Output:
  Human-readable output is the default, including when piped.
  Add --json before the command or after its arguments for machine-readable output.
  Path commands print a bare path by default. Activity publication is silent.
  Interactive UI, setup, update, fork, and daemon modes do not support --json.
  --json does not bypass confirmation for cleanup or task deletion.

Examples:
  radar tasks
  radar tasks --json | jq '.tasks // [] | .[].title'
  radar task done 42 --json
  radar workspace-context --json`)
}

func taskUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar task create --title <title>
       radar task done <task-id>
       radar task reopen <task-id>
       radar task mute <task-id>
       radar task unmute <task-id>
       radar task delete <task-id>
       radar task priority <task-id> urgent|normal

Add --json for machine-readable results. Deletion still requires confirmation.`)
}

func createUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar create
       radar create --name <name>
       radar create --repo <repo> --base <branch> --name <name>

Options:
  --repo   repository path
  --base   base branch or revision, for example origin/main
  --name   workspace name; also used to derive a sanitized branch name
  --json   print machine-readable output; requires --name`)
}

func reconcileWorkspaceUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar reconcile-workspace [--workspace <path>] --request <json> [--preview]

The request contains the revision and complete desired worktree/sandbox state. Preview and apply print readable summaries; add --json for machine-readable plans and results. --workspace defaults to the process working directory.`)
}

func workspaceContextUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar workspace-context [--workspace <path>] [--registration-only]

Print the current logical Radar workspace, its member worktrees, and discovered repositories. Add --json for the complete machine-readable state.
With --registration-only, only report registered and workspace_path without inspecting host resources.`)
}

func repositoryRefsUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar repository-refs --repo <repo>

Try to fetch and prune origin, then print branch and checkout information. Add --json for machine-readable output.
If the fetch fails, locally cached refs are returned with a warning.`)
}

func forkUsage() {
	fmt.Fprintln(os.Stderr, `usage: radar fork

Fork the current tmux workspace into a sibling workspace and fork its Pi session.`)
}

func cleanupTargetDescription(target protocol.CleanupTarget) string {
	if target.Description != "" {
		return target.Description
	}
	if target.Title != "" {
		return target.Title
	}
	return target.Source
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
	fmt.Fprintln(os.Stderr, fatalMessage(err))
	os.Exit(1)
}

func fatalMessage(err error) string {
	if !jsonOutput {
		return "radar: " + err.Error()
	}
	if manager, managerErr := app.DefaultIntegrations().WorkspaceManager(); managerErr == nil {
		if problem, ok := manager.ReconcileErrorDetails(err); ok {
			if data, marshalErr := json.Marshal(problem); marshalErr == nil {
				return string(data)
			}
		}
	}
	data, _ := json.Marshal(map[string]string{"error": err.Error()})
	return string(data)
}

func ensureOnboarding() {
	needed, err := onboarding.Needed()
	if err != nil {
		fatal(err)
	}
	if needed {
		if jsonOutput {
			fatal(errors.New("setup required; run radar setup before requesting JSON output"))
		}
		runOnboarding()
	}
}

func runOnboarding() {
	if err := onboarding.Run(); err != nil {
		if errors.Is(err, onboarding.ErrAborted) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fatal(err)
	}
	if runtime.GOOS == "darwin" {
		if err := notification.Setup(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "Notification setup deferred:", err)
		}
	}
}
