package pi

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the real extension hooks with a fake Pi API. Only external package
// imports are stubbed; Node's filesystem, os.tmpdir(), and environment are real.
func TestRadarExtensionConfiguresSharedDirectoryOnStartupReloadAndSwitch(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the extension runtime test")
	}
	dir := t.TempDir()
	text := strings.ReplaceAll(extensionSource(t), `import { Type } from "typebox";`, `const Type = new Proxy({}, { get: () => () => ({}) });`)
	if err := os.WriteFile(filepath.Join(dir, "radar.ts"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	harness := `
import assert from 'node:assert/strict';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { mkdirSync, writeFileSync, readFileSync } from 'node:fs';
import radar from './radar.ts';
const root = process.cwd();
process.env.TMPDIR = root;
delete process.env.RADAR_HOST_TMPDIR;
process.env.XDG_CONFIG_HOME = join(root, 'config');
const first = join(root, 'first'), second = join(root, 'second');
mkdirSync(first); mkdirSync(second);
let context = { registered: true, workspace_path: root, members: [], sandbox: { shared_directory: first, shared_directory_ready: true } };
let fail = false;
const hooks = new Map();
const pi = {
 on: (name, fn) => hooks.set(name, fn), registerTool() {}, registerCommand() {}, appendEntry() {},
 exec: async () => { if (fail) throw Error('unavailable'); return { code: 0, stdout: JSON.stringify(context) }; },
};
radar(pi);
const ctx = { cwd: root, sessionManager: { getEntries: () => [] }, isProjectTrusted: () => true, ui: { notify() {} } };
await hooks.get('session_start')({}, ctx);
await hooks.get('resources_discover')({ cwd: root, reason: 'startup' }, ctx);
assert.equal(tmpdir(), first);
assert.equal(process.env.RADAR_HOST_TMPDIR, root);
// This is the same os.tmpdir + write pattern used by Pi's clipboard handler.
const clipboard = join(tmpdir(), 'pi-clipboard-fixture.png');
writeFileSync(clipboard, 'clipboard bytes');
assert.equal(readFileSync(join(first, 'pi-clipboard-fixture.png'), 'utf8'), 'clipboard bytes');
const prompt = await hooks.get('before_agent_start')({ systemPrompt: 'base' }, ctx);
assert.ok(prompt.systemPrompt.includes('Shared host/sandbox directory: ' + first));
assert.ok(prompt.systemPrompt.includes("Keep the sandbox's TMPDIR and /tmp local"));
context.sandbox.shared_directory = second;
await hooks.get('resources_discover')({ cwd: root, reason: 'reload' }, ctx);
assert.equal(tmpdir(), second);
assert.equal(process.env.RADAR_HOST_TMPDIR, root);
context.sandbox.shared_directory_ready = false;
await hooks.get('before_agent_start')({ systemPrompt: 'base' }, ctx);
assert.equal(tmpdir(), root);
context.sandbox.shared_directory_ready = true;
await hooks.get('resources_discover')({ cwd: root, reason: 'reload' }, ctx);
assert.equal(tmpdir(), second);
context = { workspace_path: root, members: [] };
await hooks.get('resources_discover')({ cwd: root, reason: 'switch' }, ctx);
assert.equal(tmpdir(), root);
process.env.TMPDIR = second;
fail = true;
await hooks.get('resources_discover')({ cwd: root, reason: 'reload' }, ctx);
assert.equal(tmpdir(), root);
`
	path := filepath.Join(dir, "test.mjs")
	if err := os.WriteFile(path, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--experimental-strip-types", path)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extension runtime test: %v\n%s", err, output)
	}
}
