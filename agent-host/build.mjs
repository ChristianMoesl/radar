import { cp, chmod, rm } from "node:fs/promises";
import { spawnSync } from "node:child_process";

await rm(new URL("dist", import.meta.url), { recursive: true, force: true });
const result = spawnSync("tsc", ["-p", "tsconfig.build.json"], {
  stdio: "inherit",
});
if (result.status !== 0) process.exit(result.status ?? 1);
await cp(
  new URL("examples/review", import.meta.url),
  new URL("dist/examples/review", import.meta.url),
  { recursive: true },
);
await chmod(new URL("dist/src/runner.js", import.meta.url), 0o755);
