package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"radar/internal/app"
	"radar/internal/workspacelease"
)

// The helper owns the OS lock independently of the daemon. Its parent keeps
// stdin open; parent death closes the pipe and releases the lock.
func runWorkspaceLease(args []string) {
	flags := outputFlags("radar workspace-lease")
	path := flags.String("workspace", "", "registered workspace to protect")
	_ = parseFlags(flags, args)
	if *path == "" || flags.NArg() != 0 {
		fatal(errors.New("workspace-lease requires --workspace"))
	}
	manager, err := app.DefaultIntegrations().WorkspaceManager()
	if err != nil {
		fatal(err)
	}
	registered, found, err := manager.RegisteredWorkspace(*path)
	if err != nil {
		fatal(err)
	}
	if !found {
		fatal(errors.New("workspace is not registered"))
	}
	release, err := workspacelease.Acquire(registered.Path, false)
	if err != nil {
		fatal(err)
	}
	defer release()
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]bool{"ok": true})
	} else {
		fmt.Fprintln(os.Stdout, "Workspace lease acquired; close stdin to release.")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	closed := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(closed) }()
	select {
	case <-ctx.Done():
	case <-closed:
	}
}
