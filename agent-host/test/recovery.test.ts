import assert from "node:assert/strict";
import { spawn, type ChildProcess } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, readFile, rm, writeFile, chmod } from "node:fs/promises";
import { createServer, connect } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

async function rpc(
  path: string,
  method: string,
  params: unknown = {},
): Promise<any> {
  return new Promise((done, reject) => {
    const socket = connect(path);
    let buffer = "",
      settled = false;
    socket.setTimeout(5000, () => {
      reject(new Error("RPC timeout"));
      socket.destroy();
    });
    socket.on("error", reject);
    socket.on("close", () => {
      if (!settled) reject(new Error("response lost"));
    });
    socket.on("connect", () =>
      socket.write(
        JSON.stringify({ jsonrpc: "2.0", id: 1, method, params }) + "\n",
      ),
    );
    socket.on("data", (chunk) => {
      buffer += chunk;
      if (!buffer.includes("\n")) return;
      settled = true;
      const response = JSON.parse(buffer.split("\n")[0]!);
      socket.end();
      if (response.error) reject(new Error(response.error.message));
      else done(response.result);
    });
  });
}
async function until(
  body: () => Promise<boolean>,
  deadlineMs = 10000,
): Promise<void> {
  const deadline = Date.now() + deadlineMs;
  while (Date.now() < deadline) {
    if (await body()) return;
    await new Promise((done) => setTimeout(done, 25));
  }
  throw new Error("condition timed out");
}

test(
  "SIGKILL recovery resumes a checkpoint and a lost receipt can be retried",
  { timeout: 30000 },
  async () => {
    const dir = await mkdtemp(join(tmpdir(), "radar-recovery-")),
      socketPath = join(dir, "agent.sock"),
      config = join(dir, "config.json");
    await writeFile(config, "{}");
    const helper = join(dir, "radar-helper");
    await writeFile(
      helper,
      `#!/bin/sh\nprintf '%s\\n' '${JSON.stringify({ id: "fixture", path: dir, mode: "local" })}'\n`,
    );
    await chmod(helper, 0o700);
    let child: ChildProcess | undefined;
    const start = async () => {
      child = spawn(
        process.execPath,
        [
          resolve(import.meta.dirname, "../src/runner.ts"),
          "--id",
          "fixture",
          "--entrypoint",
          resolve(import.meta.dirname, "fixtures/recovery.ts"),
          "--data-dir",
          dir,
          "--socket",
          socketPath,
          "--config",
          config,
          "--workspace",
          dir,
          "--workspace-id",
          "fixture",
        ],
        {
          env: { ...process.env, RADAR_BINARY: helper },
          stdio: ["ignore", "ignore", "pipe"],
        },
      );
      let errors = "";
      child.stderr?.on("data", (data) => (errors += data));
      await until(async () => {
        try {
          return (await rpc(socketPath, "hello")).instanceId === "fixture";
        } catch {
          if (child?.exitCode !== null) throw new Error(errors);
          return false;
        }
      });
    };
    try {
      await start();
      const wire = JSON.parse(
        await readFile(
          resolve(import.meta.dirname, "fixtures/protocol.json"),
          "utf8",
        ),
      );
      assert.equal(
        (await rpc(socketPath, wire.hello.request.method)).protocol,
        wire.protocol,
      );
      const first = await rpc(socketPath, "deliver", {
        source: "fixture",
        id: "one",
        payload: null,
      });
      await until(async () => {
        try {
          return (await readFile(join(dir, "started"), "utf8")) === "finish";
        } catch {
          return false;
        }
      });
      child!.kill("SIGKILL");
      await once(child!, "exit");
      await start();
      await until(
        async () =>
          (await rpc(socketPath, "snapshot")).presentation.summary ===
          "1 completed",
      );
      assert.equal(
        (
          await rpc(socketPath, "deliver", {
            source: "fixture",
            id: "one",
            payload: null,
          })
        ).taskId,
        first.taskId,
      );

      let admitted: any;
      const proxyPath = join(dir, "proxy.sock");
      const proxy = createServer((client) => {
        const worker = connect(socketPath);
        client.on("data", (data) => worker.write(data));
        worker.on("data", (data) => {
          admitted = JSON.parse(data.toString()).result;
          client.destroy();
          worker.destroy();
        });
        worker.on("error", () => client.destroy());
      });
      proxy.listen(proxyPath);
      await once(proxy, "listening");
      try {
        await assert.rejects(
          rpc(proxyPath, "deliver", {
            source: "fixture",
            id: "two",
            payload: { test: "lost-response" },
          }),
          /response lost/,
        );
      } finally {
        await new Promise<void>((done) => proxy.close(() => done()));
      }
      const again = await rpc(socketPath, "deliver", {
        source: "fixture",
        id: "two",
        payload: { test: "lost-response" },
      });
      assert.equal(again.taskId, admitted.taskId);
      await until(
        async () =>
          (await rpc(socketPath, "snapshot")).presentation.summary ===
          "2 completed",
      );
      await assert.rejects(rpc(socketPath, "unknown"), /unknown method/);
    } finally {
      if (child && child.exitCode === null) {
        child.kill("SIGKILL");
        await once(child, "exit").catch(() => {});
      }
      await rm(dir, { recursive: true, force: true });
    }
  },
);

