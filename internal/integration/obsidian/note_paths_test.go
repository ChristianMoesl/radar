package obsidian

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/taskdb"
	"radar/internal/workspacelease"
)

type notePathCleanup struct {
	root            string
	holdNote        bool
	entered, resume chan struct{}
}

func (notePathCleanup) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "fixture"}
}
func (notePathCleanup) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return nil, nil
}
func (p notePathCleanup) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	pause := func() error {
		close(p.entered)
		select {
		case <-p.resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var err error
	if p.holdNote {
		err = workspacegroup.WithNoteLock(p.root, pause)
	} else {
		err = pause()
	}
	return req.Target, err
}

// Creation runs while the fixture holds the real note transaction. The other
// cases qualify resource admission before narrowing that transaction; they do
// not claim an existing note-serialized lifecycle operation bypasses its mutex.
func TestNotePathsRejectCleanupBeforeFilesystemOrAssociationEffects(t *testing.T) {
	for _, kind := range []string{"create", "create-root", "ensure", "adopt", "archive-source", "archive-destination", "restore-source", "restore-destination", "delete-source", "delete-destination", "reassociate", "collect-root", "status-root"} {
		for _, alias := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/physical", true: "/alias"}[alias], func(t *testing.T) {
				vault := testVault(t)
				source := NewSourceAt(vault)
				ctx := context.Background()
				t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
				root, err := workspacegroup.DefaultRoot()
				if err != nil {
					t.Fatal(err)
				}
				target := taskRoot(vault)
				var original, expectedAssociation string
				var taskID string
				var action func() error
				switch kind {
				case "create", "create-root":
					if kind == "create" {
						if err := os.Mkdir(taskRoot(vault), 0700); err != nil {
							t.Fatal(err)
						}
					}
					action = func() error { _, err := source.Create(ctx, "New note"); return err }
				case "ensure":
					desired, err := source.PrepareWorkspaceNote(ctx, "New note")
					if err != nil {
						t.Fatal(err)
					}
					target = filepath.Dir(desired.Path)
					taskID = strings.TrimPrefix(desired.LinkingKey, "obsidian:task:")
					action = func() error { return source.EnsureWorkspaceNote(ctx, desired) }
				case "adopt":
					if err := os.Mkdir(taskRoot(vault), 0700); err != nil {
						t.Fatal(err)
					}
					action = func() error { _, err := source.SetMuted(ctx, muteTask("New note"), true); return err }
				case "collect-root":
					action = func() error {
						r := source.Collect(ctx, integration.CollectRequest{})
						if !r.Complete && r.SourceStatus != nil {
							return errors.New(r.SourceStatus.Detail)
						}
						return nil
					}
				case "status-root":
					action = func() error {
						r := source.Status(ctx, nil)
						if r.Status.Status == "error" {
							return errors.New(r.Status.Detail)
						}
						return nil
					}
				default:
					if _, err := source.Create(ctx, "Existing note"); err != nil {
						t.Fatal(err)
					}
					ref := collectedArchiveRef(t, source)
					original = ref.Metadata["note_path"]
					taskID = strings.TrimPrefix(ref.ID, "obsidian:task:")
					expectedAssociation = original
					target = filepath.Dir(original)
					switch kind {
					case "archive-source", "archive-destination":
						if kind == "archive-destination" {
							target = filepath.Join(taskRoot(vault), "Archived")
						}
						action = func() error { _, err := source.SetLifecycle(ctx, ref, "done", nil); return err }
					case "restore-source", "restore-destination":
						destination := filepath.Dir(original)
						if _, err := source.SetLifecycle(ctx, ref, "done", nil); err != nil {
							t.Fatal(err)
						}
						ref = collectedArchiveRef(t, source)
						original = ref.Metadata["note_path"]
						expectedAssociation = original
						target = filepath.Dir(original)
						if kind == "restore-destination" {
							target = destination
						}
						action = func() error { _, err := source.SetLifecycle(ctx, ref, "open", nil); return err }
					case "delete-source", "delete-destination":
						preview, err := source.PreviewDelete(ctx, ref)
						if err != nil {
							t.Fatal(err)
						}
						if kind == "delete-destination" {
							target = preview.TrashDirectory
						}
						action = func() error { _, err := source.Delete(ctx, ref, preview); return err }
					case "reassociate":
						moved := filepath.Join(taskRoot(vault), "Moved--"+shortID(taskID), "Moved.md")
						if err := os.Mkdir(filepath.Dir(moved), 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(original, moved); err != nil {
							t.Fatal(err)
						}
						original = moved
						target = filepath.Dir(moved)
						action = func() error { _, err := source.ReassociateNote(ctx, ref, moved); return err }
					}
				}
				if alias {
					link := filepath.Join(t.TempDir(), "vault-alias")
					if err := os.Symlink(vault, link); err != nil {
						t.Fatal(err)
					}
					relative, err := filepath.Rel(vault, target)
					if err != nil {
						t.Fatal(err)
					}
					target = filepath.Join(link, relative)
				}
				var before []byte
				if original != "" {
					before, err = os.ReadFile(original)
					if err != nil {
						t.Fatal(err)
					}
				}
				_, targetErr := os.Lstat(target)
				targetWasMissing := os.IsNotExist(targetErr)
				entered, resume := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(resume) }) }
				done := make(chan error, 1)
				service := cleanup.New([]integration.CleanupProvider{notePathCleanup{root: root, holdNote: strings.HasPrefix(kind, "create"), entered: entered, resume: resume}})
				go func() {
					defer close(done)
					_, err := service.Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "workspace", Path: target}}}, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
					done <- err
				}()
				defer func() {
					unblock()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				select {
				case <-entered:
				case err := <-done:
					t.Fatal("cleanup failed admission", err)
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not enter fixture")
				}
				result := make(chan error, 1)
				go func() { result <- action() }()
				var actionErr error
				select {
				case actionErr = <-result:
				case <-time.After(5 * time.Second):
					unblock()
					actionErr = <-result
					t.Error("operation waited on global note transaction rather than rejecting path conflict")
				}
				if actionErr == nil || !strings.Contains(actionErr.Error(), "in use") {
					t.Error("missing resource conflict", actionErr)
				}
				if targetWasMissing {
					if _, err := os.Lstat(target); !os.IsNotExist(err) {
						t.Error("conflicting preparation created a destination directory", err)
					}
				}
				if original != "" {
					if after, err := os.ReadFile(original); err != nil || string(after) != string(before) {
						t.Error("conflicting operation moved or changed source content", err)
					}
					if release, err := workspacelease.Acquire(original, true); err != nil {
						t.Error("failed operation leaked partial source admission", err)
					} else {
						release()
					}
				}
				if taskID != "" {
					actual, err := taskdb.Find(taskID)
					if expectedAssociation == "" {
						if !errors.Is(err, sql.ErrNoRows) {
							t.Error("conflicting creation did not leave authority absent", actual.Path, err)
						}
					} else {
						wantState := "open"
						if strings.HasPrefix(kind, "archive-") || strings.HasPrefix(kind, "restore-") {
							// Done commits before archive is attempted; a restore
							// conflict leaves the existing Done state unchanged.
							wantState = "done"
						}
						if err != nil || actual.Path != expectedAssociation || actual.State != wantState || actual.Trashed {
							t.Error("conflict lost association or honest lifecycle outcome", actual, err)
						}
					}
				}
				assertNoNoteMoveIntents(t)
				// The fixture performs no deletion and never changes a registry.
				if strings.HasSuffix(kind, "root") || kind == "create" || kind == "adopt" {
					tasks, err := taskdb.List(vault)
					if err != nil {
						t.Fatal(err)
					}
					if len(tasks) != 0 {
						t.Errorf("conflicting %s published %d task records", kind, len(tasks))
					}
				}
			})
		}
	}
}

