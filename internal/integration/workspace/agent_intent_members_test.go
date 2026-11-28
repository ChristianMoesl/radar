package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func agentIntentMemberCases(data []byte) map[string][]byte {
	var root map[string]json.RawMessage
	_ = json.Unmarshal(data, &root)
	objects := map[string]map[string]json.RawMessage{"intent": root}
	for _, name := range []string{"binding", "note"} {
		object := map[string]json.RawMessage{}
		_ = json.Unmarshal(root[name], &object)
		objects[name] = object
	}
	cases := map[string][]byte{}
	for boundary, object := range objects {
		raw := data
		if boundary != "intent" {
			raw = root[boundary]
		}
		for field, value := range object {
			marker := `"` + field + `":`
			for kind, replacement := range map[string]string{
				"duplicate":         marker + string(value) + "," + marker,
				"escaped_duplicate": fmt.Sprintf(`"\u%04x%s":`, field[0], field[1:]) + string(value) + "," + marker,
				"folded":            `"` + strings.ToUpper(field) + `":`,
			} {
				changed := strings.Replace(string(raw), marker, replacement, 1)
				if boundary != "intent" {
					changed = strings.Replace(string(data), string(root[boundary]), changed, 1)
				}
				cases[boundary+"_"+field+"_"+kind] = []byte(changed)
			}
		}
	}
	return cases
}

func TestAgentWorkspaceIntentMembersRejectAmbiguousOwnershipAndPreview(t *testing.T) {
	options, request, runner := agentWorkspaceFixture(t)
	binding, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(request.DataDir, "workspace.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var intent agentWorkspaceIntent
	_ = json.Unmarshal(data, &intent)
	// Compact the native serialization so each exact member marker is unique.
	data, _ = json.Marshal(intent)
	request.Path, request.Expected, request.Preview = binding.Path, &binding, true
	registry, _ := os.ReadFile(workspacegroup.Path(options.WorkspaceRoot))
	for name, ambiguous := range agentIntentMemberCases(data) {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(file, ambiguous, 0600); err != nil {
				t.Fatal(err)
			}
			if note, err := AgentWorkspaceNote(request.ID, request.DataDir, binding); err == nil || note != nil || strings.Contains(err.Error(), "private-") {
				t.Error("ambiguous retained intent supplied ownership evidence or raw errors")
			}
			calls := len(runner.calls)
			actual, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
			if err == nil || actual != (protocol.AgentWorkspaceBinding{}) || len(runner.calls) != calls {
				t.Error("ambiguous retained intent authorized native planning or preparation")
			}
			after, _ := os.ReadFile(file)
			afterRegistry, _ := os.ReadFile(workspacegroup.Path(options.WorkspaceRoot))
			if !bytes.Equal(after, ambiguous) || !bytes.Equal(registry, afterRegistry) {
				t.Fatal("rejected intent was normalized or changed workspace registration")
			}
		})
	}
}

func TestAgentWorkspaceIntentMembersRejectRecoveryBeforeEffects(t *testing.T) {
	for _, mode := range []string{"stopped", "missing", "retained_add"} {
		for _, member := range []string{"intent_binding_duplicate", "binding_path_escaped_duplicate", "note_path_folded", "note_linking_key_duplicate", "intent_root_folded"} {
			t.Run(mode+"/"+member, func(t *testing.T) {
				options, request, runner := agentWorkspaceFixture(t)
				binding, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
				if err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(request.DataDir, "workspace.json")
				data, _ := os.ReadFile(file)
				var intent agentWorkspaceIntent
				_ = json.Unmarshal(data, &intent)
				data, _ = json.Marshal(intent)
				ambiguous := agentIntentMemberCases(data)[member]
				if len(ambiguous) == 0 {
					t.Fatal("missing ambiguity fixture")
				}
				if err := os.WriteFile(file, ambiguous, 0600); err != nil {
					t.Fatal(err)
				}
				request.Path, request.Expected = binding.Path, &binding
				if mode == "missing" {
					delete(runner.live, binding.Sandbox)
				} else {
					live := runner.live[binding.Sandbox]
					live.Status = "stopped"
					runner.live[binding.Sandbox] = live
				}
				if mode == "retained_add" {
					request.Path, request.Expected = "", nil
				}
				completed := 0
				if request.Expected != nil {
					request.Complete = func(context.Context, protocol.AgentWorkspaceBinding) error { completed++; return nil }
				}
				calls := len(runner.calls)
				actual, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
				if err == nil || actual != (protocol.AgentWorkspaceBinding{}) || len(runner.calls) != calls || completed != 0 {
					t.Error("ambiguous ownership entered recovery, runtime effects or runner completion")
				}
				after, _ := os.ReadFile(file)
				if !bytes.Equal(after, ambiguous) {
					t.Fatal("recovery rewrote ambiguous retained ownership")
				}
			})
		}
	}
}

