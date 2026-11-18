package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
)

func TestForegroundLeaseRetainedUntilCallbackOnEveryProcessOutcome(t *testing.T) {
	for _, outcome := range []string{"detach", "process-failure", "start-failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			r := admissionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attachment, err := prepareAttach(ctx, r, integration.SessionTarget{Name: "workspace-alph"})
			if err != nil {
				t.Fatal(err)
			}
			defer attachment.Release()
			if got := strings.Join(attachment.Command.Args, " "); got != "tmux attach-session -t $7" {
				t.Fatal("foreground target not pinned", got)
			}
			attachment.Command = exec.CommandContext(ctx, "sh", "-c", "exit 0")
			switch outcome {
			case "process-failure":
				attachment.Command = exec.CommandContext(ctx, "sh", "-c", "exit 7")
			case "start-failure":
				attachment.Command = exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-executable"))
			case "cancelled":
				ready := filepath.Join(t.TempDir(), "started")
				attachment.Command = exec.CommandContext(ctx, "sh", "-c", "printf ready > "+shellQuote(ready)+"; exec sleep 120")
				finished := make(chan error, 1)
				go func() { finished <- attachment.Run() }()
				for deadline := time.Now().Add(3 * time.Second); ; {
					if _, err := os.Stat(ready); err == nil {
						break
					}
					if time.Now().After(deadline) {
						cancel()
						<-finished
						t.Fatal("fixture foreground did not start")
					}
					time.Sleep(time.Millisecond)
				}
				cancel()
				err = <-finished
			}
			if outcome != "cancelled" {
				err = attachment.Run()
			}
			if (err != nil) != (outcome != "detach") {
				t.Fatal("unexpected native outcome", err)
			}
			// Even after Run returns, only the foreground completion callback owns
			// release. Keep the bundle unavailable until that callback is invoked.
			probe := sessionlease.New(true)
			if _, err := probe.Admit(context.Background(), r, sessionlease.Request{Selector: "$7"}); err == nil {
				probe.Close()
				t.Fatal("process return prematurely released callback-owned admission")
			}
			attachment.Release()
			attachment.Release()
			probe = sessionlease.New(true)
			defer probe.Close()
			if _, err := probe.Admit(context.Background(), r, sessionlease.Request{Selector: "$7"}); err != nil {
				t.Fatal("foreground callback leaked admission", err)
			}
		})
	}
}

func TestForegroundRevalidatesImmediatelyBeforeNativeStart(t *testing.T) {
	for _, change := range []string{"server-restart", "session-replacement", "missing", "pre-cancelled"} {
		t.Run(change, func(t *testing.T) {
			r := admissionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attachment, err := prepareAttach(ctx, r, integration.SessionTarget{Name: "workspace-alpha"})
			if err != nil {
				t.Fatal(err)
			}
			defer attachment.Release()
			attachment.Command = exec.Command("sh", "-c", "exit 0")
			id := r.sessions["$7"]
			switch change {
			case "server-restart":
				id.ServerPID = "99"
				r.sessions["$7"] = id
			case "session-replacement":
				id.Created = "35"
				r.sessions["$7"] = id
			case "missing":
				delete(r.sessions, "$7")
			case "pre-cancelled":
				cancel()
			}
			if err := attachment.Run(); err == nil || attachment.Command.ProcessState != nil {
				t.Fatal("stale foreground command executed", err)
			}
			attachment.Release()
		})
	}
}
