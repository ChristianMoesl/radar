import assert from "node:assert/strict";
import { promisify } from "node:util";
import { execFile, type ExecFileOptions } from "node:child_process";
import {
  mkdtemp,
  mkdir,
  readFile,
  readdir,
  rm,
  writeFile,
  symlink,
  access,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

import { WorkspaceExecution } from "../src/environment.ts";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";

const command = promisify(execFile);

for (const { autoConfirm, mode } of [
  { autoConfirm: true, mode: "local" },
  { autoConfirm: false, mode: "local" },
  { autoConfirm: true, mode: "sbx" },
] as const)
  test(
    `review workspace and retained conversation (auto_confirm=${autoConfirm}, mode=${mode})`,
    {
      skip:
        !process.env.RADAR_BINARY ||
        (mode === "sbx" && !process.env.RADAR_SBX_KIT),
      timeout: mode === "sbx" ? 180000 : 60000,
    },
    async (testContext) => {
      const execute = (
        file: string,
        args: string[],
        options: Omit<ExecFileOptions, "encoding"> = {},
      ) =>
        command(file, args, {
          ...options,
          encoding: "utf8",
          timeout: 120000,
          signal: testContext.signal,
        });
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
        PATH: process.env.PATH,
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
      if (mode === "sbx") {
        // Radar's isolated XDG roots must not hide the real SBX login/daemon.
        // Route only SBX through its original environment; never copy secrets.
        const executable = (
          await execute("sh", ["-c", "command -v sbx"])
        ).stdout.trim();
        const quote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;
        const directories = [
          "XDG_CONFIG_HOME",
          "XDG_DATA_HOME",
          "XDG_STATE_HOME",
          "XDG_CACHE_HOME",
        ];
        const restore = directories
          .map((name) =>
            process.env[name] === undefined
              ? `unset ${name}`
              : `export ${name}=${quote(process.env[name]!)}`,
          )
          .join("\n");
        await mkdir(join(dir, "bin"));
        await writeFile(
          join(dir, "bin", "sbx"),
          `#!/bin/sh\n${restore}\nexec ${quote(executable)} "$@"\n`,
          { mode: 0o700 },
        );
        env.PATH = `${join(dir, "bin")}:${process.env.PATH}`;
      }
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
          sbx: {
            enabled: mode === "sbx",
            ...(mode === "sbx"
              ? {
                  kit: { name: process.env.RADAR_SBX_KIT },
                  additional_mounts: [repo],
                }
              : {}),
          },
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
          mode,
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
        if (mode === "sbx") {
          assert.equal((await execution.exec(["id", "-u"])).trim(), "1000");
          assert.equal(
            (
              await native.writeFile(
                "sbx-native.txt",
                "native SBX fixture\n",
                BACKGROUND_CONTEXT,
              )
            ).ok,
            true,
          );
          const file = await native.readTextFile(
            "sbx-native.txt",
            BACKGROUND_CONTEXT,
          );
          assert.equal(file.ok, true);
          if (file.ok) assert.equal(file.value, "native SBX fixture\n");
          assert.equal(
            await readFile(join(anchor.path, "sbx-native.txt"), "utf8"),
            "native SBX fixture\n",
          );
        }
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
              { env, signal: testContext.signal, timeout: 120000 },
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
        const deadline = Date.now() + (mode === "sbx" ? 90000 : 30000);
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
        assert.equal(status.workspace.mode, mode);
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
        if (mode === "sbx") {
          await execute("sbx", ["stop", registry.workspaces[0].sandbox.name], {
            env,
          });
          const sentinel = join(anchor.path, "must-not-run-on-host");
          await assert.rejects(
            execution.exec([
              "node",
              "-e",
              "require('fs').writeFileSync(process.argv[1], 'wrong')",
              sentinel,
            ]),
            /sandbox .*unavailable/i,
          );
          await assert.rejects(access(sentinel), { code: "ENOENT" });
          await assert.rejects(
            execution.readOnly(anchor.path, "sbx-native.txt"),
            /sandbox .*unavailable/i,
          );
          await assert.rejects(
            radar("agent", "restart", "reviewer"),
            /sandbox .*unavailable/i,
          );
        }
      } catch (error) {
        const log = await readFile(
          join(dir, "agents", "reviewer", "runner.log"),
          "utf8",
        ).catch(() => "");
        if (log)
          console.error(`Review fixture runner log:\n${log.slice(-16384)}`);
        throw error;
      } finally {
        process.env = originalEnvironment;
        // Cleanup needs its own deadline after the test's signal has aborted.
        await command(binary, ["agent", "stop", "reviewer", "--json"], {
          env,
          timeout: 30000,
        }).catch(() => {});
        await command(binary, ["stop", "--json"], {
          env,
          timeout: 30000,
        }).catch(() => {});
        if (mode === "sbx") {
          const registry = await readFile(
            join(workspaceRoot, ".radar-workspaces.json"),
            "utf8",
          )
            .then(JSON.parse)
            .catch(() => null);
          for (const workspace of registry?.workspaces ?? []) {
            if (!workspace.sandbox) continue;
            await command("sbx", ["rm", "--force", workspace.sandbox.name], {
              env,
              timeout: 30000,
            }).catch(() => {});
            await rm(workspace.sandbox.shared_directory, {
              recursive: true,
              force: true,
            });
          }
        }
        await rm(dir, { recursive: true, force: true });
      }
    },
  );
