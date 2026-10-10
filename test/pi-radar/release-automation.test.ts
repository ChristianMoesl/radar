import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test, type TestContext } from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const exec = promisify(execFile);
const source = fileURLToPath(new URL("../../", import.meta.url));

// Real Git commits/refs in disposable repositories only. Pushes go to a bare
// directory in the fixture, never the user's remote. Git signing is substituted;
// gh, package installation and builds cannot access the network or user config.
async function fixture(t: TestContext, manifestVersion = "0.1.0", moduleVersion = "0.1.0") {
  const root = await mkdtemp(join(tmpdir(), "radar-release-git-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const repo = join(root, "repo"), origin = join(root, "origin.git"), bin = join(root, "bin");
  await mkdir(repo); await mkdir(bin);
  const realGit = (await exec("sh", ["-c", "command -v git"], { env: { PATH: process.env.PATH } })).stdout.trim();
  const env = {
    HOME: root, PATH: `${bin}:${dirname(process.execPath)}:/usr/bin:/bin`,
    GIT_CONFIG_NOSYSTEM: "1", GIT_CONFIG_GLOBAL: "/dev/null", GIT_TERMINAL_PROMPT: "0",
    GIT_AUTHOR_NAME: "Fixture", GIT_COMMITTER_NAME: "Fixture",
    GIT_AUTHOR_EMAIL: "fixture@example.invalid", GIT_COMMITTER_EMAIL: "fixture@example.invalid",
    GIT_AUTHOR_DATE: "2020-01-01T12:01:23Z", GIT_COMMITTER_DATE: "2020-01-01T12:01:23Z",
    REAL_GIT: realGit, CALLS: join(root, "calls"), MODE: "", NOTIFIER_REF: "",
  };
  const git = async (...args: string[]) => (await exec(realGit, args, { cwd: repo, env })).stdout.trim();
  for (const script of ["release.sh", "release-version.mjs", "notifier-release-state.mjs"]) {
    await mkdir(join(repo, "scripts"), { recursive: true });
    await writeFile(join(repo, "scripts", script), await readFile(join(source, "scripts", script)));
  }
  await mkdir(join(repo, "extensions/pi-radar"), { recursive: true });
  await mkdir(join(repo, "macos/RadarNotifier"), { recursive: true });
  await writeFile(join(repo, "macos/RadarNotifier/VERSION"), "1.2.3\n");
  await writeFile(join(repo, "package.json"), JSON.stringify({ name: "fixture", version: manifestVersion }) + "\n");
  await writeFile(join(repo, "extensions/pi-radar/version.ts"), `export const radarPackageVersion = "${moduleVersion}";\n`);
  await writeFile(join(repo, "README.md"), "fixture\n");
  await git("init", "-q", "-b", "main");
  await git("add", "."); await git("commit", "-qm", "fixture");
  await git("tag", "-a", "notifier-v1.2.3", "-m", "existing immutable component");
  env.NOTIFIER_REF = await git("rev-parse", "refs/tags/notifier-v1.2.3");
  await git("init", "--bare", "-q", origin);
  await git("remote", "add", "origin", origin);
  await git("push", "-q", "origin", "main", "refs/tags/notifier-v1.2.3");

  const scripts = {
    git: `#!/bin/sh
printf 'git %s\n' "$*" >> "$CALLS"
if [ "$MODE" = status-error ] && [ "$1" = status ]; then exit 128; fi
if [ "$MODE" = remote-error ] && [ "$1" = ls-remote ]; then exit 128; fi
if [ "$MODE" = commit-error ] && [ "$1" = commit ]; then exit 7; fi
if [ "$1" = tag ] && [ "$2" = -s ]; then
  shift 2
  exec "$REAL_GIT" tag -a "$@"
fi
exec "$REAL_GIT" "$@"
`,
    gh: `#!/bin/sh
if [ "$MODE" = edit-during-preflight ]; then printf '{"version":"0.9.0","user":"edit"}\\n' > package.json; fi
case "$*" in
 'api repos/ChristianMoesl/radar/releases --paginate --slurp') printf '[[{"id":1,"tag_name":"notifier-v1.2.3","draft":false,"published_at":"2020-01-01T00:00:00Z"}]]\n';;
 'api repos/ChristianMoesl/radar/git/ref/tags/notifier-v1.2.3') printf '{"object":{"sha":"%s"}}\n' "$NOTIFIER_REF";;
 *) exit 99;;
esac
`,
    pnpm: `#!/bin/sh
printf 'pnpm %s\n' "$*" >> "$CALLS"
case "$1" in
 check:release) exec node scripts/release-version.mjs check "$2";;
 install|check) ;;
 *) exit 99;;
esac
`,
    make: `#!/bin/sh
printf 'make %s\n' "$*" >> "$CALLS"
case "$1" in test|dist) ;; *) exit 99;; esac
if [ "$MODE" = "$1-error" ]; then exit 9; fi
if [ "$1" = dist ]; then
 case "$MODE" in
 dirty-during-validation) echo changed >> README.md;;
 head-during-validation) "$REAL_GIT" commit --allow-empty -qm concurrent;;
 esac
fi
`,
  };
  for (const [name, body] of Object.entries(scripts)) await writeFile(join(bin, name), body, { mode: 0o755 });
  return {
    repo, origin, git,
    run: (mode = "", version = "v0.2.0") => exec("/bin/bash", ["scripts/release.sh", version], { cwd: repo, env: { ...env, MODE: mode }, timeout: 20000 }),
    calls: () => readFile(env.CALLS, "utf8"),
    remote: (ref: string) => git("--git-dir", origin, "rev-parse", ref),
  };
}

for (const [manifest, module, expectedCommits] of [["0.1.0", "0.1.0", 2], ["0.2.0", "0.1.0", 2], ["0.2.0", "0.2.0", 1]] as const) {
  test(`one release command prepares ${manifest}/${module} and atomically publishes the tested commit`, async t => {
    const h = await fixture(t, manifest, module);
    const notifier = await h.remote("refs/tags/notifier-v1.2.3");
    await h.run();
    const head = await h.git("rev-parse", "HEAD");
    assert.equal(await h.git("rev-list", "--count", "HEAD"), String(expectedCommits));
    assert.equal(await h.git("status", "--porcelain"), "");
    assert.equal(await h.remote("refs/heads/main"), head);
    assert.equal(await h.remote("refs/tags/v0.2.0^{}"), head);
    assert.equal(await h.remote("refs/tags/notifier-v1.2.3"), notifier);
    assert.equal(JSON.parse(await h.git("show", `${head}:package.json`)).version, "0.2.0");
    assert.match(await h.git("show", `${head}:extensions/pi-radar/version.ts`), /"0\.2\.0"/);
    if (expectedCommits === 2) {
      assert.equal(await h.git("log", "-1", "--format=%s"), "chore: release v0.2.0");
      const files = (await h.git("diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")).split("\n");
      assert.ok(files.every(file => ["package.json", "extensions/pi-radar/version.ts"].includes(file)));
    }
    const calls = await h.calls();
    assert.ok(calls.includes(`make dist VERSION=v0.2.0 COMMIT=${head.slice(0, 12)}`));
    assert.ok(calls.includes(`git tag -s v0.2.0 ${head} -m v0.2.0`));
    assert.ok(calls.includes(`git push --atomic origin ${head}:refs/heads/main refs/tags/notifier-v1.2.3 refs/tags/v0.2.0`));
    assert.equal(calls.match(/^git push /gm)?.length, 1);
    assert.doesNotMatch(calls, /git tag -s notifier-/);
  });
}

test("a clean main ahead of origin is included without a separate manual push", async t => {
  const h = await fixture(t);
  await h.git("commit", "--allow-empty", "-qm", "feature");
  await h.run();
  assert.equal(await h.git("rev-list", "--count", "HEAD"), "3");
  assert.equal(await h.remote("refs/heads/main"), await h.git("rev-parse", "HEAD"));
});

for (const state of ["dirty", "staged", "untracked", "dirty-version", "wrong-branch", "behind", "diverged", "used-local-tag", "used-remote-tag", "invalid", "remote-error", "status-error"]) {
  test(`preflight ${state} stops before preparing or committing versions`, async t => {
    const h = await fixture(t);
    if (["dirty", "staged"].includes(state)) {
      await writeFile(join(h.repo, "README.md"), "user edit\n");
      if (state === "staged") await h.git("add", "README.md");
    }
    if (state === "untracked") await writeFile(join(h.repo, "untracked.txt"), "user edit\n");
    if (state === "dirty-version") await writeFile(join(h.repo, "package.json"), '{"version":"0.3.0"}\n');
    if (state === "wrong-branch") await h.git("checkout", "-qb", "feature");
    if (["behind", "diverged"].includes(state)) {
      await h.git("checkout", "-qb", "remote-work");
      await h.git("commit", "--allow-empty", "-qm", "remote advance");
      await h.git("push", "origin", "HEAD:refs/heads/main");
      await h.git("checkout", "-q", "main");
      if (state === "diverged") await h.git("commit", "--allow-empty", "-qm", "local divergence");
    }
    if (state.startsWith("used-")) {
      await h.git("tag", "v0.2.0");
      if (state === "used-remote-tag") { await h.git("push", "origin", "refs/tags/v0.2.0"); await h.git("tag", "-d", "v0.2.0"); }
    }
    const before = await readFile(join(h.repo, "package.json"), "utf8");
    const head = await h.git("rev-parse", "HEAD");
    await assert.rejects(h.run(state, state === "invalid" ? "v0.2.0-01" : "v0.2.0"));
    assert.equal(await readFile(join(h.repo, "package.json"), "utf8"), before);
    assert.equal(await h.git("rev-parse", "HEAD"), head);
    assert.doesNotMatch(await h.calls(), /^git (commit|tag|push) /m);
  });
}

for (const mode of ["test-error", "dist-error"]) {
  test(`${mode} retains a local version commit and retries without duplicating it`, async t => {
    const h = await fixture(t);
    const original = await h.remote("refs/heads/main");
    await assert.rejects(h.run(mode));
    const head = await h.git("rev-parse", "HEAD");
    assert.notEqual(head, original);
    assert.equal(await h.remote("refs/heads/main"), original);
    assert.equal(await h.git("status", "--porcelain"), "");
    assert.doesNotMatch(await h.calls(), /^git (tag|push) /m);
    await h.run();
    assert.equal(await h.git("rev-parse", "HEAD"), head);
    assert.equal(await h.remote("refs/heads/main"), head);
  });
}

for (const mode of ["commit-error", "dirty-during-validation", "head-during-validation"]) {
  test(`${mode} never publishes unvalidated content`, async t => {
    const h = await fixture(t);
    const original = await h.remote("refs/heads/main");
    await assert.rejects(h.run(mode));
    assert.equal(await h.remote("refs/heads/main"), original);
    assert.doesNotMatch(await h.calls(), /^git (tag|push) /m);
  });
}

test("edits made during network preflight are preserved and never committed", async t => {
  const h = await fixture(t);
  const head = await h.git("rev-parse", "HEAD");
  await assert.rejects(h.run("edit-during-preflight"), /repository changed during preflight/);
  assert.equal(JSON.parse(await readFile(join(h.repo,"package.json"),"utf8")).user, "edit");
  assert.equal(await h.git("rev-parse", "HEAD"), head);
  assert.doesNotMatch(await h.calls(), /^git (commit|tag|push) /m);
});

test("an atomic push rejection leaves remote main and release tags untouched", async t => {
  const h = await fixture(t);
  const original = await h.remote("refs/heads/main");
  await writeFile(join(h.origin, "hooks/update"), '#!/bin/sh\n[ "$1" != refs/heads/main ]\n', { mode: 0o755 });
  await assert.rejects(h.run());
  assert.equal(await h.remote("refs/heads/main"), original);
  await assert.rejects(h.remote("refs/tags/v0.2.0"));
  assert.equal((await h.calls()).match(/^git push /gm)?.length, 1);
});
