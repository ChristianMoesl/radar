import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { promisify } from "node:util";
import test from "node:test";

const exec = promisify(execFile);
const loader = new URL("../src/sdk-resolution.ts", import.meta.url).href;
async function run(entry: string) {
  return exec(process.execPath, ["--input-type=module", "--eval", `
    import { installSdkResolution } from ${JSON.stringify(loader)};
    installSdkResolution();
    await import(${JSON.stringify(pathToFileURL(entry).href)});
  `]);
}

test("SDK and every supported subpath load outside any package without project mutation", async () => {
  const dir = await mkdtemp(join(tmpdir(), "radar-sdk-"));
  try {
    const metadata = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
    const imports = Object.keys(metadata.exports).map((key) =>
      `await import(${JSON.stringify("@christianmoesl/radar-agent" + (key === "." ? "" : key.slice(1)))});`).join("\n");
    await writeFile(join(dir, "helper.ts"), `export { present } from "@christianmoesl/radar-agent";`);
    await writeFile(join(dir, "agent.ts"), `
      import assert from "node:assert/strict";
      import { present } from "./helper.ts";
      import { present as supplied } from "@christianmoesl/radar-agent";
      assert.equal(present, supplied);
      ${imports}
    `);
    await run(join(dir, "agent.ts"));
    assert.deepEqual((await readdir(dir)).sort(), ["agent.ts", "helper.ts"]);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("only the SDK is redirected; project dependencies and CommonJS keep their original parent", async () => {
  const dir = await mkdtemp(join(tmpdir(), "radar-sdk-deps-"));
  try {
    const thirdParty = join(dir, "node_modules", "example-dependency");
    const shadow = join(dir, "node_modules", "@christianmoesl", "radar-agent");
    for (const packageDir of [thirdParty, shadow]) await mkdir(packageDir, { recursive: true });
    await writeFile(join(thirdParty, "package.json"), JSON.stringify({ name: "example-dependency", main: "index.cjs" }));
    await writeFile(join(thirdParty, "index.cjs"), "module.exports = { value: 'project-owned' };");
    await writeFile(join(shadow, "package.json"), JSON.stringify({ name: "@christianmoesl/radar-agent", main: "index.cjs" }));
    await writeFile(join(shadow, "index.cjs"), "throw Error('must not load project SDK');");
    await writeFile(join(dir, "agent.mjs"), `
      import assert from "node:assert/strict";
      import { createRequire } from "node:module";
      import thirdParty from "example-dependency";
      import { RpcError } from "@christianmoesl/radar-agent";
      assert.equal(thirdParty.value, "project-owned");
      const require = createRequire(import.meta.url);
      assert.equal(require("example-dependency").value, "project-owned");
      assert.equal(require("@christianmoesl/radar-agent").RpcError, RpcError);
      await assert.rejects(import("@christianmoesl/radar-agent/internal"), {code: "ERR_PACKAGE_PATH_NOT_EXPORTED"});
      await assert.rejects(import("not-installed-example-dependency"), {code: "ERR_MODULE_NOT_FOUND"});
    `);
    const before = await readdir(dir, { recursive: true });
    await run(join(dir, "agent.mjs"));
    assert.deepEqual(await readdir(dir, { recursive: true }), before);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
