# Radar agent host

Code-defined [pi-durable](https://github.com/earendil-works/pi/tree/main/packages/durable) agents supervised by Radar. Requires Node 24+ and a Radar CLI supporting agent protocol 1.

Every registered agent belongs to a Radar workspace. Its workspace tools use that workspace's SBX sandbox when enabled. A short `defineAgent({ name, instructions, model, mcp, limits? })` definition gets ordinary durable event-to-conversation execution, exact selected tools, bounded investigation and persisted result/error presentation. The low-level native tasks/models/tools API remains available for custom workflows. Radar supplies durable event admission, supervision, interaction and workspace execution.

Install this package in the definition's project. Export an `AgentDefinition` as the module's default export, then:

```sh
radar agent add ./agent.ts --id assistant --workspace /path/to/registered/workspace --config ./config.json
radar agent start assistant
printf '%s' '{"type":"example.event"}' | radar agent emit assistant --source fixture --event-id one
radar agent inspect assistant
```

Exports:

- `@christianmoesl/radar-agent-host`: `defineAgent`, `SimpleAgentOptions`, `AgentLimits`, native pi-durable APIs, `AgentDefinition`, event/context types, `present`, `WorkspaceExecution` and `HostContext.mcp`.
- `@christianmoesl/radar-agent-host/tools`: prepared workspace operations, read-only tools, active workspace leases.
- `@christianmoesl/radar-agent-host/mcp`: trusted host-side MCP connections and the `radar-mcp login/tools` command.
- `@christianmoesl/radar-agent-host/mcp-tools`: selected native durable MCP tools routed through the associated local/SBX workspace, strict schemas and non-secret execution/tool inspection.
- `@christianmoesl/radar-agent-host/adapters`: generic event forwarding and signed GitHub webhook bridge.

Start with `dist/examples/datadog-simple.js`: a small tool-using definition with explicit host MCP selection, built-in model resolution and no hand-coded collection pipeline. Configure model/provider authentication once in the runtime host environment, optionally using an explicitly selected private `RADAR_AGENT_AUTH_FILE`; no interactive/other-agent store is discovered or copied. See the guide for limits, recovery, credential refresh and explicit workspace versus host-service placement.

Editable advanced examples are in `dist/examples`: counter, GitHub review assistant, Renovate review-agent tagging, Jira discussion assistant, and Datadog investigator. Model-based examples need configured credentials/model selection or explicit `fixture:true` for a deterministic model. Renovate tagging needs no model: subscribe with `--collection github` and confirm its author, exact comment and explicit backlog policy before activation.

Read the [complete development guide](https://github.com/ChristianMoesl/radar/blob/main/docs/durable-agents.md) for the authoring example, installation from a local tarball, event/interaction contract, SBX boundary, retention, recovery guarantees, supported adapter operations and publishing.

Existing service definitions use trusted host MCP profiles. Definitions requiring workspace-routed MCP use `createMcpTools(host.execution, host.config.mcp)` with explicit configuration/state paths and selected server/tool pairs; the selected environment must own its package installation and read-only MCP session. No host credential store is copied and unavailable SBX never falls back to host execution. Workspace files/builds continue through the same local/SBX boundary. See [MCP setup and manual agent cutover](https://github.com/ChristianMoesl/radar/blob/main/docs/agent-mcp.md).
