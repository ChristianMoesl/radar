package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type agentPlanningNoteAuthor struct {
	integration.WorkspaceNoteAuthor
	t         *testing.T
	root      string
	checkLock bool
	prepared  int
	validated []DesiredWorkspaceNote
	ensured   []DesiredWorkspaceNote
	binding   protocol.AgentWorkspaceBinding
}

func (a *agentPlanningNoteAuthor) assertNoteLock() {
	a.t.Helper()
	if !a.checkLock {
		return
	}
	file, err := os.OpenFile(filepath.Join(a.root, ".radar-notes.lock"), os.O_RDWR, 0)
	if err != nil {
		a.t.Error("native planning/acceptance did not hold the note lock", err)
		return
	}
	defer file.Close()
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		a.t.Error("native planning/acceptance did not hold the note lock")
	} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		a.t.Error("could not verify the native note lock", err)
	}
}

func (a *agentPlanningNoteAuthor) PrepareWorkspaceNote(ctx context.Context, title string) (DesiredWorkspaceNote, error) {
	a.assertNoteLock()
	a.prepared++
	return a.WorkspaceNoteAuthor.PrepareWorkspaceNote(ctx, title)
}

func (a *agentPlanningNoteAuthor) ValidateWorkspaceNote(ctx context.Context, note DesiredWorkspaceNote) error {
	a.assertNoteLock()
	a.validated = append(a.validated, note)
	return a.WorkspaceNoteAuthor.ValidateWorkspaceNote(ctx, note)
}

func (a *agentPlanningNoteAuthor) EnsureWorkspaceNote(ctx context.Context, note DesiredWorkspaceNote) error {
	a.assertNoteLock()
	a.ensured = append(a.ensured, note)
	if a.binding.Path != "" {
		if release, err := workspacelease.Acquire(a.binding.Path, true); err == nil {
			release()
			a.t.Error("native application lost its anchor bundle lease")
		}
		if release, err := workspacelease.AcquireSandbox(a.binding.Sandbox, true); err == nil {
			release()
			a.t.Error("native application lost its sandbox bundle lease")
		}
	}
	return a.WorkspaceNoteAuthor.EnsureWorkspaceNote(ctx, note)
}

func TestAgentWorkspaceUsesOneLockedNativePlan(t *testing.T) {
	for _, mode := range []string{"automatic", "accepted", "denied", "stale", "preview"} {
		t.Run(mode, func(t *testing.T) {
			options, request, runner := agentWorkspaceFixture(t)
			author := &agentPlanningNoteAuthor{WorkspaceNoteAuthor: options.NoteAuthor, t: t, root: options.WorkspaceRoot}
			options.NoteAuthor = author
			request.Preview = true
			binding, preview, err := prepareAgentWorkspace(context.Background(), runner, options, request, false)
			if err != nil {
				t.Fatal(err)
			}
			if author.prepared != 1 || len(author.validated) != 1 || len(author.ensured) != 0 {
				t.Fatal("preview did not use exactly one read-only native plan", author.prepared, len(author.validated), len(author.ensured))
			}
			if _, err := os.Stat(request.DataDir); !os.IsNotExist(err) {
				t.Fatal("preview persisted ownership intent", err)
			}
			author.prepared, author.validated = 0, nil
			author.checkLock, author.binding = mode != "preview", binding
			request.Preview = mode == "preview"
			request.Note, err = json.Marshal(preview.Note)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "accepted" {
				request.PlanID = preview.PlanID
			} else if mode == "stale" {
				request.PlanID = "stale"
			}
			actual, applied, err := prepareAgentWorkspace(context.Background(), runner, options, request, mode == "automatic")
			if mode == "denied" || mode == "stale" {
				want := "confirmation required"
				if mode == "stale" {
					want = "plan changed"
				}
				if err == nil || !strings.Contains(err.Error(), want) || actual != (protocol.AgentWorkspaceBinding{}) {
					t.Fatal("agent acceptance was bypassed", actual, err)
				}
				if _, err := os.Stat(request.DataDir); !os.IsNotExist(err) {
					t.Fatal("rejected preparation persisted ownership", err)
				}
			} else if err != nil || actual != binding || applied.PlanID != preview.PlanID || *applied.Note != *preview.Note {
				t.Fatal("native plan or binding changed during application", actual, applied, err)
			}
			if author.prepared != 0 || len(author.validated) != 1 || author.validated[0] != *preview.Note {
				t.Fatal("accepted native plan was discarded and replanned", author.prepared, author.validated)
			}
			if mode == "automatic" || mode == "accepted" {
				if len(author.ensured) != 1 || author.ensured[0] != author.validated[0] {
					t.Fatal("executor did not use the accepted full native note", author.ensured)
				}
			} else if len(author.ensured) != 0 || countAgentCalls(runner, "create") != 0 {
				t.Fatal("preview/rejection entered native resource effects")
			}
		})
	}
}

func TestAgentWorkspaceSinglePlanPreservesBundleConflicts(t *testing.T) {
	for _, resource := range []string{"anchor", "sandbox"} {
		t.Run(resource, func(t *testing.T) {
			options, request, runner := agentWorkspaceFixture(t)
			request.Preview = true
			binding, preview, err := prepareAgentWorkspace(context.Background(), runner, options, request, false)
			if err != nil {
				t.Fatal(err)
			}
			request.Preview, request.PlanID = false, preview.PlanID
			request.Note, err = json.Marshal(preview.Note)
			if err != nil {
				t.Fatal(err)
			}
			var release func()
			if resource == "anchor" {
				release, err = workspacelease.Acquire(binding.Path, true)
			} else {
				release, err = workspacelease.AcquireSandbox(binding.Sandbox, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = prepareAgentWorkspace(context.Background(), runner, options, request, false)
			release()
			if err == nil || !strings.Contains(err.Error(), "in use") {
				t.Fatal("agent preparation bypassed native bundle admission", err)
			}
			if countAgentCalls(runner, "create") != 0 || countAgentCalls(runner, "exec") != 0 {
				t.Fatal("blocked preparation entered runtime effects")
			}
			if _, err := os.Lstat(binding.Path); !os.IsNotExist(err) {
				t.Fatal("blocked preparation created its anchor", err)
			}
			// Retaining an intent is not admission or proof of recovery. Check
			// only that the failed native admission leaked no bundle leases.
			release, err = workspacelease.AcquireMutation(binding.Path)
			if err != nil {
				t.Fatal("failed admission leaked its anchor lease", err)
			}
			release()
			release, err = workspacelease.AcquireSandboxMutation(binding.Sandbox)
			if err != nil {
				t.Fatal("failed admission leaked its sandbox lease", err)
			}
			release()
		})
	}
}
