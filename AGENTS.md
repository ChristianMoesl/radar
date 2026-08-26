# Project Agent Guidelines

## Commits

Use conventional commits for commit messages, for example `feat: add tmux source` or `fix: collect tmux panes without ticket keys`.

For non-trivial changes, add a commit body that explains the motivation and the important behavior or design decisions. Do not merely restate the subject or list changed files. A subject-only message is acceptable only for a small, self-explanatory change.

After making a commit, make sure it lands on remote `main` immediately.

After the committed work has been pushed to `origin/main`, run `make install` so the local `radar` binary is up to date.

## Streamlined product design

Build Radar as a streamlined tool with one clear way to do each task.

Limit optional alternatives wherever possible. Do not add duplicate command paths, aliases, parallel workflows, configuration switches, or fallback behavior unless the user explicitly asks for them or there is a strong product reason.

Prefer simple, opinionated flows over broad configurability. When a new capability overlaps with an existing one, replace or reshape the existing path rather than adding another way to do the same thing.

## No backwards compatibility shims

Do not add backwards compatibility code unless the user explicitly asks for it.

This project prefers clean model changes over compatibility layers. When renaming or reshaping domain concepts, update the code and tests to the new model directly. Do not add legacy JSON aliases, migration fallbacks, old field readers, compatibility command paths, or similar shims "just in case".

If a compatibility concern comes up, ask before implementing it.

Apply the same rule to user configuration. Update its schema directly and require users to edit existing configuration themselves. Do not add legacy keys, aliases, or automatic user-config migrations.

## Persisted data rollouts

When changing an on-disk schema or integration-owned file layout, treat rollout validation separately from code tests. Before declaring the change install-ready:

- inventory existing local data for every affected format;
- identify whether each format is migrated, reset, or intentionally rejected;
- perform a read-only preflight against the configured local state;
- do not install an incompatible binary until the user has chosen the handling for existing data.

This does not require backwards-compatibility code. Prefer an explicit one-time migration or documented reset when compatibility shims are not wanted.

## Test and documentation data

Use neutral identifiers such as `ABC-123` in tests, documentation, and examples. Do not embed organization-specific ticket prefixes.

## Local instructions

If `AGENTS.local.md` exists, read and follow it.