func assertNoNoteMoveIntents(t *testing.T) {
	t.Helper()
	db, err := taskdb.Current()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM file_moves").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unexpected pending move intents: %d, %v", count, err)
	}
}

// These positive cases intentionally do not hold the note mutex: lifecycle
// operations still retain that transaction. Creation separately proves it can
// proceed while an unrelated cleanup provider holds the real note transaction.
func TestNotePathsAllowUnrelatedCleanupAndReadOnlyExistingRoot(t *testing.T) {
	for _, kind := range []string{"create", "ensure", "adopt", "archive", "restore", "delete", "reassociate", "collect", "status"} {
		t.Run(kind, func(t *testing.T) {
			vault := testVault(t)
			source := NewSourceAt(vault)
			ctx := context.Background()
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			root, err := workspacegroup.DefaultRoot()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(taskRoot(vault), 0700); err != nil {
				t.Fatal(err)
			}
			var action func() error
			var path, id string
			switch kind {
			case "create":
				action = func() error { _, err := source.Create(ctx, "Independent"); return err }
			case "ensure":
				desired, err := source.PrepareWorkspaceNote(ctx, "Independent")
				if err != nil {
					t.Fatal(err)
				}
				// Workspace creation already holds compatible reference leases.
				release, err := workspacelease.AcquireReferences(ctx, desired.Path)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				action = func() error { return source.EnsureWorkspaceNote(ctx, desired) }
			case "adopt":
				action = func() error { _, err := source.SetMuted(ctx, muteTask("Independent"), true); return err }
			case "collect":
				action = func() error {
					r := source.Collect(ctx, integration.CollectRequest{})
					if !r.Complete {
						return errors.New("existing-root collection failed")
					}
					return nil
				}
			case "status":
				action = func() error {
					if r := source.Status(ctx, nil); r.Status.Status == "error" {
						return errors.New(r.Status.Detail)
					}
					return nil
				}
			default:
				if _, err := source.Create(ctx, "Independent"); err != nil {
					t.Fatal(err)
				}
				ref := collectedArchiveRef(t, source)
				path, id = ref.Metadata["note_path"], strings.TrimPrefix(ref.ID, "obsidian:task:")
				switch kind {
				case "archive":
					action = func() error { _, err := source.SetLifecycle(ctx, ref, "done", nil); return err }
				case "restore":
					if _, err := source.SetLifecycle(ctx, ref, "done", nil); err != nil {
						t.Fatal(err)
					}
					ref = collectedArchiveRef(t, source)
					action = func() error { _, err := source.SetLifecycle(ctx, ref, "open", nil); return err }
				case "delete":
					preview, err := source.PreviewDelete(ctx, ref)
					if err != nil {
						t.Fatal(err)
					}
					action = func() error { _, err := source.Delete(ctx, ref, preview); return err }
				case "reassociate":
					moved := filepath.Join(taskRoot(vault), "Moved--"+shortID(id), "Moved.md")
					if err := os.Mkdir(filepath.Dir(moved), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(path, moved); err != nil {
						t.Fatal(err)
					}
					path = moved
					action = func() error { _, err := source.ReassociateNote(ctx, ref, moved); return err }
				}
			}
			// Existing-root reads need no writer admission, even for this root.
			target := filepath.Join(vault, "unrelated")
			if kind == "collect" || kind == "status" {
				target = taskRoot(vault)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			service := cleanup.New([]integration.CleanupProvider{notePathCleanup{root: root, holdNote: kind == "create", entered: entered, resume: resume}})
			go func() {
				_, err := service.Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "workspace", Path: target}}}, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
				done <- err
			}()
			var once sync.Once
			unblock := func() { once.Do(func() { close(resume) }) }
			defer func() {
				unblock()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not enter fixture")
			}
			result := make(chan error, 1)
			go func() { result <- action() }()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				unblock()
				<-result
				t.Fatal("unrelated operation waited for cleanup")
			}
			assertNoNoteMoveIntents(t)
			if id != "" {
				task, err := taskdb.Find(id)
				if err != nil || task.Trashed != (kind == "delete") {
					t.Fatalf("lost task authority: %+v, %v", task, err)
				}
				if kind == "restore" || kind == "reassociate" {
					if task.Path != path {
						t.Fatalf("association = %s, want %s", task.Path, path)
					}
				}
			}
		})
	}
}

