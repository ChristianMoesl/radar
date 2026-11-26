package agentservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// Change only the selected registration/workspace boundary. Unknown private
// extension subtrees are not registration identity or ownership evidence.
func ambiguousRegistrationMembers(reg Registration) map[string][]byte {
	encoded, _ := json.Marshal(reg)
	var top map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &top)
	var workspace map[string]json.RawMessage
	_ = json.Unmarshal(top["workspace"], &workspace)
	cases := map[string][]byte{}
	for boundary, members := range map[string]map[string]json.RawMessage{"registration": top, "workspace": workspace} {
		raw := string(encoded)
		if boundary == "workspace" {
			raw = string(top["workspace"])
		}
		for name, value := range members {
			marker := `"` + name + `":`
			variants := map[string]string{
				"duplicate":         strings.Replace(raw, marker, marker+string(value)+","+marker, 1),
				"folded":            strings.Replace(raw, marker, `"`+strings.ToUpper(name)+`":`, 1),
				"escaped_duplicate": strings.Replace(raw, marker, fmt.Sprintf(`"\u%04x%s":`, name[0], name[1:])+string(value)+","+marker, 1),
			}
			for kind, changed := range variants {
				if boundary == "workspace" {
					changed = strings.Replace(string(encoded), string(top["workspace"]), changed, 1)
				}
				cases[boundary+"_"+name+"_"+kind] = []byte(changed)
			}
		}
	}
	path, _ := json.Marshal(reg.Workspace.Path)
	other, _ := json.Marshal(reg.Workspace.Path + "-private-unrelated")
	cases["hidden_owner_last_path"] = []byte(strings.Replace(string(encoded), `"path":`+string(path), `"path":`+string(path)+`,"path":`+string(other), 1))
	cases["hidden_owner_folded_last_path"] = []byte(strings.Replace(string(encoded), `"path":`+string(path), `"path":`+string(path)+`,"PATH":`+string(other), 1))
	cases["hidden_owner_duplicate_workspace"] = []byte(strings.TrimSuffix(string(encoded), "}") + `,"workspace":{"id":"private-other","path":` + string(other) + `,"mode":"local"}}`)
	return cases
}

func TestAmbiguousAgentRegistrationCannotSupplyOwnershipOrProbeEvidence(t *testing.T) {
	for name := range ambiguousRegistrationMembers(Registration{ID: "worker", Collection: "github", Workspace: protocol.AgentWorkspaceBinding{Path: "/fixture", Sandbox: "private-sbx"}}) {
		t.Run(name, func(t *testing.T) {
			reg := probeFixture(t)
			reg.Collection, reg.Workspace.Sandbox = "github", "private-sbx"
			// A valid prefix must not escape from the complete registry scan.
			diagnosticRegistration(t, os.Getenv("RADAR_AGENT_ROOT"), "owner-a", reg.Workspace.Path)
			raw := ambiguousRegistrationMembers(reg)[name]
			dir, _ := instanceDir(reg.ID)
			file := filepath.Join(dir, "registration.json")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			methods := nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			if _, err := decodeRegistration(raw, reg.ID); err == nil || strings.Contains(err.Error(), "private-") {
				t.Error("ambiguous native registration accepted or private metadata leaked")
			}
			if regs, err := registrations(); err == nil || regs != nil {
				t.Error("native scan published an ambiguous/partial registry")
			}
			if agents, err := DiagnosticAgents("", ""); err == nil || agents != nil {
				t.Error("diagnostics published ambiguous/partial agents")
			}
			if schedules, err := DiagnosticAgentSchedules("", "", ""); err == nil || schedules != nil {
				t.Error("diagnostics published ambiguous/partial schedules")
			}
			paths := []string{reg.Workspace.Path}
			if owners, err := WorkspaceRemovalOwners(paths); err == nil || owners != nil {
				t.Error("native owner evidence chose an ambiguous registration")
			}
			if counts, fingerprint, err := DiagnosticWorkspaceRemovalOwners(context.Background(), paths); err == nil || counts != nil || fingerprint != ([32]byte{}) {
				t.Error("ambiguous registration became usable counts or a freshness fingerprint")
			}
			if owners, err := DiagnosticWorkspaceOwnerIDs(context.Background(), paths); err == nil || owners != nil {
				t.Error("ambiguous registration became owner identities")
			}
			if _, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID); err == nil {
				t.Error("ambiguous selected registration authorized a runner probe")
			}
			if len(methods()) != 0 {
				t.Error("registration refusal contacted a runner")
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(raw, after) {
				t.Error("inspection rewrote ambiguous metadata")
			}
		})
	}
}

