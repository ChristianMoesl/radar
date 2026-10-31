import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { setTimeout as delay } from "node:timers/promises";
import * as http from "node:http";
import {
  McpClient,
  StdioTransport,
  StreamableHttpTransport,
  type CallToolResult,
  type Tool,
} from "@earendil-works/pi-mcp";
import {
  adaptOAuthProvider,
  authorizeMcp,
  McpOAuthProvider,
  OAuthCallbackServer,
  type McpOAuthState,
  type McpOAuthStateStore,
} from "@earendil-works/pi-mcp/oauth";

type Server = {
  url?: string;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  oauth?: { clientId?: string; scope?: string; callbackPort?: number };
};
const MAX_BYTES = 1024 * 1024;
const PROFILE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;
function configureProxy(): void {
  const configure = (
    http as unknown as { setGlobalProxyFromEnv?: () => unknown }
  ).setGlobalProxyFromEnv;
  if (configure) {
    try {
      configure();
    } catch {
      throw new Error(
        "MCP proxy configuration is invalid; check the host proxy settings",
      );
    }
  } else if (
    process.env.HTTP_PROXY ||
    process.env.HTTPS_PROXY ||
    process.env.http_proxy ||
    process.env.https_proxy
  )
    throw new Error("MCP proxy support requires Node 24.5 or newer");
}

export function mcpPaths() {
  return {
    config:
      process.env.RADAR_MCP_CONFIG ??
      join(
        process.env.XDG_CONFIG_HOME ?? join(homedir(), ".config"),
        "radar",
        "mcp.json",
      ),
    state:
      process.env.RADAR_MCP_STATE_DIR ??
      join(
        process.env.XDG_STATE_HOME ?? join(homedir(), ".local/state"),
        "radar",
        "mcp",
      ),
  };
}

class StateStore implements McpOAuthStateStore {
  private path: string;
  constructor(path: string) {
    this.path = path;
  }
  async load(): Promise<McpOAuthState | undefined> {
    try {
      const data = await readFile(this.path, "utf8");
      if (Buffer.byteLength(data) > MAX_BYTES) throw new Error();
      const state = JSON.parse(data);
      if (!state || typeof state.serverUrl !== "string") throw new Error();
      return state;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
      throw new Error(
        "MCP OAuth state is invalid; preserve it and sign in again explicitly",
      );
    }
  }
  async save(state: McpOAuthState): Promise<void> {
    const temporary = `${this.path}.${process.pid}.tmp`;
    await writeFile(temporary, JSON.stringify(state), { mode: 0o600 });
    await rename(temporary, this.path);
  }
}

/** Host-side service access, independent of workspace execution. No provider
 * subprocess or credential is sent to SBX. Connections are scoped to one call;
 * the profile lock also serializes rotating OAuth refresh across agent runners. */
export class McpServices {
  private controller = new AbortController();
  private queues = new Map<string, Promise<unknown>>();
  private active = new Set<Promise<unknown>>();
  private proxyReady = false;
  private paths: ReturnType<typeof mcpPaths>;
  constructor(paths = mcpPaths()) {
    this.paths = paths;
  }

