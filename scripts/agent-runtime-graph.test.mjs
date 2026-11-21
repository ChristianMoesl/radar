import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, readdir, realpath, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import test from "node:test";
import { copyProductionGraph } from "./agent-runtime-graph.mjs";

// Do not use recursive readdir: Node versions can follow directory symlinks,
// including the dependency cycles that this fixture deliberately preserves.
async function tree(root, path = "") {
  const result = [];
  for (const entry of await readdir(join(root, path), { withFileTypes: true })) {
    const name = join(path, entry.name);
    result.push(name);
    if (entry.isDirectory()) result.push(...await tree(root, name));
  }
  return result.sort();
}

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), "radar-production-graph-"));
  const source = join(root, "agent-host"), output = join(root, "output"), store = join(root, "node_modules", ".pnpm");
  await mkdir(source); await mkdir(store, { recursive: true }); await mkdir(output);
  const manifest = (dir, data) => writeFile(join(dir, "package.json"), JSON.stringify(data));
  async function pkg(id, name, data = {}) {
    const dir = join(store, id, "node_modules", name);
    await mkdir(dir, { recursive: true });
    await manifest(dir, { name, version: id, ...data });
    await writeFile(join(dir, "index.js"), "export const fixture = true;");
    return dir;
  }
  async function link(parent, name, target) {
    const path = join(parent, "node_modules", name);
    await mkdir(dirname(path), { recursive: true });
    await symlink(relative(dirname(path), target), path);
  }
  return { root, source, output, store, manifest, pkg, link, close: () => rm(root, { recursive: true, force: true }) };
}

test("production traversal retains cycles and exact peer variants without dev packages or registry metadata", async () => {
  const f = await fixture();
  try {
    await f.manifest(f.source, { dependencies: { alpha: "1" }, devDependencies: { development: "1" } });
    const alpha = await f.pkg("alpha-1", "alpha", { dependencies: { beta: "1" }, peerDependencies: { gamma: "*" },
      optionalDependencies: { absent: "1" }, devDependencies: { development: "1" } });
    const beta = await f.pkg("beta-1", "beta", { dependencies: { alpha: "1" }, peerDependencies: { gamma: "*" } });
    const gamma1 = await f.pkg("gamma-1", "gamma"), gamma2 = await f.pkg("gamma-2", "gamma");
    const dev = await f.pkg("development-1", "development");
    for (const [parent, name, target] of [[f.source, "alpha", alpha], [f.source, "development", dev],
      [alpha, "beta", beta], [alpha, "gamma", gamma1], [alpha, "development", dev],
      [beta, "alpha", alpha], [beta, "gamma", gamma2]]) await f.link(parent, name, target);
    const before = await tree(f.root);
    await copyProductionGraph(f.source, f.output, f.store);
    const suppliedAlpha = await realpath(join(f.output, "node_modules", "alpha"));
    const suppliedBeta = await realpath(join(suppliedAlpha, "node_modules", "beta"));
    assert.equal(await realpath(join(suppliedBeta, "node_modules", "alpha")), suppliedAlpha);
    for (const [parent, want] of [[suppliedAlpha, "gamma-1"], [suppliedBeta, "gamma-2"]]) {
      const metadata = JSON.parse(await readFile(join(parent, "node_modules", "gamma", "package.json"), "utf8"));
      assert.equal(metadata.version, want);
    }
    const files = await tree(f.output);
    assert.ok(!files.some(name => name.includes("development") || name.includes("absent")));
    assert.deepEqual((await tree(f.root)).filter(name => !name.startsWith("output/")), before);
  } finally { await f.close(); }
});

test("missing required packages and external-store dependencies fail without installing or borrowing another graph", async () => {
  for (const external of [false, true]) {
    const f = await fixture();
    try {
      await f.manifest(f.source, { dependencies: { alpha: "1" } });
      if (external) await f.link(f.source, "alpha", f.output);
      await assert.rejects(copyProductionGraph(f.source, f.output, f.store),
        external ? /outside workspace store/ : /Missing installed production dependency/);
    } finally { await f.close(); }
  }
});