func TestAmbiguousAgentRegistrationCannotAuthorizeNativeControl(t *testing.T) {
	for _, name := range []string{"hidden_owner_last_path", "hidden_owner_folded_last_path", "hidden_owner_duplicate_workspace", "registration_desired_duplicate", "registration_id_escaped_duplicate", "workspace_path_folded"} {
		t.Run(name, func(t *testing.T) {
			for _, operation := range []string{"inspect", "start", "stop", "restart", "remove", "emit", "interact", "conversation"} {
				t.Run(operation, func(t *testing.T) {
					reg := probeFixture(t)
					dir, _ := instanceDir(reg.ID)
					file := filepath.Join(dir, "registration.json")
					raw := ambiguousRegistrationMembers(reg)[name]
					if err := os.WriteFile(file, raw, 0600); err != nil {
						t.Fatal(err)
					}
					methods := nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
					workspaceCalls, prepared := 0, 0
					manager := New(workspaceFixture{
						lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
							workspaceCalls++
							return reg.Workspace, nil
						},
						prepare: func(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
							prepared++
							return protocol.AgentWorkspaceBinding{}, integration.WorkspaceReconcilePlan{}, errors.New("unexpected fixture preparation")
						},
					})
					if _, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: operation, Params: json.RawMessage(`{}`)}); err == nil {
						t.Error("ambiguous registration authorized native control")
					}
					if workspaceCalls != 0 || prepared != 0 || len(methods()) != 0 {
						t.Error("registration refusal reached workspace/runtime effects")
					}
					after, err := os.ReadFile(file)
					if err != nil || !bytes.Equal(raw, after) {
						t.Error("refused control rewrote or removed retained registration")
					}
					if err := CheckWorkspaceRemoval(reg.Workspace.Path); err == nil {
						t.Error("ambiguous metadata released the original workspace ownership hold")
					}
				})
			}
		})
	}
}

func TestUnambiguousAgentRegistrationRetainsNativeDefaultsAndPrivateExtensionIsolation(t *testing.T) {
	reg := Registration{Version: 1, ID: "worker", Entrypoint: "private-entrypoint", Workspace: protocol.AgentWorkspaceBinding{ID: "workspace", Path: "/registered/workspace", Mode: "local"}}
	raw, _ := json.Marshal(reg)
	for _, data := range []string{
		string(raw),
		strings.TrimSuffix(string(raw), "}") + `,"extension":{"id":"one","ID":"two","private":1,"private":2}}`,
		strings.Replace(string(raw), `"mode":"local"`, `"mode":"local","extension":{"path":"one","path":"two"}`, 1),
		strings.Replace(string(raw), `"desired":false,`, ``, 1),
	} {
		decoded, err := decodeRegistration([]byte(data), reg.ID)
		if err != nil || decoded != reg {
			t.Fatal("unambiguous metadata/defaults or unknown private extensions changed", err)
		}
	}
}

func TestAgentRegistrationSelectedObjectBounds(t *testing.T) {
	reg := Registration{Version: 1, ID: "worker", Workspace: protocol.AgentWorkspaceBinding{ID: "workspace", Path: "/registered/workspace", Mode: "local"}}
	raw, _ := json.Marshal(reg)
	for _, boundary := range []string{"registration", "workspace"} {
		t.Run(boundary, func(t *testing.T) {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			selected := fields
			if boundary == "workspace" {
				selected = map[string]json.RawMessage{}
				_ = json.Unmarshal(fields["workspace"], &selected)
			}
			for len(selected) < 64 {
				selected[fmt.Sprintf("extension_%02d", len(selected))] = json.RawMessage(`null`)
			}
			encode := func() []byte {
				if boundary == "workspace" {
					fields["workspace"], _ = json.Marshal(selected)
				}
				data, _ := json.Marshal(fields)
				return data
			}
			if _, err := decodeRegistration(encode(), reg.ID); err != nil {
				t.Fatal("complete object at native member bound rejected", err)
			}
			selected["extension_over_bound"] = json.RawMessage(`null`)
			if _, err := decodeRegistration(encode(), reg.ID); err == nil {
				t.Fatal("over-bound native metadata accepted")
			}
		})
	}
	for _, extra := range []int{0, 1} {
		data := append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), protocol.AgentMaxFrameBytes+extra-len(raw))...)
		_, err := decodeRegistration(data, reg.ID)
		if (err != nil) != (extra == 1) {
			t.Fatal("native registration byte bound changed", extra, err)
		}
	}
}
