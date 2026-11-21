// Build-time only: no downloads, package manager or lifecycle scripts at start.
import { cp, mkdtemp, mkdir, rm } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { join } from "node:path";
import { copyProductionGraph } from "./agent-runtime-graph.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const build = join(root, "build");
await mkdir(build, { recursive: true });
const stage = await mkdtemp(join(build, "agent-runtime-"));
function run(command, args, cwd = root, env = process.env) {
  const result = spawnSync(command, args, { cwd, env, stdio: "inherit" });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} exited ${result.status ?? result.signal}`);
}
try {
  const sdk = join(stage, "sdk");
  await mkdir(sdk);
  for (const file of ["package.json", "README.md", "dist"])
    await cp(join(root, "agent-host", file), join(sdk, file), { recursive: true });
  await cp(join(root, "LICENSE"), join(sdk, "LICENSE"));
  await copyProductionGraph(join(root, "agent-host"), sdk, join(root, "node_modules", ".pnpm"));
  // The packer runs on the build host, not the target selected for the CLI.
  const go = process.env.GO || "go";
  const host = spawnSync(go, ["env", "-json", "GOHOSTOS", "GOHOSTARCH"], { cwd: root, encoding: "utf8" });
  if (host.error) throw host.error;
  if (host.status !== 0) throw new Error("Cannot determine Go build-host platform");
  const { GOHOSTOS, GOHOSTARCH } = JSON.parse(host.stdout);
  if (!GOHOSTOS || !GOHOSTARCH) throw new Error("Missing Go build-host platform");
  run(go, ["run", "./internal/cmd/agentbundle", sdk,
    join(root, "internal/agentruntime/payload/runtime.zip")], root,
    { ...process.env, GOOS: GOHOSTOS, GOARCH: GOHOSTARCH });
} finally {
  await rm(stage, { recursive: true, force: true });
}
