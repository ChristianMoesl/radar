package agentservice

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/protocol"
)

func diagnosticCollectionRegistration(t *testing.T) Registration {
	t.Helper()
	t.Setenv("RADAR_AGENT_ROOT", t.TempDir())
	reg := diagnosticRegistration(t, os.Getenv("RADAR_AGENT_ROOT"), "subscriber", "/registered/workspace")
	reg.Collection = "github"
	dir, _ := instanceDir(reg.ID)
	if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestDiagnosticCollectionCheckpointReadBoundary(t *testing.T) {
	valid := `{"version":1,"source":"github","seen":{},"pending":[{"source":"private-source","id":"private-event","payload":{"body":"private-body"}}],"problem":"private-error"}`
	atBound := valid + strings.Repeat(" ", protocol.AgentMaxFrameBytes-len(valid))
	for _, test := range []struct {
		name, content string
		available     bool
	}{
		{"missing", "", true},
		{"defaults", `{}`, true},
		{"native_null", `null`, true},
		{"zero_false", `{"version":1,"source":"github","seen":{},"pending":[],"problem":""}`, true},
		{"valid_private", valid, true},
		{"exact_bound", atBound, true},
		{"private_extension", `{"version":1,"source":"github","seen":{},"private":{"body":"private-body"}}`, true},
		{"empty", "", false},
		{"malformed", `{"private":"private-body"`, false},
		{"version", `{"version":99,"source":"github","seen":{}}`, false},
		{"source", `{"version":1,"source":"other","seen":{}}`, false},
		{"seen_null", `{"version":1,"source":"github","seen":null}`, false},
		{"pending_type", `{"version":1,"source":"github","pending":{}}`, false},
		{"problem_type", `{"version":1,"source":"github","problem":{}}`, false},
		{"seen_type", `{"version":1,"source":"github","seen":{"private-key":{"sequence":-1}}}`, false},
		{"trailing", valid + `{}`, false},
		{"over_bound", atBound + " ", false},
		{"oversized", valid + strings.Repeat(" ", 2*protocol.AgentMaxFrameBytes), false},
		{"directory", "", false},
		{"symlink", valid, false},
		{"dangling_symlink", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reg := diagnosticCollectionRegistration(t)
			path := collectionPath(reg)
			var err error
			switch test.name {
			case "missing":
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				target := filepath.Join(t.TempDir(), "private-checkpoint")
				if err := os.WriteFile(target, []byte(test.content), 0600); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(target, path)
			case "dangling_symlink":
				err = os.Symlink(filepath.Join(t.TempDir(), "absent"), path)
			default:
				err = os.WriteFile(path, []byte(test.content), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(path)
			items, err := DiagnosticAgents("", reg.ID)
			if err != nil || len(items) != 1 {
				t.Fatal("checkpoint refusal lost registration")
			}
			view := items[0]
			if view.OwnershipHold != "registered_owner_even_when_stopped" || view.RunnerEvidence != "unknown_not_probed" || view.LeaseEvidence != "unknown_not_inspected" || view.Desired {
				t.Fatal("checkpoint inspection changed ownership or fabricated runtime evidence")
			}
			if test.available {
				native, nativeErr := loadCollection(reg)
				if nativeErr != nil || view.CollectionEvidence != "fresh_checkpoint" || view.Collection == nil || view.Collection.Source != reg.Collection || view.Collection.Pending != len(native.Pending) || view.Collection.DeliveryProblem != (native.Problem != "") {
					t.Fatal("bounded projection changed native defaults or valid counters")
				}
			} else if view.CollectionEvidence != "unavailable" || view.Collection != nil {
				t.Fatal("unsafe or invalid checkpoint became usable collection evidence")
			}
			encoded, _ := json.Marshal(view)
			if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "payload") {
				t.Fatal("private collection body or error exposed")
			}
			after, _ := os.Lstat(path)
			if (before == nil) != (after == nil) || (before != nil && (!os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()))) {
				t.Fatal("inspection created or changed checkpoint")
			}
			if before != nil && before.Mode().IsRegular() {
				data, _ := os.ReadFile(path)
				if !bytes.Equal(data, []byte(test.content)) {
					t.Fatal("inspection rewrote collection checkpoint")
				}
			}
			if test.name == "over_bound" || test.name == "oversized" || test.name == "symlink" {
				// This bound belongs only to diagnostic observation. Do not change
				// production outbox loading, persistence, admission or retry rules.
				native, err := loadCollection(reg)
				if err != nil || len(native.Pending) != 1 || native.Problem != "private-error" {
					t.Fatal("diagnostic bound changed native collection loader policy")
				}
			}
		})
	}
}

func TestDiagnosticCollectionDoesNotWaitForFIFO(t *testing.T) {
	const child = "RADAR_TEST_DIAGNOSTIC_COLLECTION_FIFO_CHILD"
	if os.Getenv(child) == "1" {
		items, err := DiagnosticAgents("", "subscriber")
		if err != nil || len(items) != 1 || items[0].CollectionEvidence != "unavailable" || items[0].Collection != nil {
			t.Fatal("FIFO became usable checkpoint evidence")
		}
		return
	}
	reg := diagnosticCollectionRegistration(t)
	if err := syscall.Mkfifo(collectionPath(reg), 0600); err != nil {
		t.Fatal(err)
	}
	// Isolate the unchanged unsafe implementation so a regression cannot hang
	// the suite. The child is killed and collected before temporary cleanup.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDiagnosticCollectionDoesNotWaitForFIFO$", "-test.count=1")
	cmd.Env = append(os.Environ(), child+"=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			t.Fatal("diagnostic collection read waited for FIFO writer")
		}
		t.Fatalf("isolated FIFO refusal failed: %s", output)
	}
	info, err := os.Lstat(collectionPath(reg))
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("inspection changed FIFO")
	}
}

func TestDiagnosticCollectionScopeAndConfigurationPrecedeCheckpointReads(t *testing.T) {
	reg := diagnosticCollectionRegistration(t)
	if err := atomicJSON(collectionPath(reg), collectionState{Version: 1, Source: reg.Collection, Seen: map[string]collectionRevision{}}); err != nil {
		t.Fatal(err)
	}
	other := diagnosticRegistration(t, os.Getenv("RADAR_AGENT_ROOT"), "unrelated", "/registered/workspace-other")
	other.Collection = "github"
	dir, _ := instanceDir(other.ID)
	if err := atomicJSON(filepath.Join(dir, "registration.json"), other); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(collectionPath(other), 0600); err != nil {
		t.Fatal(err)
	}
	for name, selectors := range map[string][2]string{
		"exact_id": {"", reg.ID}, "workspace": {reg.Workspace.Path, ""},
		"unknown_id": {"", "absent"}, "outside_scope": {reg.Workspace.Path, other.ID},
	} {
		t.Run(name, func(t *testing.T) {
			items, err := DiagnosticAgents(selectors[0], selectors[1])
			if err != nil || len(items) > 1 || ((name == "exact_id" || name == "workspace") && (len(items) != 1 || items[0].CollectionEvidence != "fresh_checkpoint")) || ((name == "unknown_id" || name == "outside_scope") && len(items) != 0) {
				t.Fatal("unselected checkpoint read or lexical scope changed")
			}
		})
	}
	reg.Collection = ""
	dir, _ = instanceDir(reg.ID)
	if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(collectionPath(reg)); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(collectionPath(reg), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := DiagnosticAgents("", reg.ID)
	if err != nil || len(items) != 1 || items[0].CollectionEvidence != "not_configured" || items[0].Collection != nil {
		t.Fatal("unconfigured collection inspected checkpoint")
	}
}
