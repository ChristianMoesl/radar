package scripts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// The real publication script runs under system Bash, including macOS Bash 3.2.
// Only gh is replaced, so no test can touch a real release or use credentials.
func TestPublishReleaseUnderSystemBash(t *testing.T) {
	for _, test := range []struct {
		name, state string
		prerelease  bool
		wantExit    int
	}{
		{name: "stable", state: "absent"},
		{name: "prerelease", state: "absent", prerelease: true},
		{name: "identical retry", state: "complete"},
		{name: "different assets", state: "different", wantExit: 1},
		{name: "incomplete assets", state: "missing", wantExit: 1},
		{name: "failed download", state: "download failure", wantExit: 7},
		{name: "failed creation", state: "create failure", wantExit: 7},
		{name: "failed publication", state: "edit failure", wantExit: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "publication fixture with spaces")
			for _, dir := range []string{"bin", "dist", "published"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			tag := "v1.2.3"
			if test.prerelease {
				tag += "-rc.1"
			}
			files := []string{}
			for _, target := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
				files = append(files, "radar_"+tag+"_"+target+".tar.gz")
			}
			files = append(files, "checksums.txt", "release.json", "release.json.sig")
			for _, name := range files {
				data := []byte("fixture bytes for " + name)
				for _, dir := range []string{"dist", "published"} {
					if err := os.WriteFile(filepath.Join(root, dir, name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if test.state == "different" {
				if err := os.WriteFile(filepath.Join(root, "published", "release.json"), []byte("different"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if test.state == "missing" {
				if err := os.Remove(filepath.Join(root, "published", "release.json.sig")); err != nil {
					t.Fatal(err)
				}
			}
			before := publishedFixtureBytes(t, root)
			helper, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			stub := "#!/bin/sh\nexec \"$PUBLISH_HELPER\" -test.run=^TestPublishReleaseCLIHelper$ -- \"$@\"\n"
			if err := os.WriteFile(filepath.Join(root, "bin", "gh"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			script, err := filepath.Abs("publish-release.sh")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/bash", script, tag)
			cmd.Dir = root
			cmd.Env = []string{"HOME=" + root, "PATH=" + filepath.Join(root, "bin") + ":/usr/bin:/bin", "PUBLISH_HELPER=" + helper, "PUBLISH_FIXTURE=" + root, "PUBLISH_STATE=" + test.state}
			output, err := cmd.CombinedOutput()
			assertReleaseExit(t, err, test.wantExit, output)
			if strings.Contains(string(output), "unbound variable") {
				t.Fatalf("system Bash rejected release arguments: %s", output)
			}
			data, err := os.ReadFile(filepath.Join(root, "calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var calls [][]string
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var args []string
				if err := json.Unmarshal([]byte(line), &args); err != nil {
					t.Fatal(err)
				}
				calls = append(calls, args)
			}
			wantCommands := []string{"view"}
			switch test.state {
			case "absent", "create failure", "edit failure":
				wantCommands = append(wantCommands, "create")
				if test.state != "create failure" {
					wantCommands = append(wantCommands, "edit")
				}
			default:
				wantCommands = append(wantCommands, "download")
				if test.state == "complete" {
					wantCommands = append(wantCommands, "edit")
				}
			}
			var commands []string
			for _, args := range calls {
				if len(args) < 3 || args[0] != "release" || args[2] != tag {
					t.Fatalf("unexpected publication arguments: %q", args)
				}
				commands = append(commands, args[1])
				if args[1] == "create" {
					want := []string{"release", "create", tag}
					for _, file := range files {
						want = append(want, "dist/"+file)
					}
					want = append(want, "--repo", "ChristianMoesl/radar", "--verify-tag", "--title", tag, "--generate-notes", "--draft")
					if test.prerelease {
						want = append(want, "--prerelease")
					}
					if !reflect.DeepEqual(args, want) {
						t.Fatalf("create arguments=%q, want %q", args, want)
					}
				}
				if args[1] == "edit" && !reflect.DeepEqual(args, []string{"release", "edit", tag, "--repo", "ChristianMoesl/radar", "--draft=false"}) {
					t.Fatalf("unexpected release publication: %q", args)
				}
			}
			if !reflect.DeepEqual(commands, wantCommands) {
				t.Fatalf("commands=%q, want %q", commands, wantCommands)
			}
			if !reflect.DeepEqual(publishedFixtureBytes(t, root), before) {
				t.Fatal("publication changed existing immutable fixture assets")
			}
		})
	}
}

func publishedFixtureBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	files, err := os.ReadDir(filepath.Join(root, "published"))
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(root, "published", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[file.Name()] = string(data)
	}
	return result
}

func TestReleaseTriggerLogsOnlyEventIdentity(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to exercise workflow diagnostics")
	}
	data, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string }
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, step := range workflow.Jobs["release"].Steps {
		if step.Name == "Record release trigger" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("release workflow must record the push event's identity")
	}
	root := t.TempDir()
	eventFile := filepath.Join(root, "event.json")
	event := `{"before":"previous-tag-object","after":"new-tag-object","created":true,"deleted":false,"forced":false,"repository":{"private_details":"must-not-be-logged"},"token":"must-not-be-logged"}`
	if err := os.WriteFile(eventFile, []byte(event), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", "-eu", "-c", script)
	cmd.Env = append(os.Environ(), "GITHUB_EVENT_PATH="+eventFile, "GITHUB_RUN_ID=123", "GITHUB_RUN_ATTEMPT=1", "GITHUB_REF=refs/tags/v1.2.3", "GITHUB_SHA=peeled-commit")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workflow diagnostics failed: %v; %s", err, output)
	}
	var got map[string]any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"run_id": "123", "run_attempt": "1", "ref": "refs/tags/v1.2.3", "commit": "peeled-commit", "before": "previous-tag-object", "after": "new-tag-object", "created": true, "deleted": false, "forced": false}
	if !reflect.DeepEqual(got, want) || strings.Contains(string(output), "must-not-be-logged") {
		t.Fatalf("diagnostics must preserve tag-object identity without logging the full payload: %s", output)
	}
}

func TestPublishReleaseCLIHelper(t *testing.T) {
	root := os.Getenv("PUBLISH_FIXTURE")
	if root == "" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	log, err := os.OpenFile(filepath.Join(root, "calls.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(log).Encode(args); err != nil {
		panic(err)
	}
	_ = log.Close()
	if len(args) < 3 || args[0] != "release" {
		panic(fmt.Sprintf("unexpected gh arguments: %q", args))
	}
	state := os.Getenv("PUBLISH_STATE")
	switch args[1] {
	case "view":
		if state == "absent" || state == "create failure" || state == "edit failure" {
			os.Exit(1)
		}
	case "download":
		if state == "download failure" {
			os.Exit(7)
		}
		for i, arg := range args {
			if arg != "--dir" || i+1 >= len(args) {
				continue
			}
			for name, data := range publishedFixtureBytes(t, root) {
				if err := os.WriteFile(filepath.Join(args[i+1], name), []byte(data), 0600); err != nil {
					panic(err)
				}
			}
		}
	case "create":
		if state == "create failure" {
			os.Exit(7)
		}
	case "edit":
		if state == "edit failure" {
			os.Exit(7)
		}
	default:
		panic(fmt.Sprintf("unexpected publication command: %q", args))
	}
	os.Exit(0)
}
