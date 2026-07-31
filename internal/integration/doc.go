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
//   - EntityID correlates representations of one external entity without linking tasks.
//   - Source and Kind are source-owned; labels and ordering come from the descriptor.
//   - Lifecycle classifies authoritative refs as work items, workspaces, or resources.
//   - CanonicalKey is the source-owned standalone identity for a task when present.
//   - LinkingKeys are source-owned hints used by core to join related refs.
//   - Metadata is opaque to core unless a core feature explicitly documents a key.
//
// Integrations compile source-specific semantics into observations, associations,
// presentation hints, and tool actions. Radar core consumes those generic facts and
// owns task state, linking, projection, filtering, and daemon protocol behavior.
package integration
