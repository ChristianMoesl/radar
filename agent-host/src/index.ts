import type { JsonValue } from "@earendil-works/chord";
import type { WorkspaceBinding, WorkspaceExecution } from "./environment.ts";
import type { McpServices } from "./mcp.ts";
export { WorkspaceExecution, type WorkspaceBinding } from "./environment.ts";
import {
  defineDoc,
  defineDocFamily,
  type AnyTask,
  type HarnessOptions,
  type TaskId,
  type Tx,
} from "@earendil-works/pi-durable";

export * from "@earendil-works/pi-durable";
export { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
export { createModels } from "@earendil-works/pi-ai/models";
export type { JsonValue } from "@earendil-works/chord";

export const PROTOCOL_VERSION = 1;
export const MAX_FRAME_BYTES = 1024 * 1024;

export type IncomingEvent = { source: string; id: string; payload: JsonValue };
export type AgentEvent = IncomingEvent & { origin: "external" | "user" };
export type HostContext = {
  id: string;
  dataDir: string;
  config: Record<string, JsonValue>;
  workspace: WorkspaceBinding;
  execution: WorkspaceExecution;
  mcp: McpServices;
};
export type AgentDefinition = {
  version: number;
  configure(host: HostContext): HarnessOptions | Promise<HarnessOptions>;
  eventTask: AnyTask;
};

export type AgentAction = {
  id: string;
  label: string;
  text?: boolean;
  payload: JsonValue;
};
export type Presentation = {
  title: string;
  summary: string;
  attention: boolean;
  revision: number;
  message: boolean;
  actions: AgentAction[];
  conversations: { id: number; label: string }[];
  workspaces: { path: string; label: string }[];
};

export const PresentationDoc = defineDoc<Presentation>({
  kind: "radar.presentation",
  version: 1,
  scope: "session",
  initial: () => ({
    title: "",
    summary: "",
    attention: false,
    revision: 0,
    message: false,
    actions: [],
    conversations: [],
    workspaces: [],
  }),
});

export async function present(
  tx: Tx,
  patch: Partial<Omit<Presentation, "revision">>,
): Promise<void> {
  if (
    (patch.summary?.length ?? 0) > 200000 ||
    (patch.title?.length ?? 0) > 200 ||
    [patch.actions, patch.conversations, patch.workspaces].some(
      (items) => (items?.length ?? 0) > 100,
    )
  ) {
    throw new Error("agent presentation exceeds its display limits");
  }
  const doc = await tx.doc(PresentationDoc);
  Object.assign(doc, patch);
  doc.revision++;
}

export type Receipt = {
  hash: string;
  taskId: number;
  source: string;
  eventId: string;
  acceptedAt: string;
};
export const ReceiptDoc = defineDocFamily<Receipt, null>({
  kind: "radar.receipt",
  version: 1,
  scope: "session",
  family: true,
  initial: () => ({
    hash: "",
    taskId: 0,
    source: "",
    eventId: "",
    acceptedAt: "",
  }),
});

export type Snapshot = {
  protocol: number;
  instanceId: string;
  definitionVersion: number;
  pid: number;
  presentation: Presentation;
  pending: number;
  problem?: string;
  tasks: { id: number; kind: string; status: string; problem?: string }[];
  recent: { id: number; kind: string; status: string; outcome?: JsonValue }[];
};

export class RpcError extends Error {
  code: number;
  constructor(code: number, message: string) {
    super(message);
    this.code = code;
  }
}

export function validateEvent(value: unknown): asserts value is IncomingEvent {
  const e = value as IncomingEvent | null;
  if (
    !e ||
    typeof e.source !== "string" ||
    !e.source ||
    e.source.length > 200 ||
    typeof e.id !== "string" ||
    !e.id ||
    e.id.length > 500 ||
    e.payload === undefined
  ) {
    throw new RpcError(-32602, "event requires source, id, and a JSON payload");
  }
}

export function taskId(value: number): TaskId {
  return value as TaskId;
}