func TestAgentWorkspaceIntentMembersPreserveValidDefaultsAndPrivateSubtrees(t *testing.T) {
	for _, kind := range []string{"native", "escaped_unique", "private_subtrees", "optional_defaults"} {
		t.Run(kind, func(t *testing.T) {
			options, request, runner := agentWorkspaceFixture(t)
			binding, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(request.DataDir, "workspace.json")
			data, _ := os.ReadFile(file)
			var object map[string]json.RawMessage
			_ = json.Unmarshal(data, &object)
			if kind == "private_subtrees" {
				for _, name := range []string{"binding", "note"} {
					object[name] = json.RawMessage(strings.TrimSpace(strings.TrimSuffix(string(bytes.TrimSpace(object[name])), "}")) + `,"private_extension":{"path":"private-first","path":"private-last","BINDING":null}}`)
				}
				object["private_extension"] = json.RawMessage(`{"root":"private-first","root":"private-last","NOTE":null}`)
			} else if kind == "optional_defaults" {
				var note map[string]json.RawMessage
				_ = json.Unmarshal(object["note"], &note)
				delete(note, "title")
				delete(note, "create")
				object["note"], _ = json.Marshal(note)
			}
			data, _ = json.Marshal(object)
			if kind == "escaped_unique" {
				data = []byte(strings.ReplaceAll(string(data), `"path":`, `"\u0070ath":`))
			}
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			if note, err := AgentWorkspaceNote(request.ID, request.DataDir, binding); err != nil || note == nil {
				t.Fatal("valid native ownership/defaults/extensions refused", err)
			}
			request.Path, request.Expected, request.Preview = binding.Path, &binding, true
			if actual, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true); err != nil || actual != binding {
				t.Fatal("valid native preview/defaults/extensions refused", actual, err)
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(after, data) {
				t.Fatal("valid ownership inspection rewrote private extensions")
			}
		})
	}
}

func TestAgentWorkspaceIntentMembersRejectAmbiguousPreparedNoteBeforeCreation(t *testing.T) {
	for _, field := range []string{"create", "title", "path", "linking_key"} {
		for _, kind := range []string{"duplicate", "escaped_duplicate", "folded"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				options, request, runner := agentWorkspaceFixture(t)
				request.Preview = true
				binding, plan, err := prepareAgentWorkspace(context.Background(), runner, options, request, false)
				if err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(agentWorkspaceIntent{Note: plan.Note})
				ambiguous := agentIntentMemberCases(encoded)["note_"+field+"_"+kind]
				if len(ambiguous) == 0 {
					t.Fatal("missing prepared-note ambiguity fixture")
				}
				var object map[string]json.RawMessage
				_ = json.Unmarshal(ambiguous, &object)
				request.Preview, request.PlanID, request.Note = false, plan.PlanID, object["note"]
				calls := len(runner.calls)
				actual, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, false)
				if err == nil || actual != (protocol.AgentWorkspaceBinding{}) || len(runner.calls) != calls {
					t.Error("ambiguous accepted note authorized planning or creation")
				}
				for _, path := range []string{binding.Path, plan.Note.Path, filepath.Join(request.DataDir, "workspace.json")} {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Error("ambiguous accepted note persisted ownership or created resources", path, err)
					}
				}
			})
		}
	}
}
