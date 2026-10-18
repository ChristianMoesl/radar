import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { pathToFileURL } from "node:url";
import { WorkspaceExecution } from "./environment.ts";
import type { JsonValue } from "@earendil-works/chord";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import {
  Harness,
  createRegistry,
  defineExtension,
  type Conversation,
  type ConversationId,
  type Task,
} from "@earendil-works/pi-durable";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import {
  PROTOCOL_VERSION,
  PresentationDoc,
  ReceiptDoc,
  RpcError,
  validateEvent,
  type AgentDefinition,
  type AgentEvent,
  type HostContext,
  type Receipt,
  type Snapshot,
} from "./index.ts";

const context = BACKGROUND_CONTEXT;

// Canonical JSON makes retries insensitive to object-key ordering.
export function canonical(value: JsonValue): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value && typeof value === "object")
    return `{${Object.keys(value)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${canonical(value[k]!)}`)
      .join(",")}}`;
  return JSON.stringify(value);
}
const digest = (value: string) =>
  createHash("sha256").update(value).digest("hex");

export class AgentHost {
  readonly host: HostContext;
  readonly definition: AgentDefinition;
  readonly harness: Harness;
  readonly root: Conversation;
  private readonly lock: DatabaseSync;
  private constructor(
    host: HostContext,
    definition: AgentDefinition,
    harness: Harness,
    root: Conversation,
    lock: DatabaseSync,
  ) {
    this.host = host;
    this.definition = definition;
    this.harness = harness;
    this.root = root;
    this.lock = lock;
  }

  static async open(
    entrypoint: string,
    input: Omit<HostContext, "execution">,
  ): Promise<AgentHost> {
    if (!input.workspace?.id || !input.workspace.path)
      throw new Error("agent requires an associated Radar workspace");
    const host: HostContext = {
      ...input,
      execution: new WorkspaceExecution(input.workspace),
    };
    await mkdir(host.dataDir, { recursive: true, mode: 0o700 });
    const lock = new DatabaseSync(join(host.dataDir, "owner.sqlite"));
    try {
      lock.exec("PRAGMA busy_timeout=0; BEGIN EXCLUSIVE;");
    } catch {
      lock.close();
      throw new Error("agent store already has an owner");
    }
    let harness: Harness | undefined;
    try {
      const definition = (await import(pathToFileURL(entrypoint).href))
        .default as AgentDefinition;
      if (
        !definition ||
        !Number.isInteger(definition.version) ||
        definition.version < 1 ||
        typeof definition.configure !== "function" ||
        !definition.eventTask?.definition
      ) {
        throw new Error(
          "agent module must default-export { version, configure, eventTask }",
        );
      }
      const metadataPath = join(host.dataDir, "format.json");
      let metadata:
        | { version: number; workspaceId: string; instanceId: string }
        | undefined;
      try {
        metadata = JSON.parse(await readFile(metadataPath, "utf8"));
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      }
      if (
        metadata &&
        (metadata.version !== 1 ||
          metadata.workspaceId !== host.workspace.id ||
          metadata.instanceId !== host.id)
      )
        throw new Error(
          "unsupported agent storage format or changed instance/workspace identity; preserve data and perform an explicit rollout",
        );
      if (!metadata)
        await writeFile(
          metadataPath,
          JSON.stringify({
            version: 1,
            workspaceId: host.workspace.id,
            instanceId: host.id,
          }),
          { flag: "wx", mode: 0o600 },
        );
      const options = await definition.configure(host);
      if (options.env)
        throw new Error(
          "Radar supplies the workspace execution environment; use host.execution for custom workspace tools",
        );
      const registry = options.registry ?? createRegistry();
      // The registry is a host-owned mutable Registry, not a remote RegistryReader.
      if (!("install" in registry) || typeof registry.install !== "function")
        throw new Error("configure must supply a mutable registry");
      registry.install(
        defineExtension({ name: "radar.host", tasks: [definition.eventTask] }),
      );
      const storage = await openNodeSqliteStorage(
        join(host.dataDir, "agent.sqlite"),
      );
      try {
        harness = await Harness.open(
          storage,
          {
            ...options,
            registry,
            env: (target) => host.execution.native(target.cwd),
          },
          context,
        );
      } catch (error) {
        await storage.close(context);
        throw error;
      }
      const root = await harness.root(context);
      const result = new AgentHost(host, definition, harness, root, lock);
      harness.resume();
      return result;
    } catch (error) {
      if (harness) await harness.close(context).catch(() => {});
      lock.close();
      throw error;
    }
  }

  async deliver(
    value: unknown,
    interaction?: { revision: number; action?: string; text?: string },
  ): Promise<Receipt> {
    validateEvent(value);
    const event = {
      source: value.source,
      id: value.id,
      payload: value.payload,
    };
    if (!interaction && event.source === "radar.user")
      throw new RpcError(
        -32602,
        "radar.user is reserved for human interactions",
      );
    const key = digest(canonical([event.source, event.id]));
    const receipt = await this.root.commit(async (tx) => {
      const prior = await tx.doc(ReceiptDoc, key, null);
      // Match the original user request before deriving an action's current payload.
      const hash = digest(
        canonical(interaction ? { event, interaction } : event),
      );
      if (prior.taskId) {
        if (prior.hash !== hash)
          throw new RpcError(
            -32009,
            "event identity already exists with different content",
          );
        return { ...prior };
      }
      let payload = event.payload;
      if (interaction) {
        const presentation = await tx.doc(PresentationDoc);
        if (interaction.revision !== presentation.revision)
          throw new RpcError(
            -32010,
            "interaction changed; refresh before responding",
          );
        if (interaction.action) {
          const action = presentation.actions.find(
            (a) => a.id === interaction.action,
          );
          if (!action)
            throw new RpcError(-32010, "action is no longer available");
          payload = {
            type: "radar.action",
            action: action.id,
            payload: action.payload,
            text: interaction.text ?? "",
            revision: interaction.revision,
          };
        } else {
          if (!presentation.message)
            throw new RpcError(-32010, "agent does not accept messages");
          payload = {
            type: "radar.message",
            text: interaction.text ?? "",
            revision: interaction.revision,
          };
        }
      }
      const id = await tx.createTask(
        this.definition.eventTask as Task<
          AgentEvent,
          { phase: string },
          JsonValue,
          object
        >,
        {
          ...event,
          payload,
          origin: (interaction ? "user" : "external") as AgentEvent["origin"],
        },
        { ownership: { kind: "conversation" } },
      );
      Object.assign(prior, {
        hash,
        taskId: Number(id),
        source: event.source,
        eventId: event.id,
        acceptedAt: new Date().toISOString(),
      });
      return { ...prior };
    }, context);
    this.harness.resume();
    return receipt;
  }

  async snapshot(): Promise<Snapshot> {
    const [presentation, inspection, recent] = await Promise.all([
      this.harness.snapshot(PresentationDoc, context),
      this.harness.inspect(context),
      this.root.commit(
        (tx) => tx.scanTasks({ order: "descending" }, 30),
        context,
      ),
    ]);
    const lastEvent = recent.items.find(
      (t) => t.kind === this.definition.eventTask.definition.name,
    );
    const problem =
      lastEvent?.state.status === "terminal" &&
      lastEvent.state.outcome.status === "faulted"
        ? lastEvent.state.outcome.error.message
        : inspection.tasks.find((t) => t.state.kind === "blocked")?.state
              .kind === "blocked"
          ? "work is blocked by a missing or incompatible task definition"
          : undefined;
    return {
      protocol: PROTOCOL_VERSION,
      instanceId: this.host.id,
      definitionVersion: this.definition.version,
      pid: process.pid,
      presentation: presentation ?? PresentationDoc.definition.initial(),
      pending: inspection.tasks.length,
      ...(problem ? { problem } : {}),
      tasks: inspection.tasks.slice(0, 200).map((t) => ({
        id: Number(t.record.id),
        kind: t.record.kind,
        status: t.record.state.status,
        ...(t.state.kind === "blocked" ? { problem: t.state.reason } : {}),
      })),
      recent: recent.items.map((t) => ({
        id: Number(t.id),
        kind: t.kind,
        status: t.state.status,
        ...(t.state.status === "terminal"
          ? {
              outcome: {
                status: t.state.outcome.status,
                ...(t.state.outcome.status === "faulted"
                  ? { error: t.state.outcome.error as unknown as JsonValue }
                  : {}),
              },
            }
          : {}),
      })),
    };
  }

  async conversation(id: number): Promise<unknown> {
    const presentation = await this.harness.snapshot(PresentationDoc, context);
    if (!presentation?.conversations.some((c) => c.id === id))
      throw new RpcError(
        -32602,
        "conversation is not exposed by this definition",
      );
    const conversation = await this.harness.conversation(
      id as ConversationId,
      context,
    );
    if (!conversation) throw new RpcError(-32602, "conversation unavailable");
    const page = await conversation.entries(
      { order: "descending" },
      100,
      undefined,
      context,
    );
    const entries = [...page.items].reverse();
    let truncated = !!page.next;
    while (
      Buffer.byteLength(JSON.stringify(entries)) > 700000 &&
      entries.length
    ) {
      entries.shift();
      truncated = true;
    }
    return { id, entries, truncated };
  }

  async close(): Promise<void> {
    this.host.execution.close();
    try {
      await this.harness.close(context);
    } finally {
      this.lock.close();
    }
  }
}
