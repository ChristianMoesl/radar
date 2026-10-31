# Radar agent host

Code-defined [pi-durable](https://github.com/earendil-works/pi/tree/main/packages/durable) agents supervised by Radar. Requires Node 24+ and a Radar CLI supporting agent protocol 1.

Every registered agent belongs to a Radar workspace. Its workspace tools use that workspace's SBX sandbox when enabled. The definition supplies native tasks, models, tools and domain policy; Radar supplies durable event admission, supervision, interaction and workspace execution.

Install this package in the definition's project. Export an `AgentDefinition` as the module's default export, then:

```sh
radar agent add ./agent.ts --id assistant --workspace /path/to/registered/workspace --config ./config.json
radar agent start assistant
printf '%s' '{"type":"example.event"}' | radar agent emit assistant --source fixture --event-id one
radar agent inspect assistant
```

Exports:

- `@christianmoesl/radar-agent-host`: native pi-durable APIs, `AgentDefinition`, event/context types, `present`, `WorkspaceExecution` and `HostContext.mcp`.
- `@christianmoesl/radar-agent-host/tools`: prepared workspace operations, read-only tools, active workspace leases.
- `@christianmoesl/radar-agent-host/mcp`: host-side MCP connections and the `radar-mcp login/tools` command.
- `@christianmoesl/radar-agent-host/adapters`: generic event forwarding and signed GitHub webhook bridge.

Editable examples are in `dist/examples`: counter, GitHub review assistant, Renovate review-agent tagging, Jira discussion assistant, and Datadog investigator. Model-based examples need configured credentials/model selection or explicit `fixture:true` for a deterministic model. Renovate tagging needs no model: subscribe with `--collection github` and confirm its author, exact comment and explicit backlog policy before activation.

Read the [complete development guide](https://github.com/ChristianMoesl/radar/blob/main/docs/durable-agents.md) for the authoring example, installation from a local tarball, event/interaction contract, SBX boundary, retention, recovery guarantees, supported adapter operations and publishing.

Remote services default to host MCP profiles; workspace files/builds remain in the selected local/SBX execution environment. See [MCP setup and manual agent cutover](https://github.com/ChristianMoesl/radar/blob/main/docs/agent-mcp.md). No provider CLI/session is required in the sandbox.