test(
  "Renovate reconciles a comment accepted immediately before SIGKILL",
  { timeout: 30000 },
  async () => {
    const dir = await mkdtemp(join(tmpdir(), "radar-tag-recovery-"));
    const socketPath = join(dir, "agent.sock");
    const config = join(dir, "config.json");
    const helper = join(dir, "radar-helper.mjs");
    const binding = { id: "fixture", path: dir, mode: "local" };
    await writeFile(
      config,
      JSON.stringify({
        renovateAuthor: "renovate[bot]",
        comment: "@review-agent please review this PR.",
        includeExisting: false,
      }),
    );
    // The helper is the workspace execution boundary. It persists GitHub's side
    // of the effect, then withholds the POST response until the runner is killed.
    await writeFile(
      helper,
      `#!/usr/bin/env node
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
const dir = ${JSON.stringify(dir)}, args = process.argv.slice(2);
if (args.includes("--inspect")) { console.log(JSON.stringify(${JSON.stringify(binding)})); process.exit(0); }
const argv = args.slice(args.indexOf("--")+1), endpoint = argv[2];
const comments = JSON.parse(await readFile(join(dir,"comments.json"),"utf8").catch(()=>"[]"));
let value;
if (argv.includes("POST")) {
  value = {id:9, body:argv.at(-1).replace(/^body=/,""), html_url:"https://github.com/acme/app/issues/1#issuecomment-9"};
  comments.push(value);
  await writeFile(join(dir,"comments.json"),JSON.stringify(comments));
  await writeFile(join(dir,"posted"),String(comments.length));
  await writeFile(join(dir,"helper-pid"),String(process.pid));
  await new Promise(done=>setTimeout(done,30000));
} else if(endpoint.includes("/comments")) value = comments;
else value = {node_id:"PR_1",state:"open",user:{login:"renovate[bot]"}};
console.log(JSON.stringify({output:JSON.stringify(value)}));
`,
    );
    await chmod(helper, 0o700);
    let child: ChildProcess | undefined;
    const start = async () => {
      child = spawn(
        process.execPath,
        [
          resolve(import.meta.dirname, "../src/runner.ts"),
          "--id",
          "renovate",
          "--entrypoint",
          resolve(import.meta.dirname, "../examples/renovate.ts"),
          "--data-dir",
          dir,
          "--socket",
          socketPath,
          "--config",
          config,
          "--workspace",
          dir,
          "--workspace-id",
          "fixture",
        ],
        {
          env: { ...process.env, RADAR_BINARY: helper },
          stdio: ["ignore", "ignore", "pipe"],
        },
      );
      let errors = "";
      child.stderr?.on("data", (data) => {
        errors += data;
      });
      await until(async () => {
        try {
          return (await rpc(socketPath, "hello")).instanceId === "renovate";
        } catch {
          if (child?.exitCode !== null) throw new Error(errors);
          return false;
        }
      });
    };
    const event = {
      source: "radar.collection.github",
      id: "detected-1",
      payload: {
        change: "discovered",
        baseline: false,
        data: {
          type: "github.pull_request",
          nodeId: "PR_1",
          repository: "acme/app",
          number: 1,
          author: "renovate",
          authorType: "Bot",
          state: "OPEN",
          url: "https://github.com/acme/app/pull/1",
        },
      },
    };
    try {
      await start();
      const receipt = await rpc(socketPath, "deliver", event);
      await until(
        async () =>
          (await readFile(join(dir, "posted"), "utf8").catch(() => "")) === "1",
      );
      child!.kill("SIGKILL");
      await once(child!, "exit");
      const helperPID = Number(await readFile(join(dir, "helper-pid"), "utf8"));
      process.kill(helperPID, "SIGKILL");
      await start();
      await until(async () => {
        const snapshot = await rpc(socketPath, "snapshot");
        return (
          snapshot.pending === 0 &&
          snapshot.presentation.summary.includes("issuecomment-9")
        );
      });
      assert.equal(
        (await rpc(socketPath, "snapshot")).presentation.attention,
        false,
      );
      assert.equal(
        (await rpc(socketPath, "deliver", event)).taskId,
        receipt.taskId,
      );
      await rpc(socketPath, "deliver", {
        ...event,
        id: "detected-2",
        payload: { ...event.payload, change: "changed" },
      });
      await until(
        async () => (await rpc(socketPath, "snapshot")).pending === 0,
      );
      assert.equal(
        await readFile(join(dir, "posted"), "utf8"),
        "1",
        "recovery posted a duplicate mention",
      );
    } finally {
      if (child && child.exitCode === null) {
        child.kill("SIGKILL");
        await once(child, "exit").catch(() => {});
      }
      const helperPID = Number(
        await readFile(join(dir, "helper-pid"), "utf8").catch(() => "0"),
      );
      if (helperPID > 0) {
        try {
          process.kill(helperPID, "SIGKILL");
        } catch {}
      }
      await rm(dir, { recursive: true, force: true });
    }
  },
);