  private async server(name: string): Promise<Server> {
    if (!PROFILE.test(name))
      throw new Error("Select a named MCP server profile");
    let server: Server;
    try {
      const data = await readFile(this.paths.config, "utf8");
      if (Buffer.byteLength(data) > MAX_BYTES) throw new Error();
      server = JSON.parse(data).mcpServers?.[name];
    } catch {
      throw new Error(
        "Configure host MCP servers in RADAR_MCP_CONFIG or ~/.config/radar/mcp.json",
      );
    }
    if (!server || !!server.url === !!server.command)
      throw new Error(
        `MCP profile ${name} requires exactly one url or command`,
      );
    if (server.url) {
      let url: URL;
      try {
        url = new URL(server.url);
      } catch {
        throw new Error(
          "Configure a valid MCP server URL without embedded credentials",
        );
      }
      if (
        url.username ||
        url.password ||
        url.hash ||
        (url.protocol !== "https:" &&
          !(
            url.protocol === "http:" &&
            ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname)
          ))
      )
        throw new Error(
          "MCP URLs require HTTPS, or HTTP on loopback, without embedded credentials",
        );
    } else if (
      typeof server.command !== "string" ||
      !server.command.trim() ||
      (server.args &&
        (!Array.isArray(server.args) ||
          server.args.some((a) => typeof a !== "string")))
    ) {
      throw new Error(
        "Configure an installed host MCP server command and string arguments",
      );
    }
    return server;
  }

  private withProfile<T>(
    name: string,
    operation: (
      server: Server,
      directory: string,
      signal: AbortSignal,
    ) => Promise<T>,
    signal?: AbortSignal,
    timeout = 60000,
  ): Promise<T> {
    const combined = AbortSignal.any([
      this.controller.signal,
      AbortSignal.timeout(timeout),
      ...(signal ? [signal] : []),
    ]);
    const previous = this.queues.get(name) ?? Promise.resolve();
    const result = previous
      .catch(() => {})
      .then(async () => {
        combined.throwIfAborted();
        const server = await this.server(name);
        const directory = resolve(this.paths.state, name);
        await mkdir(directory, { recursive: true, mode: 0o700 });
        // This database contains no tokens. SQLite releases the lock on process
        // death, so interrupted runs cannot strand another agent's refresh.
        const lock = new DatabaseSync(join(directory, "lock.sqlite"));
        try {
          lock.exec("PRAGMA busy_timeout=0");
          for (;;) {
            combined.throwIfAborted();
            try {
              lock.exec("BEGIN IMMEDIATE");
              break;
            } catch (error) {
              if ((error as { errcode?: number }).errcode !== 5) throw error;
              await delay(50, undefined, { signal: combined });
            }
          }
          return await operation(server, directory, combined);
        } finally {
          lock.close();
        }
      });
    this.queues.set(name, result);
    this.active.add(result);
    void result
      .finally(() => {
        this.active.delete(result);
        if (this.queues.get(name) === result) this.queues.delete(name);
      })
      .catch(() => {});
    return result;
  }

  private provider(
    server: Server,
    directory: string,
    onRedirect: (url: URL) => void,
    redirectUrl?: string,
  ) {
    return new McpOAuthProvider({
      serverUrl: server.url!,
      redirectUrl:
        redirectUrl ??
        `http://127.0.0.1:${server.oauth?.callbackPort ?? 8765}/callback`,
      clientMetadata: { client_name: "Radar MCP client" },
      clientId: server.oauth?.clientId,
      store: new StateStore(join(directory, "oauth.json")),
      onRedirect,
    });
  }

  private async connected<T>(
    name: string,
    operation: (client: McpClient, signal: AbortSignal) => Promise<T>,
    signal?: AbortSignal,
  ): Promise<T> {
    return this.withProfile(
      name,
      async (server, directory, bounded) => {
        // Node's HTTP and fetch implementations honor the existing proxy and CA
        // configuration. Never unset them to reach a provider.
        if (!this.proxyReady) {
          configureProxy();
          this.proxyReady = true;
        }
        const client = new McpClient({
          name: "radar-agent-host",
          version: "0.1.0",
          requestTimeoutMs: 30000,
        });
        const auth = server.url
          ? this.provider(server, directory, () => {
              throw new Error(
                `MCP login required: run radar-mcp login ${name} on the host`,
              );
            })
          : undefined;
        const adapter = auth ? adaptOAuthProvider(auth) : undefined;
        const recoveries = new Set<Promise<void>>();
        const transport = server.url
          ? new StreamableHttpTransport({
              url: server.url,
              maxMessageBytes: MAX_BYTES,
              // Sessions last for one bounded operation. An unused background GET
              // stream must not start a refresh after the profile lock is released.
              openGetStream: false,
              authProvider: adapter && {
                ...adapter,
                onUnauthorized: async (context) => {
                  const recovery = (async () => {
                    bounded.throwIfAborted();
                    if (!(await auth!.tokens()))
                      throw new Error("MCP login required");
                    await adapter.onUnauthorized!(context);
                  })();
                  recoveries.add(recovery);
                  try {
                    await recovery;
                  } finally {
                    recoveries.delete(recovery);
                  }
                },
              },
              fetch: (input, init) =>
                fetch(input, {
                  ...init,
                  signal: AbortSignal.any([
                    bounded,
                    ...(init?.signal ? [init.signal] : []),
                  ]),
                }),
            })
          : new StdioTransport({
              command: server.command!,
              args: server.args,
              env: server.env,
              stderr: "pipe",
              maxMessageBytes: MAX_BYTES,
            });
        const abort = () => void client.close();
        bounded.addEventListener("abort", abort, { once: true });
        try {
          bounded.throwIfAborted();
          await client.connect(transport);
          return await operation(client, bounded);
        } catch {
          // Remote bodies and process stderr may contain credentials or private
          // provider data. Diagnostics identify only the locally selected profile.
          throw new Error(
            `MCP ${name} request failed; verify the host server, permissions and radar-mcp login ${name}`,
          );
        } finally {
          bounded.removeEventListener("abort", abort);
          await client.close().catch(() => {});
          // Transport cancellation rejects callers before shared OAuth recovery
          // settles. Keep the profile lock until its final state write finishes.
          await Promise.allSettled([...recoveries]);
        }
      },
      signal,
    );
  }

  tools(server: string, signal?: AbortSignal): Promise<Tool[]> {
    return this.connected(
      server,
      async (client, bounded) => {
        const tools = await client.listTools({ signal: bounded });
        if (Buffer.byteLength(JSON.stringify(tools)) > MAX_BYTES)
          throw new Error("MCP tool catalog exceeds the response limit");
        return tools;
      },
      signal,
    );
  }

  call(
    server: string,
    tool: string,
    args: Record<string, unknown>,
    signal?: AbortSignal,
    readOnly = false,
  ): Promise<CallToolResult> {
    return this.connected(
      server,
      async (client, bounded) => {
        const tools = await client.listTools({ signal: bounded });
        const selected = tools.find((t) => t.name === tool);
        if (Buffer.byteLength(JSON.stringify(tools)) > MAX_BYTES)
          throw new Error("MCP tool catalog exceeds the response limit");
        if (
          !selected ||
          (readOnly && selected.annotations?.readOnlyHint !== true)
        )
          throw new Error(
            "Configured MCP read tool is unavailable or not marked read-only",
          );
        if (Buffer.byteLength(JSON.stringify(args)) > MAX_BYTES)
          throw new Error("MCP tool arguments exceed the request limit");
        const result = await client.callTool(tool, args, { signal: bounded });
        if (
          result.isError ||
          Buffer.byteLength(JSON.stringify(result)) > MAX_BYTES
        )
          throw new Error("MCP tool failed or exceeded the response limit");
        return result;
      },
      signal,
    );
  }

  async json(
    server: string,
    tool: string,
    args: Record<string, unknown>,
    signal?: AbortSignal,
    readOnly = true,
  ): Promise<unknown> {
    const result = await this.call(server, tool, args, signal, readOnly);
    const metadata = (
      result as { _meta?: { partial?: boolean; truncated?: boolean } }
    )._meta;
    if (metadata?.partial === true || metadata?.truncated === true)
      throw new Error(
        "MCP returned partial JSON; prior facts must be retained",
      );
    if (result.structuredContent !== undefined) return result.structuredContent;
    const text = result.content?.filter((c) => c.type === "text");
    if (text?.length !== 1)
      throw new Error("MCP read requires one structured JSON result");
    try {
      return JSON.parse((text[0] as { text: string }).text);
    } catch {
      throw new Error(
        "MCP read returned unstructured data; completeness cannot be verified",
      );
    }
  }

  /** Explicit foreground OAuth only. Background calls never open a browser. */
  login(name: string, display: (url: URL) => void): Promise<void> {
    return this.withProfile(
      name,
      async (server, directory, signal) => {
        if (!server.url)
          throw new Error(
            "This stdio MCP server owns its own host authentication; follow its setup instructions",
          );
        if (!this.proxyReady) {
          configureProxy();
          this.proxyReady = true;
        }
        const callback = await OAuthCallbackServer.listen({
          port: server.oauth?.callbackPort ?? 8765,
          timeoutMs: 300000,
        });
        const provider = this.provider(
          server,
          directory,
          display,
          callback.redirectUrl,
        );
        let closing: Promise<void> | undefined;
        const close = () => (closing ??= callback.close());
        const abort = () => {
          void close().catch(() => {});
        };
        signal.addEventListener("abort", abort, { once: true });
        try {
          const state = await provider.state();
          const received = callback.waitForCallback(state);
          void received.catch(() => {});
          await authorizeMcp(provider, {
            serverUrl: server.url,
            scope: server.oauth?.scope,
            skipRefresh: true,
            signal,
          });
          const response = await received;
          await authorizeMcp(provider, {
            serverUrl: server.url,
            authorizationCode: response.code,
            iss: response.iss,
            signal,
          });
        } catch {
          throw new Error(
            "MCP OAuth login did not complete; check browser approval, callback port and server registration requirements",
          );
        } finally {
          signal.removeEventListener("abort", abort);
          await close().catch(() => {});
        }
      },
      undefined,
      330000,
    );
  }

  async close(): Promise<void> {
    this.controller.abort();
    await Promise.allSettled([...this.active]);
  }
}
