#!/usr/bin/env node
import { createServer, type Socket } from "node:net";
import { chmod, readFile, unlink, writeFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import { AgentHost } from "./host.ts";
import { MAX_FRAME_BYTES, PROTOCOL_VERSION, RpcError } from "./index.ts";
import { radar } from "./tools.ts";
import type { WorkspaceBinding } from "./environment.ts";

const args = process.argv.slice(2);
function arg(name: string): string {
  const at = args.indexOf(name);
  if (at < 0 || !args[at + 1]) throw new Error(`missing ${name}`);
  return args[at + 1]!;
}

async function main(): Promise<void> {
  process.umask(0o077);
  const dataDir = resolve(arg("--data-dir"));
  const config = JSON.parse(await readFile(arg("--config"), "utf8"));
  const workspace = await radar<WorkspaceBinding>([
    "workspace-exec",
    "--workspace",
    arg("--workspace"),
    "--inspect",
  ]);
  if (workspace.id !== arg("--workspace-id"))
    throw new Error("associated workspace identity changed");
  const host = await AgentHost.open(resolve(arg("--entrypoint")), {
    id: arg("--id"),
    dataDir,
    config,
    workspace,
  });
  const socketPath = arg("--socket");
  // Only the storage owner removes a stale socket.
  await unlink(socketPath).catch((error) => {
    if (error.code !== "ENOENT") throw error;
  });
  const clients = new Set<Socket>();
  let closing = false;
  const shutdown = async () => {
    if (closing) return;
    closing = true;
    const deadline = setTimeout(() => process.exit(1), 5000);
    deadline.unref();
    server.close();
    for (const socket of clients) socket.destroy();
    await host.close();
    await unlink(socketPath).catch(() => {});
    process.exit(0);
  };
  const server = createServer((socket) => {
    clients.add(socket);
    let buffer = Buffer.alloc(0),
      watching = false,
      sending = false,
      active = 0;
    const send = (value: unknown) => {
      const line = JSON.stringify(value) + "\n";
      if (
        Buffer.byteLength(line) > MAX_FRAME_BYTES ||
        socket.writableLength > MAX_FRAME_BYTES * 2
      ) {
        socket.destroy();
        return;
      }
      if (!socket.destroyed) socket.write(line);
    };
    // Snapshots coalesce updates and never retain an unbounded token stream.
    const timer = setInterval(async () => {
      if (!watching || sending || socket.writableLength > MAX_FRAME_BYTES)
        return;
      sending = true;
      try {
        send({
          jsonrpc: "2.0",
          method: "snapshot",
          params: await host.snapshot(),
        });
      } catch {
        socket.destroy();
      } finally {
        sending = false;
      }
    }, 1000);
    socket.on("close", () => {
      clearInterval(timer);
      clients.delete(socket);
    });
    socket.on("error", () => {});
    socket.on("data", (chunk) => {
      buffer = Buffer.concat([buffer, chunk]);
      if (buffer.length > MAX_FRAME_BYTES) {
        socket.destroy();
        return;
      }
      let at: number;
      while ((at = buffer.indexOf(10)) >= 0) {
        const line = buffer.subarray(0, at).toString("utf8");
        buffer = buffer.subarray(at + 1);
        if (active >= 32) {
          socket.destroy();
          return;
        }
        active++;
        void (async () => {
          let id: unknown = null;
          try {
            let request;
            try {
              request = JSON.parse(line);
            } catch {
              throw new RpcError(-32700, "invalid JSON");
            }
            id = request?.id ?? null;
            if (typeof id !== "string" && typeof id !== "number") id = null;
            if (
              request?.jsonrpc !== "2.0" ||
              typeof request.method !== "string" ||
              !(typeof id === "string" || typeof id === "number")
            )
              throw new RpcError(-32600, "invalid request");
            const params = request.params ?? {};
            let result: unknown;
            switch (request.method) {
              case "hello":
                result = {
                  protocol: PROTOCOL_VERSION,
                  instanceId: host.host.id,
                  pid: process.pid,
                };
                break;
              case "deliver":
                result = await host.deliver(params);
                break;
              case "interact":
                if (
                  !Number.isInteger(params.revision) ||
                  params.revision < 0 ||
                  (params.action !== undefined &&
                    typeof params.action !== "string") ||
                  (params.text !== undefined && typeof params.text !== "string")
                )
                  throw new RpcError(
                    -32602,
                    "interaction requires a presentation revision and optional action/text strings",
                  );
                result = await host.deliver(
                  { source: "radar.user", id: params.eventId, payload: null },
                  {
                    revision: params.revision,
                    action: params.action,
                    text: params.text,
                  },
                );
                break;
              case "snapshot":
                result = await host.snapshot();
                break;
              case "conversation":
                result = await host.conversation(params.id);
                break;
              case "watch":
                watching = true;
                result = await host.snapshot();
                break;
              case "shutdown":
                send({ jsonrpc: "2.0", id, result: { stopping: true } });
                setImmediate(() => void shutdown());
                return;
              default:
                throw new RpcError(-32601, "unknown method");
            }
            send({ jsonrpc: "2.0", id, result });
          } catch (error) {
            send({
              jsonrpc: "2.0",
              id,
              error: {
                code: error instanceof RpcError ? error.code : -32603,
                message:
                  error instanceof Error ? error.message : "internal error",
              },
            });
          } finally {
            active--;
          }
        })();
      }
    });
  });
  await new Promise<void>((done, reject) => {
    server.once("error", reject);
    server.listen(socketPath, done);
  });
  await chmod(socketPath, 0o600);
  await writeFile(join(dataDir, "pid"), String(process.pid), { mode: 0o600 });
  process.on("SIGTERM", () => void shutdown());
  process.on("SIGINT", () => void shutdown());
  host.harness.closed.then((end) => {
    if (!closing) {
      console.error("Harness closed:", end.reason);
      void shutdown();
    }
  });
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : error);
  process.exitCode = 1;
});
