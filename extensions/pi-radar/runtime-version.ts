import { randomUUID } from "node:crypto";
import { mkdir, rename, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { getAgentDir, type ExtensionAPI, type ExtensionContext } from "@earendil-works/pi-coding-agent";
import { radarPackageVersion } from "./version.ts";

// A loaded-version report is independent of package.json on disk, which can be
// replaced while this module remains resident. Multiple sessions share a PID.
export async function reportLoadedVersion(pi: ExtensionAPI, ctx: ExtensionContext) {
  const profile = resolve(getAgentDir());
  const directory = join(profile, "radar", "loaded");
  const file = join(directory, `${process.pid}-${randomUUID()}.json`);
  let stopped = false;
  let pending = Promise.resolve();
  function publish() {
    pending = pending.then(async () => {
      if (stopped) return;
      const temporary = `${file}.tmp`;
      try {
        await mkdir(directory, { recursive: true, mode: 0o700 });
        await writeFile(temporary, JSON.stringify({ pid: process.pid, version: radarPackageVersion, profile, cwd: ctx.cwd, source: fileURLToPath(import.meta.url), updated: Date.now() }), { mode: 0o600 });
        await rename(temporary, file);
      } catch { /* Informational; never interfere with Pi activity. */ }
      finally { await rm(temporary, { force: true }).catch(() => {}); }
    });
    return pending;
  }
  const timer = setInterval(() => void publish(), 60_000);
  timer.unref();
  pi.on("session_shutdown", async () => {
    stopped = true;
    clearInterval(timer);
    await pending;
    await rm(file, { force: true }).catch(() => {});
  });
  await publish();
}
