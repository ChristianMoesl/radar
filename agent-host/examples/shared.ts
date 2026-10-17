import { promisify } from "node:util";
import { execFile } from "node:child_process";
import { createModels } from "@earendil-works/pi-ai/models";
import { openaiProvider } from "@earendil-works/pi-ai/providers/openai";
import {
  fauxAssistantMessage,
  fauxProvider,
} from "@earendil-works/pi-ai/providers/faux";
import type {
  AgentChange,
  ModelRef,
  TaskRuntime,
  EntryId,
} from "@earendil-works/pi-durable";
import type { Context } from "@earendil-works/chord";
import type { HostContext, JsonValue } from "../src/index.ts";

export function modelRuntime(config: HostContext["config"]): {
  models: ReturnType<typeof createModels>;
  model: ModelRef;
} {
  const models = createModels();
  if (config.fixture === true) {
    const faux = fauxProvider();
    faux.setResponses(
      Array.from({ length: 1000 }, () =>
        fauxAssistantMessage(
          "Fixture analysis completed. This is a deterministic test result, not a live investigation.",
        ),
      ),
    );
    models.setProvider(faux.provider);
    return { models, model: { provider: "faux", modelId: "faux-1" } };
  }
  if (typeof config.modelId !== "string" || !config.modelId)
    throw new Error(
      "configure modelId and OPENAI_API_KEY, or explicitly select fixture:true for tests",
    );
  models.setProvider(openaiProvider());
  return { models, model: { provider: "openai", modelId: config.modelId } };
}

export function agentChoices(
  model: ModelRef,
  instructions: string,
  cwd?: string,
): AgentChange {
  return { model, instructions, ...(cwd ? { cwd } : {}) };
}

export function requiredString(
  config: HostContext["config"],
  key: string,
): string {
  const value = config[key];
  if (typeof value !== "string" || !value) throw new Error(`configure ${key}`);
  return value;
}

export async function command(
  file: string,
  args: string[],
  cwd?: string,
  signal?: AbortSignal,
): Promise<string> {
  const { stdout } = await promisify(execFile)(file, args, {
    cwd,
    signal,
    maxBuffer: 1024 * 1024,
    env: { ...process.env, GIT_OPTIONAL_LOCKS: "0", GIT_TERMINAL_PROMPT: "0" },
  });
  return stdout.trim();
}

export async function jsonRequest<T>(
  url: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch(url, {
    ...options,
    signal: options.signal ?? AbortSignal.timeout(20000),
  });
  if (!response.ok)
    throw new Error(`remote request failed (${response.status})`);
  const text = await response.text();
  if (text.length > 4 * 1024 * 1024)
    throw new Error("remote result exceeds 4 MiB; narrow the query");
  return JSON.parse(text) as T;
}

export function textFromDocument(value: JsonValue): string {
  if (typeof value === "string") return value;
  if (!value || typeof value !== "object") return "";
  if (Array.isArray(value)) return value.map(textFromDocument).join(" ");
  return [
    typeof value.text === "string" ? value.text : "",
    value.content ? textFromDocument(value.content) : "",
  ]
    .filter(Boolean)
    .join(" ");
}

// An event task's runtime.entry() is scoped to its own conversation. Answers
// from an explicitly selected other conversation are read on the Session line.
export async function answerText<
  I,
  S extends { phase: string },
  R,
  H extends object,
>(
  runtime: TaskRuntime<I, S, R, H>,
  id: EntryId,
  context: Context,
): Promise<string> {
  let text = "";
  await runtime.commit(async (tx) => {
    const entry = await tx.entry(id);
    text = textFromDocument(entry?.model as unknown as JsonValue);
    return undefined;
  }, context);
  return text;
}
