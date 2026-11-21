import { registerHooks } from "node:module";
import { fileURLToPath } from "node:url";

// Only Radar's public namespace is redirected. Resolve everything else with its
// original parent URL, including third-party packages owned by the definition.
export function installSdkResolution(): ReturnType<typeof registerHooks> {
  const extension = import.meta.url.endsWith(".ts") ? ".ts" : ".js";
  const exports = new Map([
    ["@christianmoesl/radar-agent", "index"],
    ["@christianmoesl/radar-agent/tools", "tools"],
    ["@christianmoesl/radar-agent/adapters", "adapters"],
    ["@christianmoesl/radar-agent/mcp", "mcp"],
    ["@christianmoesl/radar-agent/mcp-tools", "mcp-tools"],
  ]);
  return registerHooks({
    resolve(specifier, context, nextResolve) {
      const file = exports.get(specifier);
      if (file)
        return nextResolve(fileURLToPath(new URL(`./${file}${extension}`, import.meta.url)), context);
      if (specifier.startsWith("@christianmoesl/radar-agent/")) {
        const error = new Error(`Unsupported Radar agent SDK subpath: ${specifier}`);
        Object.assign(error, { code: "ERR_PACKAGE_PATH_NOT_EXPORTED" });
        throw error;
      }
      return nextResolve(specifier, context);
    },
  });
}
