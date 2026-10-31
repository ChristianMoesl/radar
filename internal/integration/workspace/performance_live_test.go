package workspace_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
	"radar/internal/cleanup"

	"radar/internal/config"
	"radar/internal/integration"
	gitintegration "radar/internal/integration/git"
	sbxintegration "radar/internal/integration/sbx"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/phasetiming"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/taskservice"
)

type liveTiming struct {
	Metric     string         `json:"metric"`
	Version    string         `json:"version"`
	Condition  string         `json:"condition"`
	DurationNS int64          `json:"duration_ns"`
	Commands   map[string]int `json:"commands,omitempty"`
}

// This opt-in qualification uses only a fresh fixture and its named disposable
// runtimes. It never loads configured user repositories, kits, hooks or secrets.
func TestWorkspaceLivePerformance(t *testing.T) {
	if os.Getenv("RADAR_WORKSPACE_PERFORMANCE") != "1" {
		t.Skip("explicit disposable-provider qualification only")
	}
	namespace := os.Getenv("GITHUB_RUN_ID")
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(namespace) {
		t.Fatal("qualification requires a GitHub disposable-run identity")
	}
	baseline, current, kit, report := os.Getenv("RADAR_PERF_BEFORE"), os.Getenv("RADAR_PERF_AFTER"), os.Getenv("RADAR_PERF_KIT"), os.Getenv("RADAR_PERF_REPORT")
	for _, path := range []string{baseline, current, kit, report} {
		if !filepath.IsAbs(path) {
			t.Fatal("qualification paths must be absolute")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 34*time.Minute)
	defer cancel()
	base := t.TempDir()
	providerEnvironment := map[string]*string{}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if value, ok := os.LookupEnv(name); ok {
			providerEnvironment[name] = &value
		} else {
			providerEnvironment[name] = nil
		}
	}
	root, sources := filepath.Join(base, "workspaces"), filepath.Join(base, "sources")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("RADAR_STATE", filepath.Join(base, "state.db"))
	t.Setenv("RADAR_SOCKET", filepath.Join(base, "absent.sock"))
	cfg := config.Default()
	cfg.Workspace.RootDir = root
	cfg.RepositoryDirs = []string{sources}
	cfg.LinkingMarkPrefixes = []string{"ABC"}
	enabled := true
	cfg.SBX.Enabled = &enabled
	cfg.SBX.Kit.Name = kit
	cfg.SBX.ReadyCommand = []string{"sandbox-startup", "run"}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	countsPath := filepath.Join(base, "commands")
	installLiveCounters(t, base, countsPath, providerEnvironment)
	run := func(cwd, name string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = cwd
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("disposable qualification command %s failed; private command output suppressed", filepath.Base(name))
		}
		return strings.TrimSpace(string(output))
	}
	for i := range 62 {
		run("", "git", "init", "--quiet", "--initial-branch=main", filepath.Join(sources, fmt.Sprintf("repo-%02d", i)))
	}
	repo := filepath.Join(sources, "repo-00")
	origin := filepath.Join(base, "origin.git")
	run("", "git", "init", "--quiet", "--bare", origin)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(repo, "git", "add", ".gitignore")
	run(repo, "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--quiet", "-m", "fixture")
	run(repo, "git", "remote", "add", "origin", origin)
	run(repo, "git", "push", "--quiet", "origin", "main")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	versions := map[string]any{"before": "21e4af9", "after": os.Getenv("GITHUB_SHA"), "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "kernel": run("", "uname", "-sr"), "provider": json.RawMessage(run("", "sbx", "version", "--json")), "image": os.Getenv("RADAR_PERF_IMAGE"), "planned_workspace_rounds": 20, "planned_reads_per_projection": 20, "fixture_members": 4, "discovered_repositories": 62, "filesystem_condition": "newly written fixture, warm metadata; no cache eviction", "provider_condition": "first provision on fresh runner, then warm image/daemon; every VM is new"}
	var timings []liveTiming
	measure := func(metric, version, condition string, operation func()) {
		before := readLiveCounters(countsPath)
		start := time.Now()
		operation()
		duration := time.Since(start)
		after := readLiveCounters(countsPath)
		for name, count := range before {
			after[name] -= count
		}
		timings = append(timings, liveTiming{Metric: metric, Version: version, Condition: condition, DurationNS: int64(duration), Commands: after})
	}
	writeReport := func() {
		output, err := json.MarshalIndent(map[string]any{"environment": versions, "samples": timings}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, output, 0600); err != nil {
			t.Fatal(err)
		}
	}
	defer writeReport()
	gitSource, sbxSource, workspaceSource := gitintegration.NewSource(), sbxintegration.NewSource(), workspace.NewSource(nil)
	registrySources := integration.NewRegistry(gitSource, sbxSource, workspaceSource)
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	publication := taskservice.New(store, logger, registrySources)
	service := cleanup.New([]integration.CleanupProvider{sbxSource, gitSource, workspaceSource})
	for sample := range 20 {
		condition := "warm-image-new-vm"
		if sample == 0 {
			condition = "first-provision-on-fresh-runner"
		}
		name := fmt.Sprintf("radar-perf-%s-%02d", namespace, sample)
		note := filepath.Join(base, name+".md")
		if err := os.WriteFile(note, nil, 0600); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(base, name+".log")
		t.Setenv("RADAR_LOG", logPath)
		var members []workspace.DesiredWorkspaceWorktree
		for i := range 4 {
			members = append(members, workspace.DesiredWorkspaceWorktree{Repository: repo, BranchMode: integration.WorkspaceBranchNew, Name: fmt.Sprintf("ABC-123-%02d-%d", sample, i), Base: "main"})
		}
		var created workspace.Workspace
		measure("workspace-create", "after", condition, func() {
			var err error
			created, err = workspace.Create(ctx, workspace.ExecRunner{}, workspace.CreateOptions{Name: name, WorkspaceRoot: root, PrepareOnly: true, Sandbox: true, SandboxKitName: kit, SandboxReadyCommand: []string{"sandbox-startup", "run"}, Worktrees: members, Note: &workspace.DesiredWorkspaceNote{Path: note, LinkingKey: "fixture:" + name}})
			if created.SandboxName != "" {
				runtimeName, anchor := created.SandboxName, created.Path
				t.Cleanup(func() { removeLiveRuntime(t, runtimeName, anchor) })
			}
			if err != nil {
				t.Fatal("disposable workspace provisioning failed; private provider output suppressed")
			}
		})
		appendLivePhases(t, logPath, condition, &timings)
		registry, err := workspacegroup.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		group, found := workspacegroup.FindByID(registry, workspacegroup.ID(created.Path))
		if !found {
			t.Fatal("created workspace missing")
		}
		for _, member := range group.Members {
			for i := range 200 {
				dir := filepath.Join(member.Path, "node_modules", fmt.Sprintf("dir-%03d", i))
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for j := range 100 {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%03d", j)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		if sample == 0 {
			entries := 0
			if err := filepath.WalkDir(created.Path, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if path != created.Path {
					entries++
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			versions["entries"] = entries
			for n := range 20 {
				snapshots := map[string]integration.WorkspaceContext{}
				versionsToRun := []struct{ label, binary string }{{"before", baseline}, {"after", current}}
				if n%2 == 1 {
					versionsToRun[0], versionsToRun[1] = versionsToRun[1], versionsToRun[0]
				}
				for _, version := range versionsToRun {
					var inspected integration.WorkspaceContext
					measure("workspace-context", version.label, "live-vm-warm-filesystem", func() {
						if err := json.Unmarshal([]byte(run("", version.binary, "workspace-context", "--workspace", created.Path, "--json")), &inspected); err != nil {
							t.Fatal(err)
						}
						if inspected.Sandbox == nil || inspected.Sandbox.RuntimeError != "" {
							t.Fatal("context did not verify a healthy runtime")
						}
					})
					snapshots[version.label] = inspected
					if version.label == "after" && timings[len(timings)-1].Commands["sbx-ls"] != 1 {
						t.Fatal("full context used more than one sandbox observation")
					}
					request, _ := json.Marshal(integration.WorkspaceReconcileRequest{Revision: inspected.Revision, Desired: inspected.Desired})
					measure("zero-change-preview", version.label, "live-vm-warm-filesystem", func() {
						run("", version.binary, "reconcile-workspace", "--workspace", created.Path, "--preview", "--request", string(request), "--json")
					})
				}
				if !reflect.DeepEqual(snapshots["before"], snapshots["after"]) {
					t.Fatal("live context changed result semantics between source versions")
				}
				measure("lifecycle-resources", "after", "live-vm-warm-filesystem", func() {
					run("", current, "workspace-context", "--resources-only", "--workspace", created.Path, "--json")
				})
				commands := timings[len(timings)-1].Commands
				if commands["sbx-ls"] != 1 || commands["sbx-ports"] != 0 {
					t.Fatal("lifecycle projection queried full context")
				}
			}
		}
		var task protocol.Task
		collect := func() {
			result := publication.Refresh(ctx, true)
			for _, candidate := range result.Tasks {
				for _, ref := range candidate.SourceRefs {
					if ref.Source == "workspace" && ref.WorkspaceID == group.ID {
						task = candidate
					}
				}
			}
		}
		measure("pre-cleanup-collection", "after", "large-four-member-fixture", collect)
		if len(task.SourceRefs) == 0 {
			t.Fatal("registered bundle was not collected")
		}
		phasePath := filepath.Join(base, name+"-cleanup.jsonl")
		phaseFile, err := os.Create(phasePath)
		if err != nil {
			t.Fatal(err)
		}
		timedCtx := phasetiming.WithLogger(ctx, slog.New(slog.NewJSONHandler(phaseFile, nil)))
		var preview protocol.CleanupPreview
		measure("cleanup-preview", "after", "large-four-member-fixture", func() {
			var err error
			preview, err = service.Preview(timedCtx, task, integration.CleanupConfirmed)
			if err != nil {
				t.Fatal("disposable cleanup preview failed")
			}
		})
		if timings[len(timings)-1].Commands["git-fetch"] != 1 {
			t.Fatal("preview did not deduplicate repository fetches")
		}
		measure("cleanup-deletion", "after", "large-four-member-fixture", func() {
			if _, err := service.Execute(timedCtx, preview, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed}); err != nil {
				t.Fatal("disposable cleanup failed; private provider output suppressed")
			}
		})
		phaseFile.Close()
		appendLiveJSONPhases(t, phasePath, &timings)
		measure("post-mutation-publication", "after", "no-global-collection", func() {
			publication.MarkResourceRefreshPending()
			if !publication.ResourceSnapshot().ResourceRefreshPending {
				t.Fatal("completed mutation returned a falsely fresh snapshot")
			}
		})
		measure("post-cleanup-collection", "after", "empty-workspace", func() { publication.Refresh(ctx, true) })
		if publication.ResourceSnapshot().ResourceRefreshPending {
			t.Fatal("fresh post-cleanup collection remained pending")
		}
		if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
			t.Fatal("cleanup retained the anchor")
		}
		if _, err := os.Stat(note); err != nil {
			t.Fatal("cleanup removed the canonical note")
		}
		writeReport()
	}
}

func installLiveCounters(t *testing.T, base, path string, providerEnvironment map[string]*string) {
	t.Helper()
	bin := filepath.Join(base, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git", "sbx"} {
		original, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("required provider %s is unavailable", name)
		}
		// Only fixed command names/counts enter the report, never args or output.
		body := "#!/bin/sh\n"
		if name == "sbx" {
			// Keep the runner's authenticated provider environment. Do not copy
			// credentials into the fixture or mount their original directories.
			for key, value := range providerEnvironment {
				if value == nil {
					body += "unset " + key + "\n"
				} else {
					body += "export " + key + "='" + strings.ReplaceAll(*value, "'", "'\\''") + "'\n"
				}
			}
		}
		if name == "git" {
			body += "if [ \"$1\" = fetch ]; then printf 'git-fetch\\n' >> \"$RADAR_PERF_COMMANDS\"; fi\n"
		} else {
			body += "case \"$1\" in ls|ports|version|rm|create|exec|publish|unpublish) printf 'sbx-%s\\n' \"$1\" >> \"$RADAR_PERF_COMMANDS\";; esac\n"
		}
		body += "exec '" + strings.ReplaceAll(original, "'", "'\\''") + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RADAR_PERF_COMMANDS", path)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
func readLiveCounters(path string) map[string]int {
	data, _ := os.ReadFile(path)
	result := map[string]int{}
	for _, name := range strings.Fields(string(data)) {
		result[name]++
	}
	return result
}
func appendLivePhases(t *testing.T, path, condition string, timings *[]liveTiming) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	phase := regexp.MustCompile(`phase=([a-z_]+)`)
	duration := regexp.MustCompile(`duration=([^\s]+)`)
	for _, line := range strings.Split(ansi.ReplaceAllString(string(data), ""), "\n") {
		p, d := phase.FindStringSubmatch(line), duration.FindStringSubmatch(line)
		if len(p) != 2 || len(d) != 2 {
			continue
		}
		elapsed, err := time.ParseDuration(d[1])
		if err != nil {
			t.Fatal("unreadable phase timing")
		}
		*timings = append(*timings, liveTiming{Metric: p[1], Version: "after", Condition: condition, DurationNS: int64(elapsed)})
	}
}
func appendLiveJSONPhases(t *testing.T, path string, timings *[]liveTiming) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var value struct {
			Phase    string `json:"phase"`
			Duration int64  `json:"duration"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if value.Phase != "" {
			*timings = append(*timings, liveTiming{Metric: value.Phase, Version: "after", Condition: "large-four-member-fixture", DurationNS: value.Duration})
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}
func removeLiveRuntime(t *testing.T, name, anchor string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "sbx", "ls", "--json").Output()
	if err != nil {
		t.Error("cannot verify disposable runtime cleanup")
		return
	}
	var rows []struct {
		Name       string   `json:"name"`
		Workspaces []string `json:"workspaces"`
		Mounts     []string `json:"mounts"`
	}
	if err := json.Unmarshal(output, &rows); err != nil {
		t.Error("cannot decode disposable runtime ownership")
		return
	}
	for _, row := range rows {
		if row.Name != name {
			continue
		}
		owned := false
		for _, mount := range append(row.Workspaces, row.Mounts...) {
			if mount == anchor {
				owned = true
			}
		}
		if !owned {
			t.Error("refusing to remove a runtime with changed ownership")
			return
		}
		if err := exec.CommandContext(ctx, "sbx", "rm", "--force", name).Run(); err != nil {
			t.Error("failed to remove owned disposable runtime")
		}
		return
	}
}
