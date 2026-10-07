import { readFile } from "node:fs/promises";

const tag = process.argv[2];
const { version } = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
// Release tags share the CLI and npm package version. Build metadata is not
// supported because npm does not treat it as a distinct published version.
const number = "(?:0|[1-9][0-9]*)";
const identifier = `(?:${number}|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`;
const semver = new RegExp(`^${number}\\.${number}\\.${number}(?:-${identifier}(?:\\.${identifier})*)?$`);

if (typeof version !== "string" || !semver.test(version)) {
  console.error(`package.json version must be a release version without build metadata, got: ${version}`);
  process.exit(1);
}
if (tag !== `v${version}`) {
  console.error(`release tag must match package.json version: expected v${version}, got: ${tag ?? "<missing>"}`);
  process.exit(1);
}

const moduleText = await readFile(new URL("../extensions/pi-radar/version.ts", import.meta.url), "utf8");
if (!moduleText.includes(`export const radarPackageVersion = "${version}";`)) {
  console.error("extensions/pi-radar/version.ts must match package.json (loaded-version reporting)");
  process.exit(1);
}
console.log(`Validated release ${tag}`);
