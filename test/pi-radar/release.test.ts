import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
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
  await writeFile(join(root, "package.json"), JSON.stringify({ version }));
  const script = join(root, "scripts", "check-release-version.mjs");
  await writeFile(script, await readFile(join(repository, "scripts", "check-release-version.mjs")));
  return { root, script };
}

for (const version of ["0.1.0", "1.2.3", "1.2.3-rc.1", "1.2.3-beta", "1.2.3-0.alpha-1"]) {
  test(`release version accepts matching v${version}`, async t => {
    const { script } = await fixture(t, version);
    const { stdout } = await exec(process.execPath, [script, `v${version}`]);
    assert.match(stdout, /Validated release/);
  });
}

for (const tag of [undefined, "0.1.0", "v0.2.0", "v0.1.0-rc.1", "v0.1.0+build.1"]) {
  test(`release version rejects tag ${tag ?? "<missing>"}`, async t => {
    const { script } = await fixture(t, "0.1.0");
    await assert.rejects(exec(process.execPath, [script, ...(tag ? [tag] : [])]), error => {
      assert.match(String(error), /release tag must match package.json version/);
      return true;
    });
  });
}

for (const version of ["01.2.3", "1.02.3", "1.2.03", "1.2", "1.2.3-", "1.2.3-rc..1", "1.2.3-01", "1.2.3+build.1"]) {
  test(`release version rejects invalid package version ${version}`, async t => {
    const { script } = await fixture(t, version);
    await assert.rejects(exec(process.execPath, [script, `v${version}`]), error => {
      assert.match(String(error), /package.json version must be a release version/);
      return true;
    });
  });
}

test("local release validates package version before fetching, tagging or pushing", async t => {
  const { root } = await fixture(t, "0.1.0");
  await writeFile(join(root, "scripts", "release.sh"), await readFile(join(repository, "scripts", "release.sh")));
  const bin = join(root, "bin");
  await mkdir(bin);
  await writeFile(join(bin, "git"), `#!/bin/sh
if [ "$*" = "rev-parse --show-toplevel" ]; then
  printf '%s\\n' "$FIXTURE_ROOT"
else
  printf '%s\\n' "$*" >> "$FIXTURE_ROOT/git-calls"
  exit 1
fi
`, { mode: 0o755 });
  await writeFile(join(bin, "pnpm"), `#!/bin/sh
[ "$1" = check:release ] || exit 1
exec node "$FIXTURE_ROOT/scripts/check-release-version.mjs" "$2"
`, { mode: 0o755 });
  await assert.rejects(exec("bash", [join(root, "scripts", "release.sh"), "v0.2.0"], {
    cwd: root,
    env: { PATH: `${bin}:${dirname(process.execPath)}:/usr/bin:/bin`, FIXTURE_ROOT: root },
  }), error => {
    assert.match(String(error), /release tag must match package.json version/);
    return true;
  });
  await assert.rejects(readFile(join(root, "git-calls")), { code: "ENOENT" });
});
