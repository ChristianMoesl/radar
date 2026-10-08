// Launch-only advice, not the pi-radar integration. Never installs packages,
// registers model tools, or changes Pi settings.
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { getAgentDir, type ExtensionAPI } from "@earendil-works/pi-coding-agent";

type Integration = "pi-radar" | "pi-sbx";
const widget = "radar-install-hint";

async function readJSON(path: string): Promise<any> {
  try {
    return JSON.parse(await readFile(path, "utf8"));
  } catch (error) {
    if (["ENOENT", "ENOTDIR"].includes((error as NodeJS.ErrnoException).code ?? "")) return {};
    throw error;
  }
}

async function packageSource(source: string, base: string, integration: Integration): Promise<boolean> {
  // Recognize declarations even when their resources are deliberately disabled.
  // Version requirements are enforced elsewhere, not by this notice.
  const packageName = `@christianmoesl/${integration}`;
  const repository = integration === "pi-radar" ? "radar" : "pi-sbx";
  if (source === `npm:${packageName}` || source.startsWith(`npm:${packageName}@`)) return true;
  if (new RegExp(`^(?:git:)?(?:https?://|git://|ssh://git@|git@)?github\\.com[:/]ChristianMoesl/${repository}(?:\\.git)?(?:@[^\\s]+)?/?$`, "i").test(source)) return true;
  if (/^(?:npm:|git:|https?:|ssh:|builtin:)/.test(source)) return false;
  const path = resolve(base, source.startsWith("~/") ? join(homedir(), source.slice(2)) : source);
  if (new RegExp(`[/\\\\]${integration}[/\\\\]index\\.[cm]?[jt]s$`).test(path)) return true;
  // Local package installs need not have a directory named after the integration.
  return (await readJSON(join(path, "package.json"))).name === packageName;
}

async function packageConfigured(agentDir: string, cwd: string, integration: Integration): Promise<boolean> {
  for (const base of [agentDir, join(cwd, ".pi")]) {
    const settings = await readJSON(join(base, "settings.json"));
    for (const item of settings.packages ?? []) {
      const source = typeof item === "string" ? item : item.source;
      if (typeof source === "string" && await packageSource(source, base, integration)) return true;
    }
    for (const source of settings.extensions ?? []) {
      if (await packageSource(source.replace(/^[!+-]/, ""), base, integration)) return true;
    }
  }
  return false;
}

export async function radarConfigured(agentDir: string, cwd: string): Promise<boolean> {
  return packageConfigured(agentDir, cwd, "pi-radar");
}

export async function sbxConfigured(agentDir: string, cwd: string): Promise<boolean> {
  return packageConfigured(agentDir, cwd, "pi-sbx");
}

// The workspace's effective resource bundle already incorporates user/repository
// choices. Do not guess enablement from PATH or parse Radar's YAML independently.
async function sandboxRelevant(pi: ExtensionAPI, cwd: string): Promise<boolean> {
  try {
    const result = await pi.exec(process.env.RADAR_BINARY?.trim() || "radar",
      ["workspace-context", "--workspace", resolve(cwd), "--json"], { timeout: 5000 });
    if (result.code !== 0) return false;
    const context = JSON.parse(result.stdout);
    if (context.registered !== true || context.capabilities?.sandbox !== true || context.sandbox == null) return false;
    // Introspection can preserve a sandbox's desired state after the CLI was
    // removed. Probe the native CLI without touching or provisioning a sandbox.
    const sbx = await pi.exec("sbx", ["--help"], { timeout: 2000 });
    return sbx.code === 0;
  } catch { return false; }
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
    try {
      const agentDir = resolve(getAgentDir());
      // Reading untrusted project declarations only suppresses advice; it never
      // loads their code or assumes that the integration is actually active.
      const radarPresent = pi.getAllTools().some(tool => tool.name === "radar_workspace_context")
        || await radarConfigured(agentDir, ctx.cwd);
      const sbxPresent = await sbxConfigured(agentDir, ctx.cwd);
      const recommendSBX = !sbxPresent && await sandboxRelevant(pi, ctx.cwd);
      if (radarPresent && !recommendSBX) return;
      const marker = join(agentDir, "radar", "install-hint-seen");
      await mkdir(dirname(marker), { recursive: true, mode: 0o700 });
      // Claim one combined notice per Pi profile, even across concurrent launches.
      // If config or storage cannot be read/written, leave startup unaffected.
      await writeFile(marker, "shown\n", { flag: "wx", mode: 0o600 });
      pi.registerCommand("radar-dismiss-install-hint", {
        description: "Dismiss the Pi package recommendations (don't remind me again)",
        handler: async (_args, context) => { context.ui.setWidget(widget, undefined); },
      });
      const profile = process.env.PI_CODING_AGENT_DIR
        ? `PI_CODING_AGENT_DIR='${agentDir.replaceAll("'", "'\"'\"'")}' ` : "";
      const advice: string[] = [];
      if (!radarPresent) {
        advice.push(
          "Recommended: pi-radar adds Radar workspace tools, instructions, skills, and activity status.",
          `Install in a host terminal: ${profile}pi install npm:@christianmoesl/pi-radar`,
        );
      }
      if (recommendSBX) {
        advice.push(
          "Recommended: pi-sbx routes Pi's tools into SBX; >=0.6.0 is required for early sandboxed launch.",
          `Install in a host terminal: ${profile}pi install npm:@christianmoesl/pi-sbx`,
        );
      }
      ctx.ui.setWidget(widget, [
        ...advice,
        "Then restart Pi. Advice only; no automatic installs or Pi settings changes.",
        "Don't remind me again: /radar-dismiss-install-hint (shown only once per Pi profile).",
      ]);
    } catch {
      // Advice must never turn uncertain detection or an unwritable profile
      // into a startup failure, nor diagnose a broken install as a missing one.
    }
  });
}
