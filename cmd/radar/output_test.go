package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestResultFormats(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   any
		want    []string
		warning string
	}{
		{"task", &protocol.Task{ID: 42, Title: "Ship feature", Attention: "in_progress", Muted: true}, []string{"Task 42: Ship feature", "State: in progress", "Muted: yes"}, ""},
		{"delete", &protocol.TaskDeletionResult{TaskID: 42, OriginalPath: "/notes/task.md", TrashPath: "/notes/.trash/task.md"}, []string{"Deleted authored task 42.", "Original path: /notes/task.md", "Trash path: /notes/.trash/task.md"}, ""},
		{"cleanup", &protocol.CleanupResult{TaskID: 42, Targets: []protocol.CleanupTarget{{Description: "worktree /work/feature"}}}, []string{"Cleaned up 1 local resource(s) for task 42.", "worktree /work/feature"}, ""},
		{"gc", &protocol.GarbageCollectionResult{Deleted: []protocol.GarbageCollectionItem{{TaskID: 42, Path: "/work/old"}}, Skipped: []protocol.GarbageCollectionItem{{TaskID: 43, Path: "/work/dirty", Reason: "local changes"}}}, []string{"1 deleted, 1 skipped", "/work/old", "/work/dirty", "local changes"}, ""},
		{"empty gc", &protocol.GarbageCollectionResult{}, []string{"0 deleted, 0 skipped"}, ""},
		{"create", integration.Workspace{Name: "feature", Path: "/work/feature", Branch: "feature", Warning: "refresh failed"}, []string{"Workspace ready.", "Path: /work/feature", "Branch: feature"}, "refresh failed"},
		{"registered", registrationResult{Registered: true, WorkspacePath: "/work/feature"}, []string{"Registered Radar workspace: /work/feature"}, ""},
		{"unregistered", registrationResult{}, []string{"Not inside a registered Radar workspace."}, ""},
		{"context", integration.WorkspaceContext{WorkspaceName: "feature", Registered: true, Members: []integration.WorkspaceContextMember{{Branch: "feature", Path: "/work/member", Dirty: true}}, Sandbox: &integration.WorkspaceContextSandbox{Name: "feature", Ports: []integration.SandboxPort{{HostPort: 3000, SandboxPort: 8080}}}, Repositories: []integration.WorkspaceContextRepository{{Name: "app", Path: "/repo/app", AlreadyMember: true}}}, []string{"Workspace: feature", "Registered: yes", "dirty", "/work/member", "127.0.0.1:3000 -> 8080", "/repo/app"}, ""},
		{"empty context", integration.WorkspaceContext{}, []string{"No member worktrees.", "Sandbox: none", "Available repositories (0)"}, ""},
		{"refs", integration.RepositoryRefs{Repository: "/repo/app", DefaultBranch: "main", BaseRefs: []string{"origin/main"}, Branches: []integration.RepositoryBranch{{Name: "main", Local: true, Origin: true, CheckedOutPaths: []string{"/repo/app"}}}, Warning: "using cached refs"}, []string{"Repository: /repo/app", "Default branch: main", "origin/main", "CHECKOUTS"}, "using cached refs"},
		{"empty refs", integration.RepositoryRefs{}, []string{"No branches found."}, ""},
		{"plan", integration.WorkspaceReconcilePlan{PlanID: "plan-123", Revision: "revision-1", Changes: []integration.WorkspaceChange{{Action: "add", Resource: "worktree", Summary: "Add feature"}}, Warnings: []string{"review carefully"}}, []string{"Plan ID: plan-123", "Revision: revision-1", "Planned changes (1)", "Add feature"}, "review carefully"},
		{"empty plan", integration.WorkspaceReconcilePlan{}, []string{"No changes required."}, ""},
		{"reconciled", integration.WorkspaceReconcileResult{OK: true, WorktreesAdded: 1, NoteAdded: true}, []string{"Workspace reconciled.", "Worktrees: 1 added, 0 removed", "Task note attached."}, ""},
		{"incomplete", integration.WorkspaceReconcileResult{Reason: "plan_changed", ReconfirmRequired: true, Plan: &integration.WorkspaceReconcilePlan{PlanID: "new-plan"}}, []string{"reconciliation incomplete", "plan_changed", "review and confirm", "new-plan"}, ""},
		{"retryable", integration.WorkspaceReconcileResult{Retryable: true, Warning: "sandbox pending"}, []string{"reconciliation incomplete", "Inspect the current workspace and retry"}, "sandbox pending"},
		{"path", pathResult{Path: "/home/me/state.json"}, []string{"/home/me/state.json\n"}, ""},
		{"rate limit", rateLimitResult{Summary: "4999 remaining"}, []string{"4999 remaining"}, ""},
		{"stop", daemonResult{OK: true, Status: "stopped", PIDs: []int{123}}, []string{"Radar daemon stopped.", "PIDs: [123]"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := writeResult(&stdout, &stderr, test.value, false); err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("missing %q in %q", want, stdout.String())
				}
			}
			if test.warning != "" {
				if !strings.Contains(stderr.String(), "Warning: "+test.warning) || strings.Contains(stdout.String(), test.warning) {
					t.Fatalf("warnings not separated: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
			} else if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %q", stderr.String())
			}
			if strings.Contains(stdout.String(), "\x1b") {
				t.Fatal("unexpected terminal escapes")
			}
			stdout.Reset()
			stderr.Reset()
			if err := writeResult(&stdout, &stderr, test.value, true); err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(stdout.String()) != string(want) || stderr.Len() != 0 {
				t.Fatalf("JSON changed: stdout=%q stderr=%q, want %s", stdout.String(), stderr.String(), want)
			}
		})
	}
}

