import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

// Called only after workspace activation, once per session start (not reload).
// This is a UI notice, not a command, model message, or persisted introduction.
export async function showWorkspaceAttached(pi: ExtensionAPI, ctx: ExtensionContext) {
  if (!ctx.hasUI || ctx.mode !== "tui") return;
  let opening = "Run `radar` in another terminal to open the task dashboard.";
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
        if (key !== "none" && /^[!-~]{1,40}$/.test(key)) {
          const formatted = key.replace(/^(?:(?:C|M|S)-)+/, modifiers =>
            modifiers.replaceAll("C-", "Ctrl+").replaceAll("M-", "Alt+").replaceAll("S-", "Shift+")
          ).replace(/\+[a-z]$/, letter => letter.toUpperCase());
          opening = `Press ${formatted}, then r to open the Radar task dashboard.`;
        }
      }
    } catch { /* A missing/custom popup must not produce misleading instructions. */ }
  }
  ctx.ui.notify(`Radar workspace attached. ${opening}`, "info");
}