func TestNotePathsCreationCancellationAndDurableAssociationRetry(t *testing.T) {
	source := NewSourceAt(testVault(t))
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Create(ctx, "Cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled creation was accepted", err)
	}
	if _, err := os.Lstat(taskRoot(source.vaultPath)); !os.IsNotExist(err) {
		t.Fatal("cancelled creation prepared a missing root", err)
	}
	desired, err := source.PrepareWorkspaceNote(context.Background(), "Retry")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a stop after authority publication, before content creation.
	current := newTaskNote(desired)
	if err := taskdb.Put(&current.Task); err != nil {
		t.Fatal(err)
	}
	if err := source.EnsureWorkspaceNote(ctx, desired); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled retry was accepted", err)
	}
	if _, err := os.Lstat(filepath.Dir(desired.Path)); !os.IsNotExist(err) {
		t.Fatal("cancelled retry prepared private content", err)
	}
	if release, err := workspacelease.Acquire(desired.Path, true); err != nil {
		t.Fatal("cancellation leaked admission", err)
	} else {
		release()
	}
	if err := source.EnsureWorkspaceNote(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	after, err := taskdb.Find(current.ID)
	if err != nil || after.ID != current.ID || after.Path != current.Path || after.CreatedAt != current.CreatedAt || after.Revision != current.Revision {
		t.Fatalf("retry replaced durable identity: %+v, %v", after, err)
	}
}
