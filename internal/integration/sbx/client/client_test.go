package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type call struct {
	cwd, name string
	args      []string
}
type fakeRunner struct {
	installed map[string]bool
	calls     []call
	output    string
	failure   error
	paths     map[string]string
}

func (r *fakeRunner) LookPath(name string) error {
	if r.installed[name] {
		return nil
	}
	return errors.New("not installed")
}
func (r *fakeRunner) Run(_ context.Context, cwd, name string, args ...string) (string, error) {
	r.calls = append(r.calls, call{cwd, name, append([]string(nil), args...)})
	if name == "wslpath" {
		if len(args) != 2 || args[0] != "-u" {
			return "", errors.New("unexpected translation")
		}
		if value, ok := r.paths[args[1]]; ok {
			return value, nil
		}
		return "", errors.New("translation failed")
	}
	return r.output, r.failure
}

func TestSelectExecutable(t *testing.T) {
	for _, tt := range []struct {
		name, goos    string
		wsl           bool
		installed     []string
		want, failure string
	}{
		{"mac native", "darwin", false, []string{"sbx"}, "sbx", ""},
		{"linux native", "linux", false, []string{"sbx"}, "sbx", ""},
		{"WSL Windows", "linux", true, []string{"sbx.exe", "wslpath"}, "sbx.exe", ""},
		{"WSL native preferred", "linux", true, []string{"sbx", "sbx.exe", "wslpath"}, "sbx", ""},
		{"WSL native needs no converter", "linux", true, []string{"sbx"}, "sbx", ""},
		{"missing converter", "linux", true, []string{"sbx.exe"}, "", "wslpath"},
		{"missing on WSL", "linux", true, nil, "", "sbx or sbx.exe not found"},
		{"not WSL", "linux", false, []string{"sbx.exe", "wslpath"}, "", "sbx not found"},
		{"no Windows executable on macOS", "darwin", false, []string{"sbx.exe"}, "", "sbx not found"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{installed: map[string]bool{}}
			for _, name := range tt.installed {
				runner.installed[name] = true
			}
			got, err := SelectExecutable(tt.goos, tt.wsl, runner.LookPath)
			if got != tt.want || (tt.failure == "" && err != nil) || (tt.failure != "" && (err == nil || !strings.Contains(err.Error(), tt.failure))) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func windowsClient(r *fakeRunner) *Client {
	r.installed = map[string]bool{"sbx.exe": true, "wslpath": true}
	return &Client{runner: r, goos: "linux", wsl: true, distro: "Ubuntu"}
}

func TestWindowsListNormalizesPathsAndPreservesResources(t *testing.T) {
	unc := `\\wsl.localhost\Ubuntu\home\user\ABC-123`
	drive := `C:\Users\user\project with spaces`
	other := `\\wsl.localhost\Other\home\user\ABC-123`
	raw, _ := json.Marshal(map[string]any{"sandboxes": []any{
		map[string]any{"name": "ABC-123", "id": "one", "status": "running", "workspaces": []string{unc, drive}, "mounts": []string{unc, drive + ":ro"}, "kit_path": unc + `\kit`, "unknown_field": "preserved"},
		map[string]any{"name": "other-distro", "id": "two", "workspaces": []string{other}},
	}})
	runner := &fakeRunner{output: string(raw), paths: map[string]string{unc: "/home/user/ABC-123", drive: "/custom/drives/c/Users/user/project with spaces", unc + `\kit`: "/home/user/ABC-123/kit"}}
	client := windowsClient(runner)
	output, err := client.Run(context.Background(), "/workspace", "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Sandboxes []struct {
			Name, ID, Status   string
			Workspaces, Mounts []string
			KitPath            string `json:"kit_path"`
			Unknown            string `json:"unknown_field"`
		}
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatal(err)
	}
	got := response.Sandboxes[0]
	if got.Name != "ABC-123" || got.ID != "one" || got.Status != "running" || got.Unknown != "preserved" || got.KitPath != "/home/user/ABC-123/kit" {
		t.Fatalf("sandbox: %+v", got)
	}
	if !reflect.DeepEqual(got.Workspaces, []string{"/home/user/ABC-123", "/custom/drives/c/Users/user/project with spaces"}) || !reflect.DeepEqual(got.Mounts, []string{"/home/user/ABC-123", "/custom/drives/c/Users/user/project with spaces:ro"}) {
		t.Fatalf("paths: %+v", got)
	}
	if response.Sandboxes[1].Name != "other-distro" || len(response.Sandboxes[1].Workspaces) != 0 {
		t.Fatalf("foreign sandbox: %+v", response.Sandboxes[1])
	}
	if len(runner.calls) != 4 || runner.calls[0].cwd != "/" || runner.calls[0].name != "sbx.exe" {
		t.Fatalf("calls (conversion should be cached): %+v", runner.calls)
	}
}

func TestNativeListIsUnchanged(t *testing.T) {
	runner := &fakeRunner{installed: map[string]bool{"sbx": true}, output: `{"sandboxes":[{"workspaces":["/Users/user/work"]}]}`}
	client := &Client{runner: runner, goos: "darwin"}
	got, err := client.Run(context.Background(), "/workspace", "ls", "--json")
	if err != nil || got != runner.output || len(runner.calls) != 1 || runner.calls[0].cwd != "/workspace" {
		t.Fatalf("got %q, %v; calls %+v", got, err, runner.calls)
	}
}

func TestNoRuntimeFallbackOrCommandRewriting(t *testing.T) {
	runner := &fakeRunner{failure: errors.New("daemon unavailable")}
	client := windowsClient(runner)
	args := []string{"exec", "--workdir", "/sandbox/work", "ABC-123", "sh", "-lc", `cat '/home/user/file' && printf 'C:\work'`}
	if _, err := client.Run(context.Background(), "/host/work", args...); err == nil {
		t.Fatal("runtime failure lost")
	}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, args) {
		t.Fatalf("calls: %+v", runner.calls)
	}
	// Resolve once per client: a subsequently installed native CLI must not switch
	// the resource store between discovery and execution.
	runner.installed["sbx"] = true
	runner.failure = nil
	if _, err := client.Run(context.Background(), "", "rm", "--force", "ABC-123"); err != nil {
		t.Fatal(err)
	}
	if runner.calls[1].name != "sbx.exe" {
		t.Fatalf("switched installation: %+v", runner.calls)
	}
}

func TestListReportsMalformedOutputAndTranslationFailures(t *testing.T) {
	for _, output := range []string{
		"not json", `{"sandboxes":[{"workspaces":3}]}`, `{"sandboxes":[{"mounts":[3]}]}`,
		`{"sandboxes":[{"workspaces":["C:\\missing"]}]}`,
	} {
		t.Run(output, func(t *testing.T) {
			runner := &fakeRunner{output: output}
			if _, err := windowsClient(runner).Run(context.Background(), "", "ls", "--json"); err == nil {
				t.Fatal("failure hidden")
			}
		})
	}
	for _, converted := range []string{"", "relative", "//wsl.localhost/Other/work"} {
		runner := &fakeRunner{output: `{"sandboxes":[{"workspaces":["C:\\work"]}]}`, paths: map[string]string{`C:\work`: converted}}
		if _, err := windowsClient(runner).Run(context.Background(), "", "ls", "--json"); err == nil {
			t.Fatalf("invalid converted path accepted: %q", converted)
		}
	}
}

func TestWindowsManagedWorkspacesRejectedBeforeCommands(t *testing.T) {
	runner := &fakeRunner{}
	client := windowsClient(runner)
	if err := client.RequireManaged(); !errors.Is(err, ErrWindowsWorkspace) {
		t.Fatalf("got %v", err)
	}
	if _, err := client.Run(context.Background(), "", "create", "--name", "ABC-123", "shell", "/home/user/work"); !errors.Is(err, ErrWindowsWorkspace) {
		t.Fatalf("got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("provisioned unsupported workspace: %+v", runner.calls)
	}
}

func TestExecRunnerKeepsStderrOutOfJSONAndReportsFailures(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "fake sbx.exe")
	if err := os.WriteFile(command, []byte("#!/bin/sh\necho diagnostic >&2\nif [ \"$1\" = fail ]; then exit 1; fi\nprintf '%s' '{\"sandboxes\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := ExecRunner{}
	output, err := runner.Run(context.Background(), root, command, "ls")
	if err != nil || output != `{"sandboxes":[]}` {
		t.Fatalf("got %q, %v", output, err)
	}
	if _, err := runner.Run(context.Background(), root, command, "fail"); err == nil || !strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Run(ctx, root, command); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
