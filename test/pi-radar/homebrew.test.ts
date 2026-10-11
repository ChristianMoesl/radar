import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import test from "node:test";
// @ts-expect-error Plain Node release tooling has no TypeScript declarations.
import { smokeFormula } from "../../scripts/homebrew/smoke-formula.mjs";

const exec = promisify(execFile);
const root = fileURLToPath(new URL("../../", import.meta.url));
const digest = (bytes: string) => createHash("sha256").update(bytes).digest("hex");

test("release metadata records both Linux architectures without requiring a notifier", async t => {
  const dir = await mkdtemp(join(tmpdir(), "radar-linux-metadata-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  await mkdir(join(dir, "bin"));
  await writeFile(join(dir, "bin/radar"), "linux binary");
  for (const arch of ["amd64", "arm64"]) {
    const archive = join(dir, `radar_v1.2.3_linux_${arch}.tar.gz`);
    await writeFile(archive, `archive ${arch}`);
    await exec(process.execPath, ["scripts/release-metadata.mjs", "artifact", dir, archive], { cwd: root });
    assert.deepEqual(JSON.parse(await readFile(`${archive}.json`, "utf8")), {
      file: `radar_v1.2.3_linux_${arch}.tar.gz`, size: `archive ${arch}`.length,
      sha256: digest(`archive ${arch}`), binary_sha256: digest("linux binary"),
    });
  }
  const darwin = join(dir, "radar_v1.2.3_darwin_arm64.tar.gz");
  await writeFile(darwin, "darwin archive");
  await assert.rejects(exec(process.execPath, ["scripts/release-metadata.mjs", "artifact", dir, darwin], { cwd: root }));
  await mkdir(join(dir, "libexec/radar/RadarNotifier.app/Contents/MacOS"), { recursive: true });
  await writeFile(join(dir, "libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier"), "notifier", { mode: 0o755 });
  await exec(process.execPath, ["scripts/release-metadata.mjs", "artifact", dir, darwin], { cwd: root });
  const metadata = JSON.parse(await readFile(`${darwin}.json`, "utf8"));
  assert.equal(metadata.notifier_version, (await readFile(join(root, "macos/RadarNotifier/VERSION"), "utf8")).trim());
  assert.match(metadata.notifier_sha256, /^[a-f0-9]{64}$/);
});

test("local smoke formulas use the real template's platform hooks, never a publishable unsigned formula", async t => {
  const dir = await mkdtemp(join(tmpdir(), "radar-homebrew-smoke-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const fixture = await readFile(join(root, "scripts/homebrew/testdata/Formula/radar.rb"), "utf8");
  for (const arch of ["amd64", "arm64"]) {
    const archive = join(dir, `radar_v2.3.4_linux_${arch}.tar.gz`);
    await writeFile(archive, "native archive");
    const formula = smokeFormula(archive);
    assert.match(formula, /LOCAL CI SMOKE FIXTURE ONLY/);
    assert.match(formula, /version "2\.3\.4"/);
    assert.ok(formula.includes(`url "file://${archive}"`));
    assert.ok(formula.includes(digest("native archive")));
    assert.equal(formula.slice(formula.indexOf("  def install")), fixture.slice(fixture.indexOf("  def install")));
  }
  const invalid = join(dir, "radar_darwin_arm64.tar.gz");
  await writeFile(invalid, "wrong");
  assert.throws(() => smokeFormula(invalid), /Linux release archive/);
});

test("Linux Homebrew validation gates binary publication and npm staging", async () => {
  const workflow = await readFile(join(root, ".github/workflows/release.yml"), "utf8");
  const job = (name: string) => {
    const match = workflow.match(new RegExp(`\\n  ${name}:\\n([\\s\\S]*?)(?=\\n  [a-z][\\w-]*:\\n|$)`));
    assert.ok(match, `missing ${name} release job`);
    return match[1];
  };
  const linux = job("linux-homebrew");
  assert.match(linux, /needs: release/);
  assert.match(linux, /os: ubuntu-24\.04\n\s+arch: amd64/);
  assert.match(linux, /os: ubuntu-24\.04-arm\n\s+arch: arm64/);
  assert.match(linux, /release-metadata\.mjs verify-release dist/);
  assert.match(linux, /scripts\/homebrew\/smoke-test\.sh/);
  const publish = job("publish");
  assert.match(publish, /needs: \[release, linux-homebrew\]/);
  assert.match(publish, /release-metadata\.mjs verify-release dist/);
  assert.match(publish, /scripts\/publish-release\.sh/);
  assert.match(job("stage-npm"), /needs: publish/);

  const candidate = await readFile(join(root, ".github/workflows/homebrew.yml"), "utf8");
  assert.match(candidate, /os: \[macos-latest, ubuntu-24\.04, ubuntu-24\.04-arm\]/);
  for (const command of ["brew install --formula", "brew test", "brew uninstall"]) {
    assert.ok(candidate.includes(command), `candidate validation missing ${command}`);
  }
});
