package openurl

import (
	"context"
	"os/exec"
	"runtime"
)

func Open(ctx context.Context, url string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	return exec.CommandContext(ctx, command, url).Start()
}
