import { lstat, readFile, writeFile } from "node:fs/promises";

// Shared by local release preparation and the read-only CI check. Build metadata
// is excluded because npm does not treat it as a distinct published version.
const number = "(?:0|[1-9][0-9]*)";
const identifier = `(?:${number}|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`;
const semver = new RegExp(`^${number}\\.${number}\\.${number}(?:-${identifier}(?:\\.${identifier})*)?$`);
const declaration = /^export const radarPackageVersion = "([^"\r\n]*)";$/gm;

async function main(mode, tag) {
  if (!["validate", "prepare", "check"].includes(mode)) throw new Error("usage: release-version.mjs <validate|prepare|check> vX.Y.Z");
  const target = tag?.startsWith("v") ? tag.slice(1) : undefined;
  if (mode !== "check" && (!target || !semver.test(target))) {
    throw new Error(`release version must be vX.Y.Z (optional prerelease, no build metadata), got: ${tag ?? "<missing>"}`);
  }
  if (mode === "validate") return;

  const manifestPath = new URL("../package.json", import.meta.url);
  const modulePath = new URL("../extensions/pi-radar/version.ts", import.meta.url);
  for (const path of [manifestPath, modulePath]) {
    if (!(await lstat(path)).isFile()) throw new Error(`release version file must be a regular file: ${path.pathname}`);
  }
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  if (!manifest || typeof manifest !== "object" || Array.isArray(manifest)) throw new Error("package.json must contain an object");
  const moduleText = await readFile(modulePath, "utf8");
  const declarations = [...moduleText.matchAll(declaration)];
  if (declarations.length !== 1) throw new Error("extensions/pi-radar/version.ts must contain exactly one literal radarPackageVersion declaration");

  if (mode === "check") {
    if (typeof manifest.version !== "string" || !semver.test(manifest.version)) {
      throw new Error(`package.json version must be a release version without build metadata, got: ${manifest.version}`);
    }
    if (tag !== `v${manifest.version}`) {
      throw new Error(`release tag must match package.json version: expected v${manifest.version}, got: ${tag ?? "<missing>"}`);
    }
    if (declarations[0][1] !== manifest.version) {
      throw new Error("extensions/pi-radar/version.ts must match package.json (loaded-version reporting)");
    }
    console.log(`Validated release ${tag}`);
    return;
  }

  // Validate both inputs before writing either. Preserve unrelated manifest
  // fields and module contents, and don't dirty already-aligned files.
  if (manifest.version !== target) {
    manifest.version = target;
    await writeFile(manifestPath, JSON.stringify(manifest, null, 2) + "\n");
  }
  if (declarations[0][1] !== target) {
    await writeFile(modulePath, moduleText.replace(declaration, `export const radarPackageVersion = "${target}";`));
  }
  console.log(`Prepared release ${tag}`);
}

try {
  await main(process.argv[2], process.argv[3]);
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
