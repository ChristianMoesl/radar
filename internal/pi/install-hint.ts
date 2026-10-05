// Launch-only advice, not the pi-radar integration. Never installs packages,
// registers model tools, or changes Pi settings.
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { getAgentDir, type ExtensionAPI } from "@earendil-works/pi-coding-agent";

const packageName = "@christianmoesl/pi-radar";
const widget = "radar-install-hint";

async function readJSON(path: string): Promise<any> {
  try {
    return JSON.parse(await readFile(path, "utf8"));
  } catch (error) {
    if (["ENOENT", "ENOTDIR"].includes((error as NodeJS.ErrnoException).code ?? "")) return {};
    throw error;
  }
}

async function radarSource(source: string, base: string): Promise<boolean> {
  // Recognize declarations even when their resources are deliberately disabled.
  if (source === `npm:${packageName}` || source.startsWith(`npm:${packageName}@`)) return true;
  if (/^(?:git:)?(?:https?:\/\/|ssh:\/\/git@|git@)?github\.com[:/]ChristianMoesl\/radar(?:\.git)?(?:@[^\s]+)?\/?$/i.test(source)) return true;
  if (/^(?:npm:|git:|https?:|ssh:|builtin:)/.test(source)) return false;
  const path = resolve(base, source.startsWith("~/") ? join(homedir(), source.slice(2)) : source);
  if (/[/\\]pi-radar[/\\]index\.[cm]?[jt]s$/.test(path)) return true;
  // Local package installs need not have a directory named radar.
  return (await readJSON(join(path, "package.json"))).name === packageName;
}

export async function radarConfigured(agentDir: string, cwd: string): Promise<boolean> {
  for (const base of [agentDir, join(cwd, ".pi")]) {
    const settings = await readJSON(join(base, "settings.json"));
    for (const item of settings.packages ?? []) {
      const source = typeof item === "string" ? item : item.source;
      if (typeof source === "string" && await radarSource(source, base)) return true;
    }
    for (const source of settings.extensions ?? []) {
      if (await radarSource(source.replace(/^[!+-]/, ""), base)) return true;
    }
  }
  return false;
}

export default function installHint(pi: ExtensionAPI) {
  // This event follows every extension's session_start, including pi-radar's
  // asynchronous activation. Checking all tools also respects tool filtering.
  pi.on("resources_discover", async (_event, ctx) => {
    if (!ctx.hasUI || ctx.mode !== "tui") return;
    ctx.ui.setWidget(widget, undefined);
    const args = process.argv.slice(2);
    const flags = args.slice(0, args.indexOf("--") === -1 ? args.length : args.indexOf("--"));
    if (flags.includes("--no-extensions") || flags.includes("-ne")) return;
    if (pi.getAllTools().some(tool => tool.name === "radar_workspace_context")) return;
    try {
      const agentDir = resolve(getAgentDir());
      // Reading untrusted project declarations only suppresses advice; it never
      // loads their code or assumes that the integration is actually active.
      if (await radarConfigured(agentDir, ctx.cwd)) return;
      const marker = join(agentDir, "radar", "install-hint-seen");
      await mkdir(dirname(marker), { recursive: true, mode: 0o700 });
      // Claim the notice once per Pi profile, even across concurrent launches.
      // If config or storage cannot be read/written, leave startup unaffected.
      await writeFile(marker, "shown\n", { flag: "wx", mode: 0o600 });
      pi.registerCommand("radar-dismiss-install-hint", {
        description: "Dismiss the pi-radar recommendation (don't remind me again)",
        handler: async (_args, context) => { context.ui.setWidget(widget, undefined); },
      });
      const profile = process.env.PI_CODING_AGENT_DIR
        ? `PI_CODING_AGENT_DIR='${agentDir.replaceAll("'", "'\"'\"'")}' ` : "";
      ctx.ui.setWidget(widget, [
        "Recommended: pi-radar adds Radar workspace tools, instructions, skills, and activity status.",
        `Install in a host terminal: ${profile}pi install npm:${packageName}`,
        "Then restart Pi. Optional; your session works without it.",
        "Don't remind me again: /radar-dismiss-install-hint (shown only once per Pi profile).",
      ]);
    } catch {
      // Advice must never turn uncertain detection or an unwritable profile
      // into a startup failure, nor diagnose a broken install as a missing one.
    }
  });
}
