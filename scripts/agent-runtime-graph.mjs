import { cp, mkdir, readFile, realpath, symlink } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, isAbsolute, join, relative } from "node:path";

// Copy only production-reachable packages from the already installed, frozen
// workspace graph. Never re-resolve registry ranges or run dependency scripts.
export async function copyProductionGraph(source, output, store) {
  store = await realpath(store);
  const suppliedStore = join(output, "node_modules", ".pnpm");
  const copied = new Map();
  const metadata = async dir => JSON.parse(await readFile(join(dir, "package.json"), "utf8"));
  const contained = path => path !== "" && path !== ".." && !path.startsWith("../") && !isAbsolute(path);

  async function resolvePackage(parent, name) {
    const paths = createRequire(join(parent, "package.json")).resolve.paths(name) ?? [];
    for (const dir of paths) {
      try { return await realpath(join(dir, name)); }
      catch (error) { if (error.code !== "ENOENT") throw error; }
    }
  }
  async function edges(parent, target, data) {
    const names = new Set([...Object.keys(data.dependencies ?? {}), ...Object.keys(data.optionalDependencies ?? {}),
      ...Object.keys(data.peerDependencies ?? {})]);
    for (const name of names) {
      if (!/^(?:@[a-zA-Z0-9._-]+\/)?[a-zA-Z0-9._-]+$/.test(name) || name === "." || name === "..")
        throw new Error(`Invalid dependency name: ${name}`);
      const resolved = await resolvePackage(parent, name);
      if (!resolved) {
        const optional = name in (data.optionalDependencies ?? {}) ||
          (!(name in (data.dependencies ?? {})) && data.peerDependenciesMeta?.[name]?.optional);
        if (optional) continue;
        throw new Error(`Missing installed production dependency: ${name}; run pnpm install --frozen-lockfile`);
      }
      const destination = await visit(resolved);
      const link = join(target, "node_modules", name);
      await mkdir(dirname(link), { recursive: true });
      await symlink(relative(dirname(link), destination), link);
    }
  }
  async function visit(dir) {
    const path = relative(store, dir);
    if (!contained(path)) throw new Error(`Production dependency outside workspace store: ${dir}`);
    if (copied.has(dir)) return copied.get(dir);
    const target = join(suppliedStore, path);
    copied.set(dir, target);
    await cp(dir, target, { recursive: true, verbatimSymlinks: true,
      filter: file => file !== join(dir, "node_modules") });
    await edges(dir, target, await metadata(dir));
    return target;
  }
  await edges(source, output, await metadata(source));
}