func TestDaemonHumanOutput(t *testing.T) {
	response := protocol.Response{OK: true, Summary: &protocol.Summary{Attention: 1, Muted: 1}, Tasks: []protocol.Task{{ID: 42, Title: "Feature\nwith\ttitle\x1b[31m!", Attention: "attention", Muted: true}}, Sources: []protocol.SourceStatus{{Name: "provider", Status: "error", Detail: "offline"}}}
	for _, method := range []string{"summary", "tasks", "refresh", "reset", "ack:42"} {
		var out bytes.Buffer
		if err := writeDaemonResult(&out, method, response); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "1 need attention") || !strings.Contains(out.String(), "1 muted") || !strings.Contains(out.String(), "offline") {
			t.Fatalf("incomplete %s output: %q", method, out.String())
		}
		if method == "tasks" && (!strings.Contains(out.String(), "Feature with title!") || !strings.Contains(out.String(), "muted")) {
			t.Fatalf("table: %q", out.String())
		}
		if strings.Contains(out.String(), "\x1b") {
			t.Fatal("unescaped terminal controls")
		}
	}
	var out bytes.Buffer
	_ = writeDaemonResult(&out, "tasks", protocol.Response{OK: true})
	if out.String() != "No tasks.\n" {
		t.Fatalf("empty tasks = %q", out.String())
	}
}

func TestParseOutputFlags(t *testing.T) {
	for _, test := range []struct {
		args       []string
		json       bool
		title      string
		positional []string
		fails      bool
	}{
		{args: []string{"42", "--json"}, json: true, positional: []string{"42"}},
		{args: []string{"--json", "42"}, json: true, positional: []string{"42"}},
		{args: []string{"--title", "--json"}, title: "--json"},
		{args: []string{"--title=--json", "--json"}, title: "--json", json: true},
		{args: []string{"--json=false", "42"}, positional: []string{"42"}},
		{args: []string{"--", "--json"}, positional: []string{"--json"}},
		{args: []string{"--json", "--", "--title"}, json: true, positional: []string{"--title"}},
		{args: []string{"--json=bogus"}, fails: true},
		{args: []string{"--title"}, fails: true},
		{args: []string{"42", "--bogus"}, fails: true},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			asJSON := flags.Bool("json", false, "")
			title := flags.String("title", "", "")
			err := parseFlags(flags, test.args)
			if (err != nil) != test.fails {
				t.Fatalf("parse = %v", err)
			}
			if test.fails {
				return
			}
			if *asJSON != test.json || *title != test.title || strings.Join(flags.Args(), "|") != strings.Join(test.positional, "|") {
				t.Fatalf("json=%t title=%q args=%q", *asJSON, *title, flags.Args())
			}
		})
	}
}

func TestFatalOutputModes(t *testing.T) {
	previous := jsonOutput
	t.Cleanup(func() { jsonOutput = previous })
	for _, mode := range []bool{false, true} {
		jsonOutput = mode
		message := fatalMessage(errors.New("cannot continue"))
		if mode {
			var got map[string]string
			if err := json.Unmarshal([]byte(message), &got); err != nil || !reflect.DeepEqual(got, map[string]string{"error": "cannot continue"}) {
				t.Fatalf("JSON error = %q (%v)", message, err)
			}
		} else if message != "radar: cannot continue" {
			t.Fatalf("human error = %q", message)
		}
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestOutputWriteErrors(t *testing.T) {
	for _, mode := range []bool{false, true} {
		if err := writeResult(failingOutput{}, io.Discard, pathResult{Path: "/tmp/path"}, mode); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("write error = %v", err)
		}
	}
}
