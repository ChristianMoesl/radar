package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestAgentRemoveSourceDelegatesOriginalNativeAdmission(t *testing.T) {
	for _, kind := range []string{"allowed", "denied", "nil_guard", "wrong_id", "wrong_workspace", "desired", "unknown_owner", "cancelled_capture", "cancelled_apply", "binding_drift", "schedule_drift", "late_callback", "double_callback", "guard_binding_drift", "guard_live_owner", "cancelled_after_effects", "error_after_effects"} {
		t.Run(kind, func(t *testing.T) {
			root, err := os.MkdirTemp("", "rr-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			binding := protocol.AgentWorkspaceBinding{ID: "workspace-worker", Path: filepath.Join(root, "workspace"), Mode: "local"}
			reg := agentservice.Registration{Version: 1, ID: "worker", Entrypoint: "private-definition", Workspace: binding, Desired: kind == "desired"}
			dir := filepath.Join(root, "agents", reg.ID)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(reg)
			path := filepath.Join(dir, "registration.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"snapshot.json", "ingress.token", "collection.json", "config.json", "workspace.json"} {
				if err := syscall.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			schedules := []byte(`{"version":1,"schedules":[{"name":"morning","time":"09:00","weekdays":["mon"],"timezone":"UTC","payload":{"private":"payload"},"id":"schedule-one","enabled":true,"eligible_after":"2026-10-26T08:00:00Z"},{"name":"evening","time":"18:00","weekdays":["mon"],"timezone":"UTC","payload":{"private":"other"},"id":"schedule-two","enabled":false,"eligible_after":"2026-10-26T08:00:00Z"}]}`)
			schedulePath := filepath.Join(dir, "schedules.json")
			if err := os.WriteFile(schedulePath, schedules, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "unknown_owner" {
				if err := os.WriteFile(filepath.Join(dir, "pid"), []byte("private-invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			workspace := &stopWorkspaceFixture{binding: binding}
			source := NewSource(workspace)
			provider, err := integration.NewRegistry(source).AgentRemoveDiagnostics()
			if err != nil || provider != source {
				t.Fatal("missing typed native provider", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			var retained func() error
			guard := integration.AgentRemoveGuard(func(_ context.Context, execute func() error) error {
				calls++
				switch kind {
				case "denied":
					return errors.New("private-caller-refusal")
				case "late_callback":
					retained = execute
					return nil
				case "double_callback":
					_ = execute()
					return execute()
				case "guard_binding_drift":
					workspace.binding.ID += "-other"
				case "guard_live_owner":
					if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.ReadFile(schedulePath)
				if err != nil || !bytes.Equal(before, schedules) {
					t.Fatal("final guard entered after schedule effects", err)
				}
				err = execute()
				if kind == "cancelled_after_effects" {
					cancel()
				}
				if kind == "error_after_effects" {
					return errors.New("private-lost-reply")
				}
				return err
			})
			id, expected := reg.ID, binding
			if kind == "nil_guard" {
				guard = nil
			}
			if kind == "wrong_id" {
				id = "other"
			}
			if kind == "wrong_workspace" {
				expected.ID += "-other"
			}
			if kind == "cancelled_capture" {
				cancel()
			}
			start := time.Now()
			plan, err := provider.PrepareDiagnosticAgentRemove(ctx, id, expected, guard)
			captureRefused := kind == "nil_guard" || kind == "wrong_id" || kind == "wrong_workspace" || kind == "desired" || kind == "unknown_owner" || kind == "cancelled_capture"
			if captureRefused {
				if err == nil || plan != nil || strings.Contains(err.Error(), "private") || calls != 0 || workspace.prepares != 0 {
					t.Fatal("capture admitted or leaked native evidence", err)
				}
				return
			}
			if err != nil || plan == nil || calls != 0 || workspace.prepares != 0 {
				t.Fatal("source performed apply/guard/workspace preparation during capture", err)
			}
			preview := integration.AgentRemovePreview{ID: reg.ID, Schedules: 2, EnabledSchedules: 1}
			if plan.Preview() != preview || !plan.ExpiresAt().After(start) || plan.ExpiresAt().After(time.Now().Add(30*time.Second)) || plan.RetainedBytes() < 64*1024 {
				t.Fatal("typed source lost native preview or retention", plan.Preview())
			}
			mutable := plan.Preview()
			mutable.ID, mutable.Schedules, mutable.EnabledSchedules = "other", 99, 0
			if plan.Preview() != preview {
				t.Fatal("mutable view changed native retained evidence")
			}
			encoded, _ := json.Marshal(plan)
			if string(encoded) != "{}" {
				t.Fatal("private executable plan serialized", string(encoded))
			}
			encoded, _ = json.Marshal(plan.Preview())
			if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), root) || strings.Contains(string(encoded), "schedule-one") {
				t.Fatal("preview exposed private native state")
			}
			before, _ := os.ReadFile(path)
			beforeSchedules, _ := os.ReadFile(schedulePath)
			if !bytes.Equal(before, data) || !bytes.Equal(beforeSchedules, schedules) || agentservice.CheckWorkspaceRemoval(binding.Path) == nil {
				t.Fatal("capture mutated native state or released ownership")
			}
			guard = func(context.Context, func() error) error { t.Error("source replaced captured final guard"); return nil }
			if kind == "binding_drift" {
				workspace.binding.ID += "-other"
			}
			if kind == "cancelled_apply" {
				cancel()
			}
			if kind == "schedule_drift" {
				if err := os.WriteFile(schedulePath, append(schedules, ' '), 0600); err != nil {
					t.Fatal(err)
				}
			}
			copy := plan
			err = plan.Execute(ctx)
			if (kind == "allowed") != (err == nil) || err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("source bypassed/lost native admission or leaked error", err)
			}
			if err := copy.Execute(context.Background()); err == nil {
				t.Fatal("interface copy replayed native admission")
			}
			if retained != nil {
				if err := retained(); err == nil {
					t.Fatal("retained callback escaped native manager")
				}
			}
			changed := kind == "allowed" || kind == "double_callback" || kind == "cancelled_after_effects" || kind == "error_after_effects"
			_, statErr := os.Lstat(path)
			if changed != os.IsNotExist(statErr) || workspace.prepares != 0 {
				t.Fatal("refusal mutated registration or post-effect uncertainty rolled back", statErr)
			}
			if plan.Preview() != preview {
				t.Fatal("execution rewrote capture facts into outcomes")
			}
			afterSchedules, _ := os.ReadFile(schedulePath)
			if changed {
				var want, got map[string]any
				if json.Unmarshal(schedules, &want) != nil || json.Unmarshal(afterSchedules, &got) != nil {
					t.Fatal("lost schedule checkpoint")
				}
				for _, v := range want["schedules"].([]any) {
					v.(map[string]any)["enabled"] = false
				}
				if !reflect.DeepEqual(want, got) || agentservice.CheckWorkspaceRemoval(binding.Path) != nil {
					t.Fatal("native removal replaced retained schedule identity/data or retained its owner association")
				}
			} else if kind != "schedule_drift" && !bytes.Equal(afterSchedules, schedules) {
				t.Fatal("pre-effect refusal changed schedules")
			}
			if calls != 1 && kind != "cancelled_apply" && kind != "binding_drift" && kind != "schedule_drift" {
				t.Fatal("unexpected final guard count", calls)
			}
		})
	}
}
