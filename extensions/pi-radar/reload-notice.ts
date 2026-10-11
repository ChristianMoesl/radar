import { realpathSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { getAgentDir, type ExtensionAPI, type ExtensionContext } from "@earendil-works/pi-coding-agent";
import { radarPackageVersion } from "./version.ts";

const statusKey = "radar-reload";
const packageName = "@christianmoesl/pi-radar";

// Follow the stable package link, not a pnpm version directory captured when
// this module loaded. Custom/local sources must not compare against another
// installation that happens to exist in the same user's npm profile.
export function installedPackageFile(profile: string, modulePath: string): string {
  const npmRoot = resolve(profile, "npm");
  let canonicalRoot = npmRoot;
  try { canonicalRoot = realpathSync(npmRoot); } catch { /* Missing/custom profile. */ }
  for (const root of [npmRoot, canonicalRoot]) {
    const path = relative(root, modulePath);
    if (!isAbsolute(path) && path !== ".." && !path.startsWith("../")) {
      return join(npmRoot, "node_modules", packageName, "package.json");
    }
  }
  return resolve(dirname(modulePath), "../../package.json");
}

// Compare SemVer precedence (including prereleases), ignoring build metadata.
// Invalid/partially-written manifests are not evidence that a reload is needed.
export function isNewerVersion(installed: unknown, loaded: string): boolean {
  const parse = (value: unknown) => {
    if (typeof value !== "string") return undefined;
    const match = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([\da-zA-Z-]+(?:\.[\da-zA-Z-]+)*))?(?:\+[\da-zA-Z-]+(?:\.[\da-zA-Z-]+)*)?$/.exec(value);
    if (!match) return undefined;
    const prerelease = match[4]?.split(".");
    if (prerelease?.some(part => /^\d+$/.test(part) && part.length > 1 && part[0] === "0")) return undefined;
    return { core: match.slice(1, 4).map(BigInt), prerelease };
  };
  const a = parse(installed), b = parse(loaded);
  if (!a || !b) return false;
  for (let i = 0; i < 3; i++) {
    if (a.core[i] !== b.core[i]) return a.core[i] > b.core[i];
  }
  if (!a.prerelease || !b.prerelease) return !a.prerelease && !!b.prerelease;
  for (let i = 0; i < Math.max(a.prerelease.length, b.prerelease.length); i++) {
    const x = a.prerelease[i], y = b.prerelease[i];
    if (x === y) continue;
    if (x === undefined || y === undefined) return y === undefined;
    const nx = /^\d+$/.test(x), ny = /^\d+$/.test(y);
    if (nx && ny) return BigInt(x) > BigInt(y);
    if (nx !== ny) return !nx;
    return x > y;
  }
  return false;
}

export async function showReloadNotice(
  pi: ExtensionAPI,
  ctx: ExtensionContext,
  packageFile = installedPackageFile(resolve(getAgentDir()), fileURLToPath(import.meta.url)),
) {
  // Background/RPC/print sessions have no interactive statusbar. This notice
  // never sends messages, invokes commands, or tries to determine agent idleness.
  if (ctx.mode !== "tui" || !ctx.hasUI) return;
  let stopped = false;
  let checking = false;
  let previous: string | undefined;
  const check = async () => {
    if (stopped || checking) return;
    checking = true;
    try {
      let newer = false;
      try {
        const pkg = JSON.parse(await readFile(packageFile, "utf8"));
        newer = pkg?.name === packageName && isNewerVersion(pkg.version, radarPackageVersion);
      } catch { /* Updates can temporarily remove/replace the package manifest. */ }
      if (stopped) return;
      const text = newer ? ctx.ui.theme.fg("error", "pi-radar updated · /reload to apply") : undefined;
      if (text !== previous) {
        ctx.ui.setStatus(statusKey, text);
        previous = text;
      }
    } finally {
      checking = false;
    }
  };
  // Do not keep a process alive just for an informational notice.
  const timer = setInterval(() => check().catch(() => {}), 30_000);
  timer.unref();
  pi.on("session_shutdown", async () => {
    stopped = true;
    clearInterval(timer);
    ctx.ui.setStatus(statusKey, undefined);
  });
  await check();
}
