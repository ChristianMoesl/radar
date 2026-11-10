import { readFile, lstat, rename, writeFile, unlink } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { isAbsolute } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { setTimeout as delay } from "node:timers/promises";
import type { Credential, CredentialStore } from "@earendil-works/pi-ai";
import type { ModelRef } from "@earendil-works/pi-durable";

/** Only an explicitly configured, runtime-owned store is used. Never search
 * interactive Pi, other agents, or provider-specific credential directories. */
export function modelCredentialStore(path: string, provider: string): CredentialStore {
  if (!isAbsolute(path)) throw new Error("RADAR_AGENT_AUTH_FILE must be absolute");
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/.test(provider)) throw new Error("Select a named model provider");
  async function load(): Promise<Record<string, Credential>> {
    try {
      const info = await lstat(path);
      if (!info.isFile() || info.mode & 0o077 ||
          (process.getuid && info.uid !== process.getuid())) throw new Error();
      const text = await readFile(path, "utf8");
      if (Buffer.byteLength(text) > 1024 * 1024) throw new Error();
      const value = JSON.parse(text);
      if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error();
      const selected = value[provider];
      if (selected && !["api_key", "oauth"].includes(selected.type)) throw new Error();
      return value;
    } catch {
      throw new Error("Configured model authentication store is unavailable or invalid; preserve it and check its private path/permissions");
    }
  }
  const allowed = (id: string) => {
    if (id !== provider) throw new Error("Model provider is not selected");
  };
  async function locked<T>(operation: () => Promise<T>, signal?: AbortSignal): Promise<T> {
    const combined = AbortSignal.any([AbortSignal.timeout(60000), ...(signal ? [signal] : [])]);
    // Token-free SQLite ownership follows the existing MCP client convention:
    // process death releases the lock, including during OAuth rotation.
    const lock = new DatabaseSync(`${path}.radar-lock.sqlite`);
    try {
      while (true) {
        combined.throwIfAborted();
        try { lock.exec("PRAGMA busy_timeout=0; BEGIN EXCLUSIVE"); break; }
        catch (error) {
          if (!/locked|busy/i.test(String(error))) throw error;
          await delay(25, undefined, { signal: combined });
        }
      }
      return await operation();
    } finally { lock.close(); }
  }
  return {
    async read(id, options) { allowed(id); options?.signal?.throwIfAborted(); return (await load())[id]; },
    async list(options) {
      options?.signal?.throwIfAborted();
      const value = (await load())[provider];
      return value ? [{ providerId: provider, type: value.type }] : [];
    },
    async modify(id, fn, options) {
      allowed(id);
      return locked(async () => {
        const values = await load(), current = values[id];
        const next = await fn(current === undefined ? undefined : structuredClone(current));
        if (next !== undefined && JSON.stringify(next) !== JSON.stringify(current)) {
          values[id] = next;
          const temporary = `${path}.${randomUUID()}.tmp`;
          await writeFile(temporary, JSON.stringify(values), { mode: 0o600, flag: "wx" });
          try { await rename(temporary, path); }
          finally { await unlink(temporary).catch(() => {}); }
        }
        return next ?? current;
      }, options?.signal);
    },
    async delete() { throw new Error("Agent model runtime does not log out or delete credentials"); },
  };
}

export async function configuredModels(model: ModelRef) {
  const { builtinModels } = await import("@earendil-works/pi-ai/providers/all");
  const path = process.env.RADAR_AGENT_AUTH_FILE;
  const models = builtinModels(path ? { credentials: modelCredentialStore(path, model.provider) } : undefined);
  if (!models.getModel(model.provider, model.modelId))
    throw new Error("Selected model is unavailable in the installed provider catalog");
  try {
    if (!await models.checkAuth(model.provider)) throw new Error();
  } catch {
    throw new Error("Selected model authentication is unavailable; configure its provider environment or RADAR_AGENT_AUTH_FILE on the runtime host");
  }
  return models;
}
