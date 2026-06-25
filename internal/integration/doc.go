// Package integration defines Radar's source-compiled integration boundary.
//
// Radar is one Go binary with explicit integrations compiled into it. This
// package is not a plugin API: there is no discovery mechanism, dynamic loading,
// manifest format, or subprocess protocol. Core packages depend on these small
// capability interfaces while source packages own source-specific facts and tool
// actions.
//
// Source refs are the stable fact boundary between integrations and core:
//   - ID is globally stable and owned by the integration.
//   - Source is the integration name, such as github, jira, git, tmux, or sbx.
//   - Kind is source-owned, such as pull_request, issue, worktree, session, or sandbox.
//   - CanonicalKey is the source-owned standalone identity for a task when present.
//   - LinkingKeys are source-owned hints used by core to join related refs.
//   - Metadata is opaque to core unless a core feature explicitly documents a key.
//
// Integrations produce observations and tool actions. Radar core owns task state,
// linking, projection, filtering, presentation, and daemon protocol behavior.
package integration
