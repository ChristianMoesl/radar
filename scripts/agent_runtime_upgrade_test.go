package scripts_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/client"
	"radar/internal/protocol"
)

// Exercise the public release reinstallation path with real compiled binaries,
// daemons and Node runners. Only the fixture export differs between compatible
// SDK revisions; there are no production version switches or state migrations.
func TestSuppliedRuntimeReleaseUpgradeRecoversCheckpoint(t *testing.T) {
	payload, err := os.ReadFile("../internal/agentruntime/payload/runtime.zip")
	if err != nil {
		if os.Getenv("RADAR_TEST_SUPPLIED_RUNTIME") == "1" {
			t.Fatal(err)
		}
		t.Skip("make agent-runtime is required for supplied-runtime upgrade qualification")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	put := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	run := func(command string, env []string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), command, args...)
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", command, args, err, output)
		}
		return output
	}
	archives := map[string]string{}
	for _, revision := range []string{"before", "after"} {
		archive := filepath.Join(dir, revision)
		archives[revision] = archive
		put(filepath.Join(archive, "install.sh"), bundledInstaller(t), 0700)
		for source, target := range map[string]string{
			"../LICENSE": "LICENSE", "../internal/pi/default-AGENTS.md": "share/radar/AGENTS.md",
			"install-agent-instructions.sh": "install-agent-instructions.sh",
		} {
			data, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			put(filepath.Join(archive, target), data, 0700)
		}
		for _, page := range []string{"man1/radar.1", "man5/radar-config.5"} {
			put(filepath.Join(archive, "share/man", page), []byte("fixture manual\n"), 0600)
		}
		// Go's overlay replaces only the embedded payload for this build, never
		// the checkout, installed dependencies, or an existing runtime cache.
		fixture := suppliedRevision(t, payload, revision)
		payloadPath := filepath.Join(archive, "runtime.zip")
		put(payloadPath, fixture, 0600)
		original, err := filepath.Abs("../internal/agentruntime/payload/runtime.zip")
		if err != nil {
			t.Fatal(err)
		}
		overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{original: payloadPath}})
		put(filepath.Join(archive, "overlay.json"), overlay, 0600)
		if err := os.MkdirAll(filepath.Join(archive, "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		run("go", os.Environ(), "build", "-overlay", filepath.Join(archive, "overlay.json"),
			"-ldflags", "-X radar/internal/version.Number=fixture-"+revision,
			"-o", filepath.Join(archive, "bin/radar"), "../cmd/radar")
	}
	home, prefix := filepath.Join(dir, "home"), filepath.Join(dir, "prefix")
	config, root := filepath.Join(dir, "config"), filepath.Join(dir, "agents")
	project := filepath.Join(dir, "project")
	entry := filepath.Join(project, "agent.ts")
	put(entry, []byte(suppliedUpgradeDefinition), 0600)
	configData, _ := json.Marshal(map[string]any{
		"workspace": map[string]any{"root_dir": filepath.Join(dir, "workspaces"), "auto_confirm": true},
		"obsidian":  map[string]any{"vault_path": filepath.Join(dir, "vault")},
		"github":    map[string]bool{"enabled": false}, "jira": map[string]bool{"enabled": false},
		"datadog": map[string]bool{"enabled": false}, "sbx": map[string]bool{"enabled": false},
	})
	put(filepath.Join(config, "radar/config.yaml"), configData, 0600)
	if err := os.MkdirAll(filepath.Join(dir, "vault"), 0700); err != nil {
		t.Fatal(err)
	}
	installEnv := append(filteredEnvironment("HOME", "PREFIX", "BINDIR", "MANDIR", "LIBEXECDIR", "XDG_CONFIG_HOME", "BASH_ENV", "ENV"),
		"HOME="+home, "PREFIX="+prefix, "XDG_CONFIG_HOME="+config, "PATH="+readyTools(t)+":"+os.Getenv("PATH"))
	install := func(revision string) {
		t.Helper()
		out := run("bash", installEnv, filepath.Join(archives[revision], "install.sh"))
		if strings.Contains(string(out), "radar-agent") {
			t.Fatalf("installer introduced a separate SDK action: %s", out)
		}
	}
	install("before")
	executable := filepath.Join(prefix, "bin/radar")
	// No inherited credentials, project packages, development runner override,
	// real package managers, or provider accounts enter the daemon fixture.
	tools := filepath.Join(dir, "tools")
	for _, tool := range []string{"node", "git"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(tools, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(tools, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"pi", "npm", "pnpm"} {
		put(filepath.Join(tools, tool), []byte("#!/bin/sh\nexit 1\n"), 0700)
	}
	// This fixture provisions a dedicated empty workspace, not a repository.
	put(filepath.Join(tools, "fd"), []byte("#!/bin/sh\nexit 0\n"), 0700)
	short, err := os.MkdirTemp("", "ru-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	socket := filepath.Join(short, "r.sock")
	env := []string{"HOME=" + home, "PATH=" + tools + ":/usr/bin:/bin", "XDG_CONFIG_HOME=" + config,
		"XDG_DATA_HOME=" + filepath.Join(dir, "data"), "XDG_STATE_HOME=" + filepath.Join(dir, "state"),
		"RADAR_AGENT_ROOT=" + root, "RADAR_SOCKET=" + socket, "RADAR_PID=" + filepath.Join(short, "r.pid"),
		"RADAR_STATE=" + filepath.Join(dir, "state.db"), "RADAR_DISABLE_COLLECTION=1", "RADAR_BINARY=" + executable,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	var daemon *exec.Cmd
	var daemonLog bytes.Buffer
	stopDaemon := func() {
		if daemon != nil {
			_ = daemon.Process.Signal(syscall.SIGTERM)
			_ = daemon.Wait()
			daemon = nil
		}
	}
	call := func(req protocol.AgentRequest) json.RawMessage {
		t.Helper()
		response, err := client.CallRequest(socket, protocol.Request{Method: "agent", Agent: &req})
		if err != nil || !response.OK {
			t.Fatalf("agent %s/%s: %v, %s", req.ID, req.Operation, err, response.Error)
		}
		return response.AgentResult
	}
	inspect := func(id string) protocol.AgentStatus {
		t.Helper()
		var status protocol.AgentStatus
		if err := json.Unmarshal(call(protocol.AgentRequest{ID: id, Operation: "inspect"}), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	until := func(label string, check func() bool) {
		t.Helper()
		for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
			if check() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", label)
	}
	startDaemon := func() {
		t.Helper()
		daemon = exec.Command(executable, "daemon")
		daemon.Env, daemon.Stdout, daemon.Stderr = env, &daemonLog, &daemonLog
		if err := daemon.Start(); err != nil {
			t.Fatal(err)
		}
		until("installed daemon ready", func() bool {
			response, err := client.CallWithTimeout(socket, "summary", time.Second)
			return err == nil && response.OK
		})
	}
	t.Cleanup(func() {
		if daemon != nil {
			for _, id := range []string{"recover", "stopped"} {
				_, _ = client.CallRequest(socket, protocol.Request{Method: "agent", Agent: &protocol.AgentRequest{ID: id, Operation: "stop"}})
			}
		}
		stopDaemon()
		// Also clean up an orphan if a fixture assertion failed between daemons.
		for _, id := range []string{"recover", "stopped"} {
			data, _ := os.ReadFile(filepath.Join(root, id, "pid"))
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if t.Failed() {
			t.Log(daemonLog.String())
			data, _ := os.ReadFile(filepath.Join(root, "recover", "runner.log"))
			t.Log(string(data))
		}
	})
	startDaemon()
	for _, id := range []string{"recover", "stopped"} {
		call(protocol.AgentRequest{Operation: "add", ID: id, Entrypoint: entry})
		call(protocol.AgentRequest{Operation: "start", ID: id})
	}
	call(protocol.AgentRequest{Operation: "stop", ID: "stopped"})
	event := protocol.AgentRequest{Operation: "emit", ID: "recover", Params: json.RawMessage(`{"source":"fixture","id":"one","payload":null}`)}
	receipt := call(event)
	old := inspect("recover")
	if old.Snapshot == nil {
		t.Fatal("missing running snapshot")
	}
	marker := func(pid int) string { return filepath.Join(root, "recover", fmt.Sprintf("waiting-%d", pid)) }
	until("before revision checkpoint", func() bool {
		data, _ := os.ReadFile(marker(old.Snapshot.PID))
		return string(data) == "before -> before"
	})
	preserved := map[string][]byte{}
	for _, id := range []string{"recover", "stopped"} {
		for _, name := range []string{"registration.json", "config.json", "ingress.token"} {
			path := filepath.Join(root, id, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			preserved[path] = data
		}
	}
	install("after")
	// Raw RPC deliberately avoids the new CLI's normal daemon-version restart.
	// Merely installing must neither replace a live Node owner nor its imports.
	current := inspect("recover")
	if current.Snapshot == nil || current.Snapshot.PID != old.Snapshot.PID {
		t.Fatal("installation hot-swapped the running owner")
	}
	if data, _ := os.ReadFile(marker(current.Snapshot.PID)); string(data) != "before -> before" {
		t.Fatal("installation changed the live runner's SDK")
	}
	stopDaemon()
	if err := syscall.Kill(old.Snapshot.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	startDaemon()
	// Existing desired-state supervision, not start/add or store reset, recovers
	// the unfinished phase using the new executable's supplied SDK.
	until("supervised recovery on the after revision", func() bool {
		status := inspect("recover")
		if status.Snapshot == nil || !status.Running || status.Snapshot.PID == old.Snapshot.PID {
			return false
		}
		data, _ := os.ReadFile(marker(status.Snapshot.PID))
		return string(data) == "before -> after"
	})
	put(filepath.Join(root, "recover", "release"), nil, 0600)
	until("retained checkpoint completed without replaying start", func() bool {
		status := inspect("recover")
		return status.Snapshot != nil && status.Snapshot.Presentation.Summary == "before -> after; starts=1; completed=1"
	})
	if again := call(event); !bytes.Equal(again, receipt) {
		t.Fatal("upgrade reset the durable admission receipt")
	}
	if stopped := inspect("stopped"); stopped.Desired || stopped.Running {
		t.Fatal("upgrade restarted an intentionally stopped agent")
	}
	for path, want := range preserved {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("upgrade changed retained registration/configuration/authentication: %s", filepath.Base(path))
		}
	}
	if runtimes, err := os.ReadDir(filepath.Join(root, ".runtime")); err != nil || len(runtimes) != 2 {
		t.Fatalf("expected both immutable supplied revisions to remain: %v, %v", runtimes, err)
	}
	if files, err := os.ReadDir(project); err != nil || len(files) != 1 || files[0].Name() != "agent.ts" {
		t.Fatalf("upgrade changed the definition project: %v, %v", files, err)
	}
}

func suppliedRevision(t *testing.T, payload []byte, revision string) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	changed := false
	for _, file := range reader.File {
		if file.Name != "dist/src/index.js" {
			if err := writer.Copy(file); err != nil {
				t.Fatal(err)
			}
			continue
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		header := file.FileHeader
		w, err := writer.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(w, "export const fixtureRevision = %q;\n%s", revision, data); err != nil {
			t.Fatal(err)
		}
		changed = true
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("SDK entrypoint missing from the real payload")
	}
	return buffer.Bytes()
}

const suppliedUpgradeDefinition = `
import { writeFile, access } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout } from "node:timers/promises";
import { fixtureRevision, createModels, createRegistry, defineDoc, defineTask, present } from "@christianmoesl/radar-agent";
import { radar } from "@christianmoesl/radar-agent/tools";
if (typeof radar !== "function") throw Error("SDK subpath unavailable");
let dataDir;
const Count = defineDoc({ kind: "fixture.upgrade-count", version: 1, scope: "session", initial: () => ({ starts: 0, completed: 0 }) });
const Work = defineTask({ name: "fixture.upgrade", version: 1, initial: () => ({ phase: "start" }), phases: {
  start: async (_task, runtime, context) => runtime.commit(async tx => {
    const doc = await tx.doc(Count); doc.starts++;
    return { status: "running", checkpoint: { phase: "finish", started: fixtureRevision } };
  }, context),
  finish: async (task, runtime, context) => {
    const revisions = task.state.checkpoint.started + " -> " + fixtureRevision;
    await writeFile(join(dataDir, "waiting-" + process.pid), revisions);
    while (true) { try { await access(join(dataDir, "release")); break; } catch {} await setTimeout(25, undefined, { signal: runtime.signal }); }
    await runtime.commit(async tx => {
      const doc = await tx.doc(Count); doc.completed++;
      await present(tx, { summary: revisions + "; starts=" + doc.starts + "; completed=" + doc.completed });
      return { status: "terminal", outcome: { status: "completed", result: doc.completed } };
    }, context);
  }
}, abort: async (_task, runtime, context) => runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context) });
export default { version: 1, configure: host => { dataDir = host.dataDir; return { models: createModels(), registry: createRegistry() }; }, eventTask: Work };
`
