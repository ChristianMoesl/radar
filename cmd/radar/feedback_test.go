package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

func feedbackDiagnosticsFixture(t *testing.T) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "workspaces")
	configDir := filepath.Join(home, "config/radar")
	anchor := filepath.Join(root, "feedback")
	for _, path := range []string{configDir, anchor} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_SOCKET", filepath.Join(home, "absent.sock"))
	log := filepath.Join(home, "radar.log")
	t.Setenv("RADAR_LOG", log)
	config := fmt.Sprintf("workspace:\n  root_dir: %s\nmodel: example/model\n", root)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "Feedback", Path: anchor, Feedback: true, Members: []workspacegroup.Member{}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	return root, anchor, log
}

func TestFeedbackDiagnosticsAreScopedReadOnlyAndDoNotReadLogsOrSecretsByDefault(t *testing.T) {
	root, anchor, log := feedbackDiagnosticsFixture(t)
	private := "do-not-include-private-content"
	if err := os.WriteFile(log, []byte(private), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar/secrets.yaml")
	if err := os.WriteFile(secrets, []byte("unknown-provider:\n  token: "+private+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(workspacegroup.Path(root))
	result, err := feedbackDiagnostics(anchor, "overview")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), private) || result["logs"] != nil || result["settings"] == nil || result["daemon"].(map[string]any)["available"] != false {
		t.Fatalf("unexpected automatic diagnostic data: %s", data)
	}
	after, _ := os.ReadFile(workspacegroup.Path(root))
	if string(before) != string(after) {
		t.Fatal("diagnostics mutated the registry")
	}
	if _, err := os.Stat(os.Getenv("RADAR_SOCKET")); !os.IsNotExist(err) {
		t.Fatal("diagnostics started a daemon")
	}
	result, err = feedbackDiagnostics(anchor, "logs")
	if err != nil || result["private"] != true || result["logs"] != private {
		t.Fatalf("explicit log projection: %+v %v", result, err)
	}
	registry, _ := workspacegroup.Load(root)
	registry.Workspaces[0].Feedback = false
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	if _, err := feedbackDiagnostics(anchor, "overview"); err == nil {
		t.Fatal("ordinary workspace gained host diagnostics")
	}
	if _, err := feedbackDiagnostics(t.TempDir(), "logs"); err == nil {
		t.Fatal("unmanaged directory gained host diagnostics")
	}
}

func TestFeedbackDiagnosticsDoNotEnrollRemovedOrigin(t *testing.T) {
	root, anchor, _ := feedbackDiagnosticsFixture(t)
	registry, _ := workspacegroup.Load(root)
	registry.Workspaces[0].FeedbackOrigin = filepath.Join(root, "removed-origin")
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	result, err := feedbackDiagnostics(anchor, "overview")
	if err != nil || result["origin"].(map[string]any)["available"] != false {
		t.Fatalf("removed origin was inspected/enrolled: %+v %v", result, err)
	}
}

func TestFeedbackLogTailIsBoundedByLinesAndBytes(t *testing.T) {
	_, _, log := feedbackDiagnosticsFixture(t)
	var lines []string
	for i := range 200 {
		lines = append(lines, fmt.Sprintf("line-%03d", i))
	}
	if err := os.WriteFile(log, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	text, truncated, err := feedbackLogTail(log)
	if err != nil || !truncated || len(strings.Split(text, "\n")) != 100 || !strings.HasPrefix(text, "line-100") || !strings.HasSuffix(text, "line-199") {
		t.Fatalf("tail=%q truncated=%v err=%v", text, truncated, err)
	}
	if err := os.WriteFile(log, []byte(strings.Repeat("x", 100000)), 0600); err != nil {
		t.Fatal(err)
	}
	text, truncated, err = feedbackLogTail(log)
	if err != nil || !truncated || len(text) > 32*1024 {
		t.Fatalf("unbounded tail: %d %v %v", len(text), truncated, err)
	}
}

func TestFeedbackDiagnosticsCLIUsesStrictMachineBoundary(t *testing.T) {
	_, anchor, _ := feedbackDiagnosticsFixture(t)
	env := []string{"HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "RADAR_SOCKET=" + os.Getenv("RADAR_SOCKET")}
	stdout, stderr, code := outputCLI(t, []string{"feedback-diagnostics", "--workspace", anchor, "--section", "overview", "--json"}, "", env...)
	if code != 0 || !strings.Contains(stdout, `"section":"overview"`) {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	for _, args := range [][]string{
		{"feedback-diagnostics", "--workspace", anchor},
		{"feedback-diagnostics", "--workspace", anchor, "--section", "arbitrary", "--json"},
		{"feedback-diagnostics", "--workspace", anchor, "--path", "/private", "--json"},
	} {
		if _, _, code := outputCLI(t, args, "", env...); code == 0 {
			t.Fatalf("accepted invalid request: %v", args)
		}
	}
}
