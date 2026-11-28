import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const exec = promisify(execFile);
const repository = fileURLToPath(new URL("../../", import.meta.url));

async function fixture(t: { after: (cleanup: () => Promise<void>) => void }, version: string) {
  const root = await mkdtemp(join(tmpdir(), "radar-release-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(join(root, "scripts"));
  await mkdir(join(root, "agent-host"));
  await writeFile(join(root, "agent-host/package.json"), JSON.stringify({ name: "@christianmoesl/radar-agent", version }));
  await mkdir(join(root, "extensions/pi-radar"), { recursive: true });
  await writeFile(join(root, "extensions/pi-radar/version.ts"), `export const radarPackageVersion = "${version}";\n`);
  await writeFile(join(root, "package.json"), JSON.stringify({ version }));
  const script = join(root, "scripts", "release-version.mjs");
  await writeFile(script, await readFile(join(repository, "scripts", "release-version.mjs")));
  return { root, script };
}

for (const version of ["0.1.0", "1.2.3", "1.2.3-rc.1", "1.2.3-beta", "1.2.3-0.alpha-1"]) {
  test(`release version accepts matching v${version}`, async t => {
    const { script } = await fixture(t, version);
    const { stdout } = await exec(process.execPath, [script, "check", `v${version}`]);
    assert.match(stdout, /Validated release/);
  });
}

for (const tag of [undefined, "0.1.0", "v0.2.0", "v0.1.0-rc.1", "v0.1.0+build.1"]) {
  test(`release version rejects tag ${tag ?? "<missing>"}`, async t => {
    const { script } = await fixture(t, "0.1.0");
    await assert.rejects(exec(process.execPath, [script, "check", ...(tag ? [tag] : [])]), error => {
      assert.match(String(error), /release tag must match package.json version/);
      return true;
    });
  });
}

for (const version of ["01.2.3", "1.02.3", "1.2.03", "1.2", "1.2.3-", "1.2.3-rc..1", "1.2.3-01", "1.2.3+build.1"]) {
  test(`release version rejects invalid package version ${version}`, async t => {
    const { script } = await fixture(t, version);
    await assert.rejects(exec(process.execPath, [script, "check", `v${version}`]), error => {
      assert.match(String(error), /package.json version must be a release version/);
      return true;
    });
  });
}

test("local release rejects malformed versions before fetching, tagging or pushing", async t => {
  for (const tag of ["v0.2.0-01", "v01.2.3", "v1.02.3", "v1.2.03", "v1.2.3-rc..1"]) {
    await t.test(tag, async t => {
      const h = await releaseInputFixture(t);
      await assert.rejects(h.run(tag), error => {
        assert.match(String(error), /release version must be vX.Y.Z/);
        return true;
      });
      // Trace the REAL Git executable, not an unrecorded rev-parse exception
      // in a mock. Invalid input must not enter even repository discovery.
      await assert.rejects(readFile(h.trace), { code: "ENOENT" });
      assert.equal(await readFile(join(h.root, "package.json"), "utf8"), JSON.stringify({ version: "0.1.0" }));
    });
  }
});

async function releaseInputFixture(t: Parameters<typeof fixture>[0]) {
  const { root } = await fixture(t, "0.1.0");
  const script = join(root, "scripts", "release.sh");
  await writeFile(script, await readFile(join(repository, "scripts", "release.sh")));
  const trace = join(root, "git-trace");
  return {
    root, trace,
    run: (tag: string) => exec("bash", [script, tag], {
      cwd: root,
      env: {
        PATH: `${dirname(process.execPath)}:/usr/bin:/bin`, HOME: root,
        GIT_TRACE: trace, GIT_CEILING_DIRECTORIES: dirname(root),
        GIT_CONFIG_NOSYSTEM: "1", GIT_CONFIG_GLOBAL: "/dev/null", GIT_TERMINAL_PROMPT: "0",
      },
    }),
  };
}

for (const tag of ["v0.0.0", "v1.2.3", "v1.2.3-rc.1", "v1.2.3-0.alpha-1"]) {
  test(`valid release input ${tag} reaches read-only repository discovery`, async t => {
    const h = await releaseInputFixture(t);
    // The fixture has NO repository, credentials or remote: a valid version
    // can only reach discovery, never a real release/tag/push boundary.
    await assert.rejects(h.run(tag), error => {
      assert.match(String(error), /not a git repository/);
      assert.doesNotMatch(String(error), /release version must/);
      return true;
    });
    assert.match(await readFile(h.trace, "utf8"), /git rev-parse --show-toplevel/);
    assert.doesNotMatch(await readFile(h.trace, "utf8"), /git (fetch|tag|push|commit)/);
  });
}

test("CI owns native archives and gates publication on authentication and platform validation", async () => {
  const workflow = await readFile(join(repository, ".github", "workflows", "release.yml"), "utf8");
  const job = (name: string) => {
    const marker = `\n  ${name}:\n`;
    assert.ok(workflow.includes(marker), `missing release job: ${name}`);
    return workflow.split(marker)[1].split(/\n  [\w-]+:\n/)[0];
  };
  const build = job("release");
  assert.match(build, /runs-on: macos-latest/);
  assert.match(build, /run: make test/);
  assert.match(build, /run: scripts\/prepare-release-notifier\.sh/);
  assert.match(build, /run: make dist VERSION="\$\{GITHUB_REF_NAME\}".*NOTIFIER_ARTIFACT_DIR=/);
  assert.match(build, /run: node scripts\/release-metadata\.mjs release dist/);
  const linux = job("linux-homebrew");
  assert.match(linux, /needs: release/);
  for (const arch of ["amd64", "arm64"]) assert.ok(linux.includes(`arch: ${arch}`));
  assert.match(linux, /run: node scripts\/release-metadata\.mjs verify-release/);
  assert.match(linux, /run: scripts\/homebrew\/smoke-test\.sh/);
  const publish = job("publish");
  assert.match(publish, /needs: \[release, linux-homebrew\]/);
  assert.match(publish, /run: node scripts\/release-metadata\.mjs verify-release/);
  assert.match(publish, /run: scripts\/publish-release\.sh/);
  assert.match(job("stage-npm"), /needs: publish/);
});

async function stagingFixture(t: Parameters<typeof fixture>[0], version: string) {
  const { root } = await fixture(t, version);
  const workflow = await readFile(join(repository, ".github", "workflows", "release.yml"), "utf8");
  assert.match(workflow, /npm install --global "npm@\^11\.15\.0"/);
  const lines = workflow.split("\n");
  const step = lines.indexOf("      - name: Stage using npm trusted publishing");
  assert.ok(step >= 0, "the release workflow must stage the npm artifact");
  const run = lines.indexOf("        run: |", step);
  assert.ok(run > step);
  const shell: string[] = [];
  for (const line of lines.slice(run + 1)) {
    if (!line.startsWith("          ")) break;
    shell.push(line.slice(10));
  }
  assert.ok(shell.length > 0);
  const calls = join(root, "calls.jsonl");
  const summary = join(root, "summary.md");
  const bin = join(root, "bin");
  await mkdir(bin);
  await writeFile(join(bin, "pnpm"), `#!/usr/bin/env node
const { appendFileSync, writeFileSync } = require('node:fs');
const args = process.argv.slice(2);
appendFileSync(process.env.FIXTURE_CALLS, JSON.stringify({command: 'pnpm', args}) + '\\n');
if (process.env.FAIL_PACK || args[0] !== '--dir' || args[1] !== process.env.NPM_PACKAGE_DIRECTORY || args[2] !== 'pack' || args[3] !== '--out') process.exit(1);
writeFileSync(args[4], 'fixture tarball');
`, { mode: 0o755 });
  await writeFile(join(bin, "npm"), `#!/usr/bin/env node
const { appendFileSync, existsSync } = require('node:fs');
const args = process.argv.slice(2);
appendFileSync(process.env.FIXTURE_CALLS, JSON.stringify({command: 'npm', args}) + '\\n');
if (process.env.FAIL_STAGE || args[0] !== 'stage' || args[1] !== 'publish' || !existsSync(args[2])) process.exit(1);
`, { mode: 0o755 });
  const env = {
    PATH: `${bin}:${dirname(process.execPath)}:/usr/bin:/bin`,
    RUNNER_TEMP: root, GITHUB_STEP_SUMMARY: summary, FIXTURE_CALLS: calls,
    NPM_PACKAGE_NAME: "@christianmoesl/pi-radar", NPM_PACKAGE_DIRECTORY: ".", NPM_PACKAGE_ARTIFACT: "pi-radar.tgz",
  };
  return {
    root, summary,
    run: (extra: Record<string, string> = {}) => exec("bash", ["-e", "-u", "-o", "pipefail", "-c", shell.join("\n")], {
      cwd: root, env: { ...env, ...extra },
    }),
    calls: async () => (await readFile(calls, "utf8")).trim().split("\n").map(line => JSON.parse(line)),
  };
}

for (const [name, directory, artifact] of [["@christianmoesl/pi-radar", ".", "pi-radar.tgz"], ["@christianmoesl/radar-agent", "agent-host", "radar-agent.tgz"]]) {
  for (const [version, tag] of [["0.1.1", "latest"], ["0.2.0-rc.1", "next"]]) {
    test(`npm release stages ${name}@${version} for ${tag} and requests human approval`, async t => {
      const staging = await stagingFixture(t, version);
      await staging.run({ NPM_PACKAGE_NAME: name, NPM_PACKAGE_DIRECTORY: directory, NPM_PACKAGE_ARTIFACT: artifact });
      const tarball = join(staging.root, artifact);
      assert.deepEqual(await staging.calls(), [
        { command: "pnpm", args: ["--dir", directory, "pack", "--out", tarball] },
        { command: "npm", args: ["stage", "publish", tarball, "--access", "public", "--tag", tag] },
      ]);
      const summary = await readFile(staging.summary, "utf8");
      assert.ok(summary.includes(`${name}@${version}`));
      assert.ok(summary.includes(`staged for \`${tag}\``));
      assert.match(summary, /not publicly published/);
      assert.match(summary, /approve with 2FA/);
    });
  }
}

test("npm release does not stage anything if packing fails", async t => {
  const staging = await stagingFixture(t, "0.1.1");
  await assert.rejects(staging.run({ FAIL_PACK: "1" }));
  assert.deepEqual((await staging.calls()).map(call => call.command), ["pnpm"]);
  await assert.rejects(readFile(staging.summary), { code: "ENOENT" });
});

test("failed npm staging fails the job without claiming approval is ready", async t => {
  const staging = await stagingFixture(t, "0.1.1");
  await assert.rejects(staging.run({ FAIL_STAGE: "1" }));
  assert.deepEqual((await staging.calls()).map(call => call.command), ["pnpm", "npm"]);
  await assert.rejects(readFile(staging.summary), { code: "ENOENT" });
});

for (const moduleVersion of ["0.1.0", "0.2.0"]) {
  test(`preparation synchronizes all copies, including a partial bump from ${moduleVersion}`, async t => {
    const { root, script } = await fixture(t, moduleVersion);
    const manifest = { name: "fixture", version: "0.2.0", scripts: { test: "unchanged" }, pi: { extensions: ["entry.ts"] } };
    await writeFile(join(root, "package.json"), JSON.stringify(manifest, null, 4) + "\n");
    await exec(process.execPath, [script, "prepare", "v0.2.0"]);
    assert.deepEqual(JSON.parse(await readFile(join(root, "package.json"), "utf8")), manifest);
    assert.equal(await readFile(join(root, "package.json"), "utf8"), JSON.stringify(manifest, null, 4) + "\n");
    await exec(process.execPath, [script, "check", "v0.2.0"]);
    await exec(process.execPath, [script, "prepare", "v0.3.0-rc.1"]);
    assert.deepEqual(JSON.parse(await readFile(join(root, "package.json"), "utf8")), { ...manifest, version: "0.3.0-rc.1" });
    await exec(process.execPath, [script, "check", "v0.3.0-rc.1"]);
  });
}

for (const version of ["v01.2.3", "v1.02.3", "v1.2.03", "v1.2", "v1.2.3-", "v1.2.3-rc..1", "v1.2.3-01", "v1.2.3+build.1", "1.2.3"]) {
  test(`preparation rejects ${version} without changing either file`, async t => {
    const { root, script } = await fixture(t, "0.1.0");
    const paths = [join(root, "package.json"), join(root, "agent-host/package.json"), join(root, "extensions/pi-radar/version.ts")];
    const before = await Promise.all(paths.map(path => readFile(path, "utf8")));
    await assert.rejects(exec(process.execPath, [script, "prepare", version]));
    assert.deepEqual(await Promise.all(paths.map(path => readFile(path, "utf8"))), before);
  });
}

test("a malformed module or symlink cannot cause a partial version rewrite", async t => {
  const { root, script } = await fixture(t, "0.1.0");
  const manifest = await readFile(join(root, "package.json"), "utf8");
  const module = join(root, "extensions/pi-radar/version.ts");
  await writeFile(module, "not a version declaration\n");
  await assert.rejects(exec(process.execPath, [script, "prepare", "v0.2.0"]));
  assert.equal(await readFile(join(root, "package.json"), "utf8"), manifest);
  await rm(module);
  await symlink(join(root, "package.json"), module);
  await assert.rejects(exec(process.execPath, [script, "prepare", "v0.2.0"]), /regular file/);
  assert.equal(await readFile(join(root, "package.json"), "utf8"), manifest);
});

test("CI check remains read-only and rejects a stale loaded-version constant", async t => {
  const { root, script } = await fixture(t, "0.1.0");
  await writeFile(join(root, "package.json"), JSON.stringify({ version: "0.2.0" }));
  await assert.rejects(exec(process.execPath, [script, "check", "v0.2.0"]), /loaded-version reporting/);
  assert.match(await readFile(join(root, "extensions/pi-radar/version.ts"), "utf8"), /"0\.1\.0"/);
});

test("the SDK must use the shared version even when its implementation is unchanged", async t => {
  const { root, script } = await fixture(t, "0.2.0");
  const path = join(root, "agent-host/package.json");
  const agent = { name: "@christianmoesl/radar-agent", version: "0.1.0", exports: { ".": "./dist/src/index.js", "./tools": "./dist/src/tools.js" } };
  await writeFile(path, JSON.stringify(agent, null, 4) + "\n");
  const before = await readFile(path, "utf8");
  await assert.rejects(exec(process.execPath, [script, "check", "v0.2.0"]), /shared release version/);
  assert.equal(await readFile(path, "utf8"), before);
  await exec(process.execPath, [script, "prepare", "v0.2.0"]);
  assert.deepEqual(JSON.parse(await readFile(path, "utf8")), { ...agent, version: "0.2.0" });
  await exec(process.execPath, [script, "check", "v0.2.0"]);
  const aligned = await readFile(path, "utf8");
  await exec(process.execPath, [script, "prepare", "v0.2.0"]);
  assert.equal(await readFile(path, "utf8"), aligned);
});

for (const invalid of ["null", "[]", "{", '{"name":"unexpected","version":"0.1.0"}']) {
  test(`invalid SDK manifest ${invalid} cannot partially bump the other versions`, async t => {
    const { root, script } = await fixture(t, "0.1.0");
    await writeFile(join(root, "agent-host/package.json"), invalid);
    const paths = ["package.json", "agent-host/package.json", "extensions/pi-radar/version.ts"].map(path => join(root, path));
    const before = await Promise.all(paths.map(path => readFile(path, "utf8")));
    await assert.rejects(exec(process.execPath, [script, "prepare", "v0.2.0"]));
    assert.deepEqual(await Promise.all(paths.map(path => readFile(path, "utf8"))), before);
  });
}

test("a symlinked or missing SDK manifest fails before any version write", async t => {
  const { root, script } = await fixture(t, "0.1.0");
  const agent = join(root, "agent-host/package.json");
  const before = await readFile(join(root, "package.json"), "utf8");
  await rm(agent);
  await assert.rejects(exec(process.execPath, [script, "prepare", "v0.2.0"]));
  await symlink(join(root, "package.json"), agent);
  await assert.rejects(exec(process.execPath, [script, "prepare", "v0.2.0"]), /regular file/);
  assert.equal(await readFile(join(root, "package.json"), "utf8"), before);
  assert.match(await readFile(join(root, "extensions/pi-radar/version.ts"), "utf8"), /"0\.1\.0"/);
});

test("both packages stage independently from the same immutable release commit, never a manual second workflow", async () => {
  const workflow = await readFile(join(repository, ".github/workflows/release.yml"), "utf8");
  const stage = workflow.split("\n  stage-npm:\n")[1];
  assert.match(stage, /fail-fast: false/);
  assert.match(stage, /package: '@christianmoesl\/pi-radar'\n\s+directory: '\.'/);
  assert.match(stage, /package: '@christianmoesl\/radar-agent'\n\s+directory: agent-host/);
  assert.match(stage, /ref: \$\{\{ github.sha \}\}/);
  assert.match(stage, /contents: read\n\s+id-token: write/);
  assert.match(stage, /check:release "\$\{GITHUB_REF_NAME\}"/);
  assert.doesNotMatch(stage, /(?:secrets\.(?:NODE_AUTH_TOKEN|NPM_TOKEN)|(?:NODE_AUTH_TOKEN|NPM_TOKEN):|npm publish|npm stage approve)/);
  await assert.rejects(readFile(join(repository, ".github/workflows/agent-host-package.yml")), { code: "ENOENT" });
});
