import assert from "node:assert/strict";
import { promisify } from "node:util";
import { execFile } from "node:child_process";
import {
  mkdtemp,
  mkdir,
  readFile,
  readdir,
  rm,
  writeFile,
  symlink,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

import { WorkspaceExecution } from "../src/environment.ts";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";

const execute = promisify(execFile);

for (const autoConfirm of [true, false])
  test(
    `review workspace and retained conversation (auto_confirm=${autoConfirm})`,
    { skip: !process.env.RADAR_BINARY, timeout: 60000 },
    async () => {
      const binary = process.env.RADAR_BINARY!;
      const dir = await mkdtemp(join(tmpdir(), "radar-review-"));
      const repo = join(dir, "repo"),
        workspaceRoot = join(dir, "workspaces"),
        vault = join(dir, "vault");
      await mkdir(repo);
      await mkdir(vault);
      await mkdir(join(dir, "config", "radar"), { recursive: true });
      const env = {
        ...process.env,
        XDG_CONFIG_HOME: join(dir, "config"),
        XDG_DATA_HOME: join(dir, "data"),
        XDG_STATE_HOME: join(dir, "state"),
        RADAR_AGENT_ROOT: join(dir, "agents"),
        RADAR_SOCKET: join(dir, "r.sock"),
        RADAR_STATE: join(dir, "tasks.json"),
        RADAR_PID: join(dir, "radar.pid"),
        RADAR_AGENT_HOST: resolve(import.meta.dirname, "../src/runner.ts"),
        RADAR_DISABLE_COLLECTION: "1",
        RADAR_BINARY: binary,
        GIT_CONFIG_NOSYSTEM: "1",
        GIT_CONFIG_GLOBAL: "/dev/null",
        TMUX: "",
      };
      const git = async (...args: string[]) =>
        (await execute("git", args, { cwd: repo, env })).stdout.trim();
      const radar = async (...args: string[]) =>
        JSON.parse(
          (
            await execute(binary, [...args, "--json"], {
              env,
              maxBuffer: 1024 * 1024,
            })
          ).stdout,
        );
      await writeFile(
        join(dir, "config", "radar", "config.yaml"),
        JSON.stringify({
          workspace: { root_dir: workspaceRoot, auto_confirm: autoConfirm },
          repository_dirs: [dir],
          obsidian: { vault_path: vault },
          github: { enabled: false },
          jira: { enabled: false },
          datadog: { enabled: false },
          sbx: { enabled: false },
        }),
      );
      await git("init", "--initial-branch=main");
      await writeFile(join(repo, "app.txt"), "original\n");
      await git("add", ".");
      await git(
        "-c",
        "user.name=Fixture",
        "-c",
        "user.email=fixture@example.invalid",
        "commit",
        "-m",
        "base",
      );
      const base = await git("rev-parse", "HEAD");
      await writeFile(join(repo, "app.txt"), "changed\n");
      await git("add", ".");
      await git(
        "-c",
        "user.name=Fixture",
        "-c",
        "user.email=fixture@example.invalid",
        "commit",
        "-m",
        "change",
      );
      const head = await git("rev-parse", "HEAD");
      const config = join(dir, "review.json");
      await writeFile(
        config,
        JSON.stringify({
          fixture: true,
          reviewer: "reviewer",
          repositories: { "owner/project": repo },
        }),
      );
      const originalEnvironment = process.env;
      try {
        process.env = { ...env };
        const initialPlan = await radar(
          "create",
          "--prepare-only",
          "--repo",
          repo,
          "--base",
          head,
          "--name",
          "review-agent",
          "--preview",
        );
        const anchor = await radar(
          "create",
          "--plan-id",
          initialPlan.plan_id,
          "--prepared-note",
          JSON.stringify(initialPlan.note),
          "--prepare-only",
          "--repo",
          repo,
          "--base",
          head,
          "--name",
          "review-agent",
        );
        await mkdir(join(anchor.path, "skills", "review"), { recursive: true });
        await writeFile(
          join(anchor.path, "skills", "review", "SKILL.md"),
          await readFile(
            resolve(import.meta.dirname, "../examples/review/SKILL.md"),
          ),
        );
        await radar(
          "agent",
          "add",
          resolve(import.meta.dirname, "../examples/review.ts"),
          "--id",
          "reviewer",
          "--workspace",
          anchor.path,
          "--config",
          config,
        );
        await radar("agent", "start", "reviewer");
        const execution = new WorkspaceExecution({
          id: "fixture",
          path: anchor.path,
          mode: "local",
        });
        assert.match(
          await execution.readOnly(anchor.path, "skills/review/SKILL.md"),
          /review/i,
        );
        await symlink(repo, join(anchor.path, "escape"));
        await assert.rejects(
          execution.readOnly(anchor.path, "escape/app.txt"),
          /outside selected read root/,
        );
        const native = execution.native(anchor.path);
        const reader = await native.openBinaryReader(
          "skills/review/SKILL.md",
          undefined,
          BACKGROUND_CONTEXT,
        );
        assert.equal(reader.ok, true);
        if (reader.ok) {
          assert.equal(
            (
              await reader.value.scanLines(
                { startLine: 0, endLine: 2 },
                BACKGROUND_CONTEXT,
              )
            ).ok,
            true,
          );
          await reader.value.close(BACKGROUND_CONTEXT);
        }

        const payload = {
          type: "github.webhook",
          eventType: "pull_request",
          body: {
            action: "review_requested",
            number: 7,
            repository: { full_name: "owner/project" },
            requested_reviewer: { login: "reviewer" },
            pull_request: {
              head: { sha: head },
              base: { sha: base },
              html_url: "https://github.com/owner/project/pull/7",
            },
          },
        };
        const { spawn } = await import("node:child_process");
        const deliver = () =>
          new Promise<any>((done, reject) => {
            const child = spawn(
              binary,
              [
                "agent",
                "emit",
                "reviewer",
                "--source",
                "github.webhook",
                "--event-id",
                "delivery-1",
                "--json",
              ],
              { env },
            );
            let out = "",
              error = "";
            child.stdout.on("data", (b) => (out += b));
            child.stderr.on("data", (b) => (error += b));
            child.on("error", reject);
            child.on("close", (code) =>
              code ? reject(new Error(error)) : done(JSON.parse(out)),
            );
            child.stdin.end(JSON.stringify(payload));
          });
        const receipt = await deliver();
        let status;
        let approved = false;
        let approvalRevision = -1;
        const deadline = Date.now() + 30000;
        do {
          status = await radar("agent", "inspect", "reviewer");
          if (
            status.snapshot?.presentation.actions.some(
              (a: any) => a.id === "prepare-review",
            )
          ) {
            assert.equal(autoConfirm, false);
            if (approved) {
              assert.equal(
                status.snapshot.presentation.revision,
                approvalRevision,
                "unexpected renewed confirmation",
              );
              await new Promise((done) => setTimeout(done, 100));
              continue;
            }
            assert.match(status.snapshot.presentation.summary, /"plan_id"/);
            const revision = status.snapshot.presentation.revision;
            await assert.rejects(
              radar(
                "agent",
                "action",
                "reviewer",
                "prepare-review",
                "--revision",
                String(revision - 1),
                "--event-id",
                "stale-decision",
              ),
              /interaction changed/,
            );
            await radar(
              "agent",
              "action",
              "reviewer",
              "prepare-review",
              "--revision",
              String(revision),
              "--event-id",
              "approve-review",
            );
            approved = true;
            approvalRevision = revision;
          } else if (status.snapshot?.presentation.attention) break;
          const latest = status.snapshot?.recent?.find(
            (t: any) => t.kind === "example.review",
          );
          if (latest?.outcome?.status === "faulted")
            throw new Error(JSON.stringify(latest.outcome));
          await new Promise((done) => setTimeout(done, 100));
        } while (Date.now() < deadline);
        assert.equal(
          status.snapshot.presentation.attention,
          true,
          JSON.stringify(status),
        );
        assert.match(
          status.snapshot.presentation.summary,
          /Fixture analysis completed/,
        );
        assert.equal(status.snapshot.presentation.workspaces.length, 1);
        assert.equal(status.snapshot.presentation.conversations.length, 1);
        const workspace = status.snapshot.presentation.workspaces[0].path;
        const registry = JSON.parse(
          await readFile(join(workspaceRoot, ".radar-workspaces.json"), "utf8"),
        );
        assert.equal(registry.workspaces.length, 1);
        assert.equal(status.workspace.path, anchor.path);
        assert.equal(status.workspace.mode, "local");
        assert.equal(workspace, anchor.path);
        assert.equal(
          (
            await execute("git", ["rev-parse", "HEAD"], {
              cwd: registry.workspaces[0].members.find((m: any) =>
                m.branch.startsWith("review-owner"),
              ).path,
              env,
            })
          ).stdout.trim(),
          head,
        );
        assert.deepEqual(await deliver(), receipt);
        const pid = status.snapshot.pid;
        await radar("restart");
        assert.equal(
          (await radar("agent", "inspect", "reviewer")).snapshot.pid,
          pid,
          "daemon restart replaced a live runner",
        );
        await radar("reset");
        await radar("agent", "restart", "reviewer");
        status = await radar("agent", "inspect", "reviewer");
        assert.equal(status.snapshot.presentation.conversations.length, 1);
        assert.equal(
          (await readdir(workspaceRoot, { withFileTypes: true })).filter((e) =>
            e.isDirectory(),
          ).length,
          1,
        );
        assert.equal(
          await readFile(join(repo, "app.txt"), "utf8"),
          "changed\n",
        );
      } finally {
        process.env = originalEnvironment;
        await radar("agent", "stop", "reviewer").catch(() => {});
        await radar("stop").catch(() => {});
        await rm(dir, { recursive: true, force: true });
      }
    },
  );
