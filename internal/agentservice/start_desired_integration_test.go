package agentservice_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestNativeStartRefusedWorkspacePlanDoesNotEnableSupervision(t *testing.T) {
	for _, operation := range []string{"start", "restart"} {
		t.Run(operation, func(t *testing.T) {
			for _, refusal := range []string{"stale_plan", "busy_anchor"} {
				t.Run(refusal, func(t *testing.T) {
					short, err := os.MkdirTemp("", "rad-")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(short) })
					root, configHome := t.TempDir(), t.TempDir()
					if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0700); err != nil {
						t.Fatal(err)
					}
					cfg := fmt.Sprintf("workspace:\n  root_dir: %q\n  auto_confirm: true\nsbx:\n  enabled: false\n", root)
					if err := os.WriteFile(filepath.Join(configHome, "radar", "config.yaml"), []byte(cfg), 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("XDG_CONFIG_HOME", configHome)
					t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
					t.Setenv("RADAR_SOCKET", filepath.Join(short, "radar.sock"))
					t.Setenv("RADAR_AGENT_ROOT", filepath.Join(short, "agents"))
					entry := filepath.Join(t.TempDir(), "definition.ts")
					if err := os.WriteFile(entry, []byte("export default {};\n"), 0600); err != nil {
						t.Fatal(err)
					}
					manager := agentservice.New(workspace.NewSource(obsidian.NewSourceAt(t.TempDir())))
					value, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: "worker", Operation: "add", Entrypoint: entry})
					if err != nil {
						t.Fatal(err)
					}
					var added protocol.AgentStatus
					if err := json.Unmarshal(value, &added); err != nil || added.Desired || added.Running {
						t.Fatal("invalid stopped registration", err)
					}
					request := protocol.AgentRequest{ID: "worker", Operation: operation, Preview: true}
					if _, err := manager.Handle(context.Background(), request); err != nil {
						t.Fatal(err)
					}
					files := []string{"registration.json", "workspace.json", "config.json", "ingress.token"}
					before := map[string][]byte{}
					for _, name := range files {
						data, err := os.ReadFile(filepath.Join(added.DataDir, name))
						if err != nil {
							t.Fatal(err)
						}
						before[name] = data
					}
					request.Preview = false
					expected := "agent workspace plan changed"
					if refusal == "stale_plan" {
						request.PlanID = "not-the-native-plan"
					} else {
						release, err := workspacelease.Acquire(added.Workspace.Path, true)
						if err != nil {
							t.Fatal(err)
						}
						defer release()
						expected = "in use by another operation"
					}
					_, err = manager.Handle(context.Background(), request)
					if err == nil || !strings.Contains(err.Error(), expected) {
						t.Fatal("wrong native refusal", err)
					}
					for _, name := range files {
						data, err := os.ReadFile(filepath.Join(added.DataDir, name))
						if err != nil || !bytes.Equal(data, before[name]) {
							t.Fatal("native refusal changed retained file", name, err)
						}
					}
					runCtx, cancel := context.WithCancel(context.Background())
					manager.Run(runCtx, cancel)
					cancel()
					data, err := os.ReadFile(filepath.Join(added.DataDir, "registration.json"))
					if err != nil || !bytes.Equal(data, before["registration.json"]) {
						t.Fatal("refused native plan enabled automatic supervision", err)
					}
				})
			}
		})
	}
}
