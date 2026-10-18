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
