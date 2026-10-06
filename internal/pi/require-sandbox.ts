// Launch-only prerequisite for Radar's early sandboxed Pi session. pi-sbx owns
// routing, readiness and deliberate /sbx off host consent; this guard owns none.
import { readFileSync, realpathSync, statSync, writeSync } from "node:fs";
import { dirname, isAbsolute, join, relative, sep } from "node:path";
import type { ExtensionAPI, ExtensionContext, SourceInfo } from "@earendil-works/pi-coding-agent";

const routedTools = ["bash", "read", "write", "edit", "grep", "find", "ls"];
// Pi rebuilds extension instances on reload/session replacement, but does not
// change the process cwd. Do not scope this CLI extension to the resumed cwd.
const workspace = realpathSync(process.cwd());

function extensionPath(info: SourceInfo | undefined): string {
  if (!info || typeof info.path !== "string" || !isAbsolute(info.path)) {
    throw new Error("Pi did not expose a verifiable extension sourceInfo.path (update Pi).");
  }
  return realpathSync(info.path);
}

function verify(pi: ExtensionAPI): void {
  // Declarations, disabled package records and persisted session state are not
  // evidence of an active provider. Only Pi's effective command/tool provenance is.
  const commands = pi.getCommands().filter(command => command.name === "sbx" && command.source === "extension");
  if (commands.length !== 1) throw new Error("The active /sbx command is missing or ambiguous.");
  const source = extensionPath(commands[0].sourceInfo);
  const tools = pi.getAllTools();
  for (const name of routedTools) {
    const matches = tools.filter(tool => tool.name === name);
    if (matches.length !== 1 || extensionPath(matches[0].sourceInfo) !== source) {
      throw new Error(`The ${name} tool is missing or overridden; all seven routed tools must belong to the active /sbx extension.`);
    }
  }

  // Resolve symlinks first (local installs need not have a recognizable path),
  // then stop at the CLOSEST package.json, even if its identity is wrong.
  let directory = statSync(source).isDirectory() ? source : dirname(source);
  for (;;) {
    const manifestPath = join(directory, "package.json");
    let manifest: { name?: unknown; version?: unknown };
    try {
      manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      const parent = dirname(directory);
      if (parent === directory) throw new Error(`No package.json owns the active extension at ${source}.`);
      directory = parent;
      continue;
    }
    if (manifest?.name !== "@christianmoesl/pi-sbx") {
      throw new Error(`The closest package.json at ${manifestPath} is not @christianmoesl/pi-sbx.`);
    }
    // Stable SemVer only, including optional build metadata. No prerelease can
    // establish the safety guarantee, even one newer than the minimum release.
    const version = typeof manifest.version === "string"
      ? /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.exec(manifest.version)
      : null;
    if (!version || !(BigInt(version[1]) > 0n || BigInt(version[2]) >= 6n)) {
      throw new Error(`Active pi-sbx version ${JSON.stringify(manifest.version) ?? "unknown"} is incompatible; a stable version >=0.6.0 is required.`);
    }
    return;
  }
}

function guidance(reason: string): string {
  const profile = process.env.PI_CODING_AGENT_DIR;
  const prefix = profile ? `PI_CODING_AGENT_DIR='${profile.replaceAll("'", "'\"'\"'")}' ` : "";
  return [
    "Radar sandbox launch refused: active @christianmoesl/pi-sbx >=0.6.0 is required.",
    reason,
    `In a host terminal, install/update: ${prefix}pi install npm:@christianmoesl/pi-sbx`,
    "Enable its extension with pi config, remove conflicting /sbx or routed-tool overrides, then restart this workspace Pi session.",
    "No unsafe host fallback. /sbx off remains deliberate host consent only with the verified provider.",
  ].join("\n");
}

export default function requireSandbox(pi: ExtensionAPI) {
  let verified = false;
  let failure = guidance("The active provider has not yet been verified.");
  let reported: string | undefined;
  function inWorkspace(ctx: ExtensionContext): boolean | undefined {
    try {
      const path = relative(workspace, realpathSync(ctx.cwd));
      return path !== ".." && !path.startsWith(`..${sep}`) && !isAbsolute(path);
    } catch (error) {
      // A swallowed user_bash scope error must never become local execution.
      verified = false;
      failure = guidance(`Cannot verify the session cwd: ${String(error)}`);
      refuse(ctx);
      return undefined;
    }
  }

  function refuse(ctx: ExtensionContext): void {
    try {
      if (reported !== failure) {
        reported = failure;
        // Synchronous stderr survives immediate exit and remains readable in
        // Radar's failed tmux pane, unlike an ephemeral startup UI notification.
        writeSync(2, `\n${failure}\n`);
        if (ctx.hasUI) ctx.ui.notify(failure, "error");
      }
    } finally {
      // Pi catches lifecycle errors. RPC shutdown waits until AFTER its next
      // command, which an older provider can claim before our user_bash blocker;
      // TUI shutdown exits with 0. This dedicated CLI launch prerequisite must
      // refuse all incompatible modes immediately with a retained failure pane.
      try {
        ctx.shutdown();
      } finally {
        process.exit(1);
      }
    }
  }

  function check(ctx: ExtensionContext): void {
    verified = false;
    if (inWorkspace(ctx) !== true) return;
    try {
      verify(pi);
      verified = true;
      reported = undefined;
    } catch (error) {
      failure = guidance(error instanceof Error ? error.message : String(error));
      refuse(ctx);
    }
  }

  // Install blockers synchronously from the factory, before any lifecycle hook.
  // Recheck verified metadata at execution boundaries to catch dynamic overrides.
  function blocked(ctx: ExtensionContext): boolean {
    const scoped = inWorkspace(ctx);
    if (scoped === false) return false;
    if (scoped === undefined) return true;
    if (verified) check(ctx);
    return !verified;
  }
  pi.on("tool_call", (_event, ctx) => {
    if (blocked(ctx)) return { block: true, reason: failure, terminate: true };
  });
  pi.on("user_bash", (_event, ctx) => {
    if (blocked(ctx)) {
      // A bare thrown user_bash handler is swallowed by supported older Pi
      // runtimes and falls back to local bash. Claim execution, then reject it.
      return { operations: { exec: async () => { throw new Error(failure); } } };
    }
  });
  pi.on("input", (_event, ctx) => {
    if (blocked(ctx)) {
      refuse(ctx);
      return { action: "handled" };
    }
  });
  pi.on("session_start", (_event, ctx) => { check(ctx); });
  // Follows every extension's session_start; also covers resources reload.
  pi.on("resources_discover", (_event, ctx) => { check(ctx); });
}
