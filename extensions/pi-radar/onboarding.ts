import { mkdir, open, unlink } from "node:fs/promises";
import { dirname, join } from "node:path";
import { getAgentDir, type ExtensionAPI, type ExtensionContext } from "@earendil-works/pi-coding-agent";

async function introduction(pi: ExtensionAPI): Promise<string> {
  let opening = "Run `radar` in another terminal to open the dashboard.";
  if (process.env.TMUX) {
    try {
      const target = process.env.TMUX_PANE;
      const prefixArgs = ["show-options", "-v", ...(target ? ["-t", target] : []), "prefix"];
      const [prefix, binding] = await Promise.all([
        pi.exec("tmux", prefixArgs, { timeout: 2000 }),
        pi.exec("tmux", ["list-keys", "-T", "prefix", "r"], { timeout: 2000 }),
      ]);
      if (prefix.code === 0 && binding.code === 0 && /\bdisplay-popup\b.*[ '\"]radar['\"]?\s*$/.test(binding.stdout)) {
        const key = prefix.stdout.trim();
        if (key !== "none" && /^[A-Za-z0-9+-]+$/.test(key)) {
          opening = `Press ${key.replace(/^C-(.+)$/, (_, letter: string) => "Ctrl+" + (letter.length === 1 ? letter.toUpperCase() : letter)).replace(/^M-/, "Alt+")}, release, then R to open Radar from this workspace.`;
        }
      }
    } catch { /* A missing/custom popup must not produce misleading instructions. */ }
  }
  return `${opening}\nUse Radar to find tasks, create workspaces, and switch between ongoing work.\nAsk Pi “What can Radar do?” or “How do I configure my workspaces?”; terminal reference: man radar and man radar-config.\nReopen this introduction with /radar-onboarding.`;
}

// Registered only after pi-radar establishes workspace membership. No model
// invocation, installer, or background worker is needed for this short tour.
export function registerOnboarding(pi: ExtensionAPI) {
  let offered = false;
  const show = async (ctx: ExtensionContext) => {
    ctx.ui.notify(await introduction(pi), "info");
  };
  pi.registerCommand("radar-onboarding", {
    description: "Show the short Radar introduction",
    handler: async (_args, ctx) => {
      if (ctx.hasUI && ctx.mode === "tui") {
        await ctx.waitForIdle();
        await show(ctx);
      }
    },
  });
  pi.on("resources_discover", async (_event, ctx) => {
    if (offered || !ctx.hasUI || ctx.mode !== "tui" || !ctx.isIdle()) return;
    offered = true;
    const marker = join(getAgentDir(), "radar", "onboarding-seen");
    let file;
    let answered = false;
    try {
      await mkdir(dirname(marker), { recursive: true, mode: 0o700 });
      // Exclusive creation claims the offer across simultaneous Pi processes.
      // Existing markers (including an interrupted process's claim) stay quiet.
      file = await open(marker, "wx", 0o600);
      const choice = await ctx.ui.select("Would you like a quick introduction to Radar?", ["Yes", "No — don't ask again"]);
      if (choice === undefined) return; // Escape is not a permanent dismissal.
      await file.writeFile(choice === "Yes" ? "completed\n" : "dismissed\n");
      answered = true;
      if (choice === "Yes") await show(ctx);
    } catch {
      // An optional welcome must never prevent Pi or workspace startup.
    } finally {
      if (file) {
        await file.close().catch(() => {});
        if (!answered) await unlink(marker).catch(() => {});
      }
    }
  });
}
