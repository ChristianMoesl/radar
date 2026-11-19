package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/workspacelease"
)

func TestWorkspaceWriterReconcileAndUnrelatedLiveActivationKeepIndependentOwnership(t *testing.T) {
	for _, first := range []string{"reconcile", "activation"} {
		t.Run(first+"-first", func(t *testing.T) {
			root, group := activationFixture(t)
			other := group
			other.Path = filepath.Join(root, "unrelated")
			other.ID, other.Name, other.SessionName = workspacegroup.ID(other.Path), "Unrelated", "unrelated"
			other.TaskLinkingKey, other.NoteLinkingKey, other.NotePath = "", "", ""
			other.Members = []workspacegroup.Member{{Path: filepath.Join(other.Path, "member"), Repository: t.TempDir(), Branch: "fixture"}}
			if err := os.MkdirAll(other.Members[0].Path, 0755); err != nil {
				t.Fatal(err)
			}
			saveRecreationGroups(t, root, []workspacegroup.Workspace{group, other})
			_, other, _, _ = RegisteredWorkspace(other.Path, root)
			enteredWriter, resumeWriter := make(chan struct{}), make(chan struct{})
			enteredOpen, resumeOpen := make(chan struct{}), make(chan struct{})
			var writerOnce, openOnce sync.Once
			unblockWriter := func() { writerOnce.Do(func() { close(resumeWriter) }) }
			unblockOpen := func() { openOnce.Do(func() { close(resumeOpen) }) }
			baseWriter, baseOpen := &fakeRunner{}, &fakeRunner{hasSession: true}
			writer := activationRunner{Runner: baseWriter, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				if name == "tmux" && args[0] == "has-session" {
					close(enteredWriter)
					<-resumeWriter
				}
				return baseWriter.Run(ctx, cwd, name, args...)
			}}
			opener := activationRunner{Runner: baseOpen, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				if name == "tmux" && args[0] == "switch-client" {
					close(enteredOpen)
					<-resumeOpen
				}
				return baseOpen.Run(ctx, cwd, name, args...)
			}}
			writerResult, openResult := make(chan error, 1), make(chan error, 1)
			writerDone, openDone := make(chan struct{}), make(chan struct{})
			request := writerRequest(t, root, group)
			go func() {
				result, err := ApplyReconcileWorkspace(context.Background(), writer, nil, request)
				if err == nil && !result.OK {
					err = os.ErrInvalid
				}
				writerResult <- err
				close(writerDone)
			}()
			defer func() { unblockWriter(); <-writerDone }()
			select {
			case <-enteredWriter:
			case err := <-writerResult:
				t.Fatal("writer did not reach provider", err)
			case <-time.After(5 * time.Second):
				t.Fatal("writer did not start")
			}
			for _, live := range []bool{false, true} {
				conflict := &fakeRunner{hasSession: live}
				if _, err := OpenRegisteredWorkspace(context.Background(), conflict, group.Path, true); err == nil || !strings.Contains(err.Error(), "mutation is in use") || len(conflict.calls) != 0 {
					t.Error("activation bypassed active writer", err, conflict.calls)
				}
			}
			go func() {
				_, err := OpenRegisteredWorkspace(context.Background(), opener, other.Path, true)
				openResult <- err
				close(openDone)
			}()
			defer func() { unblockOpen(); <-openDone }()
			select {
			case <-enteredOpen:
			case err := <-openResult:
				t.Fatal("unrelated navigation returned early", err)
			case <-time.After(5 * time.Second):
				t.Fatal("unrelated live navigation waited for writer's note lock")
			}
			if first == "reconcile" {
				unblockWriter()
				if err := <-writerResult; err != nil {
					t.Fatal(err)
				}
				if release, err := workspacelease.AcquireMutation(other.Path); err == nil {
					release()
					t.Error("writer completion lost active navigation ownership")
				}
				unblockOpen()
				if err := <-openResult; err != nil {
					t.Fatal(err)
				}
			} else {
				unblockOpen()
				if err := <-openResult; err != nil {
					t.Fatal(err)
				}
				if release, err := workspacelease.AcquireMutation(group.Path); err == nil {
					release()
					t.Error("navigation completion lost active writer ownership")
				}
				unblockWriter()
				if err := <-writerResult; err != nil {
					t.Fatal(err)
				}
			}
			assertActivationLeasesReleased(t, group)
			assertActivationLeasesReleased(t, other)
		})
	}
}
