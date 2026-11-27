// Registration is a recipe source, never runtime observation or action consent.
export const workspacePolicyLimitations = [
  "exact_registered_workspace_recipe_only",
  "not_current_repository_or_global_effective_policy",
  "historical_setting_origins_unknown",
  "runtime_presence_configuration_and_health_not_probed",
  "ownership_leases_and_execution_eligibility_not_checked",
  "no_repository_or_referenced_file_reads_or_maintenance",
  "private_recipe_values_paths_and_commands_withheld",
  "snapshot_binds_public_shape_not_private_recipe_values",
  "single_registration_snapshot_not_future_state_reservation",
];

export function validateWorkspacePolicy(data: any, params: {parent_id?: string; scope?: string}): void {
  const fail = (): never => { throw new Error("Invalid Radar registered workspace policy evidence"); };
  const object = (value: any, keys: string[]): boolean => value !== null && typeof value === "object" && !Array.isArray(value) && Object.keys(value).every(key => keys.includes(key));
  const text = (value: any, max = 512): boolean => typeof value === "string" && value.length > 0 && Buffer.byteLength(value, "utf8") <= max;
  const integer = (value: any): boolean => Number.isSafeInteger(value) && value >= 0;
  const scope = params.scope ?? "origin";
  if (!object(data, ["diagnostics_schema", "section", "scope", "available", "feedback_workspace_id", "origin_workspace_id", "evidence", "reason", "inventory"]) || data.diagnostics_schema !== 41 || data.section !== "workspace_policy" || data.scope !== scope || !text(data.feedback_workspace_id) || (data.origin_workspace_id !== undefined && (scope !== "origin" || !text(data.origin_workspace_id)))) fail();
  if (data.available === false) {
    if (data.evidence !== "unavailable" || data.inventory !== undefined || !["origin_not_registered", "daemon_unavailable", "snapshot_changed_restart_pagination"].includes(data.reason) || (data.reason === "origin_not_registered" && (scope !== "origin" || data.origin_workspace_id !== undefined))) fail();
    return;
  }
  const page = data.inventory;
  const evidence = "fresh_registration_recorded_recipe_not_runtime";
  if (data.available !== true || data.reason !== undefined || data.evidence !== evidence || (scope === "origin" && data.origin_workspace_id !== params.parent_id) || (scope === "feedback" && data.feedback_workspace_id !== params.parent_id) || !object(page, ["diagnostics_schema", "section", "evidence", "observed_at", "revision", "snapshot", "total", "items", "next_cursor", "pagination_complete", "resource_refresh_pending", "limitations"])) fail();
  if (page.diagnostics_schema !== 41 || page.section !== "workspace_policy" || page.evidence !== evidence || !text(page.observed_at, 35) || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(page.observed_at) || page.observed_at.startsWith("0000-") || !Number.isFinite(Date.parse(page.observed_at)) || new Date(page.observed_at).toISOString().slice(0,19) !== page.observed_at.slice(0,19) || !integer(page.revision) || !text(page.snapshot,64) || !/^[a-f0-9]{64}$/.test(page.snapshot) || typeof page.resource_refresh_pending !== "boolean" || page.total !== 1 || !Array.isArray(page.items) || page.items.length !== 1 || page.pagination_complete !== true || page.next_cursor !== undefined || !Array.isArray(page.limitations) || page.limitations.length !== workspacePolicyLimitations.length || new Set(page.limitations).size !== workspacePolicyLimitations.length || page.limitations.some((v: any) => !workspacePolicyLimitations.includes(v)) || Buffer.byteLength(JSON.stringify(page), "utf8") > 32 * 1024) fail();
  const row = page.items[0], view = row?.workspace_policy;
  if (!object(row,["id","workspace_policy"]) || row.id !== params.parent_id || !object(view,["id","origin","sandbox_registered","sandbox"]) || view.id !== params.parent_id || view.origin !== "recorded_workspace_registration" || typeof view.sandbox_registered !== "boolean") fail();
  if (!view.sandbox_registered) {
    if (view.sandbox !== undefined) fail();
    return;
  }
  const recipe = view.sandbox;
  const flags = ["kit_configured","kit_path_configured","env_file_configured","ready_command_configured"];
  const counts = ["managed_mount_count","additional_mount_count","port_count"];
  if (!object(recipe,[...flags,...counts]) || flags.some(key => typeof recipe[key] !== "boolean") || counts.some(key => !integer(recipe[key]))) fail();
}
