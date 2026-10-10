import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type TestContext } from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("../../scripts/release-notes.mjs", import.meta.url));

function fixture(t: TestContext) {
  const root = mkdtempSync(join(tmpdir(), "radar-notes-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const options = {
    cwd: root, encoding: "utf8" as const, stdio: ["ignore", "pipe", "pipe"] as ["ignore", "pipe", "pipe"],
    env: {
      PATH: process.env.PATH, HOME: root, GIT_CONFIG_NOSYSTEM: "1", GIT_CONFIG_GLOBAL: "/dev/null",
      GIT_AUTHOR_NAME: "Fixture", GIT_COMMITTER_NAME: "Fixture",
      GIT_AUTHOR_EMAIL: "fixture@example.invalid", GIT_COMMITTER_EMAIL: "fixture@example.invalid",
      GIT_AUTHOR_DATE: "2020-01-01T12:01:23Z", GIT_COMMITTER_DATE: "2020-01-01T12:01:23Z",
    },
  };
  const git = (...args: string[]) => execFileSync("git", args, options).trim();
  git("init", "-q", "-b", "main");
  return {
    root, options, git,
    commit: (subject: string, body = "") => {
      git("commit", "--allow-empty", "-qm", subject, "-m", body);
      return git("rev-parse", "HEAD");
    },
    notes: (tag: string) => execFileSync(process.execPath, [script, tag], options),
  };
}

test("notes group direct commits, retain scopes, flag breaking changes and omit noise", (t) => {
  const f = fixture(t);
  f.commit("feat: old feature"); f.git("tag", "v1.0.0");
  const feature = f.commit("feat(workspaces): add previews");
  f.commit("fix: avoid duplicate entries");
  f.commit("perf: speed up startup");
  f.commit("refactor: simplify loading");
  f.commit("docs: explain installation");
  f.commit("Legacy change");
  for (const subject of ["test: add coverage", "ci: cache builds", "chore: release v1.1.0"]) f.commit(subject);
  f.commit("feat(config)!: require explicit paths");
  f.commit("chore: migrate state", "BREAKING CHANGE: Run the migration before upgrading.");
  f.commit("fix: update protocol", "Details.\n\nBREAKING-CHANGE: Old clients must upgrade.");
  f.commit("fix(ui): escape <script> and [links]*");
  f.git("tag", "v1.1.0");
  f.commit("feat: not in this release");
  const notes = f.notes("v1.1.0");
  assert.match(notes, /^## Breaking changes/);
  assert.match(notes, /config: require explicit paths/);
  assert.match(notes, /Run the migration before upgrading\./);
  assert.match(notes, /Old clients must upgrade\./);
  assert.match(notes, /## Features\n\n- workspaces: add previews/);
  assert.ok(notes.includes(`([${feature.slice(0, 7)}](https://github.com/ChristianMoesl/radar/commit/${feature}))`));
  assert.match(notes, /## Fixed\n\n- avoid duplicate entries/);
  assert.match(notes, /## Changes\n\n- speed up startup/);
  assert.match(notes, /simplify loading/);
  assert.match(notes, /explain installation/);
  assert.match(notes, /Legacy change/);
  assert.ok(notes.includes("ui: escape &lt;script&gt; and \\[links\\]\\*"));
  assert.doesNotMatch(notes, /old feature|add coverage|cache builds|release v1.1.0|not in this release/);
  assert.match(notes, /compare\/v1.0.0\.\.\.v1.1.0/);
  assert.equal(f.notes("v1.1.0"), notes);
});

test("stable notes skip prereleases, notifier tags and unrelated branches", (t) => {
  const f = fixture(t);
  f.commit("feat: initial"); f.git("tag", "-a", "v1.0.0", "-m", "stable");
  f.git("checkout", "-qb", "unrelated");
  f.commit("feat: unrelated"); f.git("tag", "v9.0.0");
  f.git("checkout", "main");
  f.commit("feat: first candidate"); f.git("tag", "v1.1.0-rc.1");
  f.commit("fix: second candidate"); f.git("tag", "v1.1.0-rc.2");
  const prerelease = f.notes("v1.1.0-rc.2");
  assert.match(prerelease, /compare\/v1.1.0-rc.1\.\.\.v1.1.0-rc.2/);
  assert.doesNotMatch(prerelease, /first candidate/);
  f.commit("fix: final fix"); f.git("tag", "notifier-v5.0.0"); f.git("tag", "v01.2.3");
  f.commit("chore: release v1.1.0"); f.git("tag", "v1.1.0");
  f.git("tag", "v1.1.0-rc.3"); // Same-commit aliases cannot become the baseline.
  const stable = f.notes("v1.1.0");
  assert.match(stable, /compare\/v1.0.0\.\.\.v1.1.0/);
  assert.match(stable, /first candidate/);
  assert.match(stable, /second candidate/);
  assert.match(stable, /final fix/);
  assert.doesNotMatch(stable, /unrelated|notifier|v01/);
});

test("first release includes root and ignores same-commit tags; empty sections are omitted", (t) => {
  const f = fixture(t);
  f.commit("feat: root feature"); f.git("tag", "v0.1.0"); f.git("tag", "v0.1.0-rc.1");
  const notes = f.notes("v0.1.0");
  assert.match(notes, /root feature/);
  assert.match(notes, /\/commits\/v0.1.0/);
  assert.doesNotMatch(notes, /## Fixed|## Changes|## Breaking/);
  f.commit("chore: release v0.1.1"); f.git("tag", "v0.1.1");
  assert.match(f.notes("v0.1.1"), /^No user-facing changes\./);
});

test("merge subjects are omitted but branch commits are included", (t) => {
  const f = fixture(t);
  f.commit("feat: initial"); f.git("tag", "v1.0.0");
  f.git("checkout", "-qb", "feature"); f.commit("feat: merged feature");
  f.git("checkout", "main"); f.commit("fix: direct fix");
  f.git("merge", "--no-ff", "feature", "-m", "Merge feature branch");
  f.git("tag", "v1.1.0");
  const notes = f.notes("v1.1.0");
  assert.match(notes, /merged feature/); assert.match(notes, /direct fix/);
  assert.doesNotMatch(notes, /Merge feature branch/);
});

test("invalid tags, missing tags and shallow history fail rather than publishing incomplete notes", (t) => {
  const f = fixture(t);
  f.commit("feat: initial"); f.git("tag", "v1.0.0");
  f.commit("fix: next"); f.git("tag", "v1.0.1");
  for (const tag of ["main", "--help", "v01.0.0", "v1.0.0+build", "v1.0.0-01", "v9.9.9"]) {
    assert.throws(() => f.notes(tag));
  }
  const shallow = join(f.root, "shallow");
  f.git("clone", "-q", "--depth=1", `file://${f.root}`, shallow);
  assert.throws(() => execFileSync(process.execPath, [script, "v1.0.1"], { ...f.options, cwd: shallow }), /full Git history/);
});
