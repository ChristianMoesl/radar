// Explicit file access is distinct from registration-only workspace_policy.
export const repositoryPolicyLimitations = [
  "explicit_exact_registered_member_repository_configuration_only",
  "current_declarations_not_historical_recipe_or_effective_policy",
  "no_global_merge_referenced_file_runtime_provider_or_executor_calls",
  "absent_configuration_is_not_unavailable_repository",
  "private_values_paths_commands_and_file_hashes_withheld",
  "snapshot_binds_public_shape_not_private_configuration",
  "point_in_time_file_and_registration_checks_not_future_reservation",
];
export const repositoryPolicyKeys = ["copy_files", "setup", "model", "thinking", "sbx.enabled", "sbx.ready_command", "sbx.env_file", "sbx.kit.name", "sbx.kit.path", "sbx.additional_mounts"];
export function validateRepositoryPolicy(data: any, params: {parent_id?: string; id?: string; scope?: string}): void {
  const fail = (): never => { throw new Error("Invalid Radar current repository declaration evidence"); };
  const object = (v: any, keys: string[]): boolean => v !== null && typeof v === "object" && !Array.isArray(v) && Object.keys(v).every(key => keys.includes(key));
  const text = (v: any, max = 512): boolean => typeof v === "string" && v.length > 0 && Buffer.byteLength(v, "utf8") <= max;
  const integer = (v: any): boolean => Number.isSafeInteger(v) && v >= 0;
  const scope = params.scope ?? "origin";
  if (!object(data, ["diagnostics_schema", "section", "scope", "available", "feedback_workspace_id", "origin_workspace_id", "evidence", "reason", "inventory"]) || data.diagnostics_schema !== 41 || data.section !== "repository_policy" || data.scope !== scope || !text(data.feedback_workspace_id) || (data.origin_workspace_id !== undefined && (scope !== "origin" || !text(data.origin_workspace_id)))) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.inventory !== undefined || !["origin_not_registered", "daemon_unavailable", "snapshot_changed_restart_pagination"].includes(data.reason) || (data.reason === "origin_not_registered" && (scope !== "origin" || data.origin_workspace_id !== undefined))) fail();
    return;
  }
  const evidence = "explicit_current_registered_member_repository_declarations", page = data.inventory;
  if (data.available !== true || data.reason !== undefined || data.evidence !== evidence || (scope === "origin" && data.origin_workspace_id !== params.parent_id) || (scope === "feedback" && data.feedback_workspace_id !== params.parent_id) || !object(page, ["diagnostics_schema", "section", "evidence", "observed_at", "revision", "snapshot", "total", "items", "next_cursor", "pagination_complete", "resource_refresh_pending", "limitations"])) fail();
  if (page.diagnostics_schema !== 41 || page.section !== "repository_policy" || page.evidence !== evidence || !text(page.observed_at,35) || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(page.observed_at) || page.observed_at.startsWith("0000-") || !Number.isFinite(Date.parse(page.observed_at)) || new Date(page.observed_at).toISOString().slice(0,19) !== page.observed_at.slice(0,19) || !integer(page.revision) || !text(page.snapshot,64) || !/^[a-f0-9]{64}$/.test(page.snapshot) || typeof page.resource_refresh_pending !== "boolean" || page.total !== 1 || !Array.isArray(page.items) || page.items.length !== 1 || page.pagination_complete !== true || page.next_cursor !== undefined || !Array.isArray(page.limitations) || page.limitations.length !== repositoryPolicyLimitations.length || new Set(page.limitations).size !== repositoryPolicyLimitations.length || page.limitations.some((v: any) => !repositoryPolicyLimitations.includes(v)) || Buffer.byteLength(JSON.stringify(page),"utf8") > 32*1024) fail();
  const row = page.items[0], view = row?.repository_policy;
  if (!object(row,["id","repository_policy"]) || row.id !== params.id || !object(view,["id","workspace_id","origin","available","evidence","reason","settings"]) || view.id !== params.id || view.workspace_id !== params.parent_id || view.origin !== "current_registered_member_repository" || !Array.isArray(view.settings)) fail();
  if (view.available === false) {
    if (view.evidence !== "unavailable" || view.settings.length !== 0 || !["repository_unavailable", "configuration_read_unavailable", "configuration_not_regular_or_exceeds_bound", "configuration_changed_or_read_incomplete", "configuration_invalid", "registration_changed_or_unavailable", "cancelled_or_timed_out"].includes(view.reason)) fail();
    return;
  }
  if (view.available !== true || view.reason !== undefined || !["fresh_bounded_repository_configuration","repository_configuration_absent"].includes(view.evidence) || view.settings.length !== repositoryPolicyKeys.length) fail();
  const seen = new Set<string>();
  for (const setting of view.settings) {
    if (!object(setting,["key","value","origin","declaration"]) || !repositoryPolicyKeys.includes(setting.key) || seen.has(setting.key) || !["absent","null","value"].includes(setting.declaration) || setting.origin !== (setting.declaration === "value" ? "repository_file" : "repository_default")) fail();
    seen.add(setting.key);
    const count = ["copy_files","setup","sbx.additional_mounts"].includes(setting.key), mode = setting.key === "sbx.enabled";
    if (typeof setting.value !== "string" || (count ? !/^(0|[1-9]\d*)$/.test(setting.value) || !integer(Number(setting.value)) : mode ? !["inherit","required","disabled"].includes(setting.value) : !["true","false"].includes(setting.value))) fail();
    if (setting.declaration !== "value" && setting.value !== (count ? "0" : mode ? "inherit" : "false")) fail();
    if (view.evidence === "repository_configuration_absent" && setting.declaration !== "absent") fail();
  }
}
