# Configuration

[Documentation](README.md) · [Guided setup](installation.md#reconfiguring-with-radar-setup)

Start with **`radar setup`**. It prefills your current settings, preserves custom
layouts and unchanged secrets, and saves only after review. For settings outside
the wizard, edit the YAML file reported by:

```sh
radar config-path
```

The default is `$XDG_CONFIG_HOME/radar/config.yaml`, or
`~/.config/radar/config.yaml`. Setup preserves comments and unedited settings;
edits may normalize whitespace. The daemon and `config-path` do not create the
file or bypass onboarding.

## Files and credentials

| File | Purpose |
| --- | --- |
| `config.yaml` | Directories, tool settings and connection metadata |
| `secrets.yaml` beside it | Jira/Datadog credentials, stored with owner-only permissions |
| `AGENTS.md` beside it | Instructions for Radar-managed Pi sessions |
| `.radar.yaml` in a repository | Repository-specific copy/setup commands and runtime overrides |

Secrets are plaintext, not encrypted. They never appear in the config preview;
see [credential storage and environment overrides](installation.md#settings-and-secrets).
GitHub credentials stay with `gh`, and Pi provider authentication stays with Pi.

The installer creates the default `AGENTS.md` only if missing. Edit it to change
agent behavior for Radar workspace/resource management. Pi reads it before each
turn, so instruction changes need no restart.

## Directories and linking

A minimal directory configuration looks like:

```yaml
repository_dirs:
  - ~/code
workspace:
  root_dir: ~/.local/share/radar/workspaces
obsidian:
  vault_path: ~/Documents/Radar
linking_mark_prefixes:
  - ABC
```

- **`repository_dirs`**: existing source checkouts searched when adding a repository.
- **`workspace.root_dir`**: where Radar creates workspace anchors and their worktrees.
  Defaults to `$XDG_DATA_HOME/radar/workspaces`, or `~/.local/share/radar/workspaces`.
- **`obsidian.vault_path`**: the notes parent directory. Despite the setting's name,
  an ordinary directory is fine; Obsidian is optional. Radar stores tasks in its
  `Tasks/` folder. Setup creates a missing parent only after confirmation and never
  creates `.obsidian/`. Every new workspace needs a valid notes directory.
- **`linking_mark_prefixes`**: optional ticket prefixes, such as `ABC` for `ABC-123`.
  Omitted or `[]` disables only ticket-prefix linking; source, branch and workspace
  linking still work. Prefixes are uppercased, begin with a letter, and contain only
  letters/numbers. Radar matches complete `<PREFIX>-<NUMBER>` marks, not unrelated
  suffixes such as `Origin-096e274f`.

Changing directories does not move existing notes or workspaces. Old
`workspace_root` configs must be edited to use `workspace.root_dir`; there is no
legacy-key alias or automatic migration.

## Workspace and agent defaults

```yaml
workspace:
  auto_confirm: true
  cleanup:
    disposable_entries: []
model: github-copilot/claude-sonnet-4.5
thinking: medium
```

`workspace.auto_confirm` defaults to `true`: Pi's workspace tool previews and
validates its plan, then applies it without an extra prompt. Set it to `false`
to require interactive confirmation. Dirty-worktree and ownership checks still
apply. Existing explicit `false` settings remain unchanged.

`workspace.cleanup.disposable_entries` authorizes deletion of exact workspace-root
entries, not glob patterns. The default is empty. Read the
[cleanup policy](workspace-cleanup.md#disposable-workspace-root-entries) before
adding anything; completed registered workspaces also have an eight-day expiry.

`model` and `thinking` become Pi's `--model` and `--thinking` arguments for new
workspace sessions. Choose a model available through your Pi provider. A
repository's `.radar.yaml` can override these values. See
[repository setup](workspaces.md#repository-setup) and
[tmux layouts](integrations/tmux.md#workspace-layout) for commands and panes.

## Optional integrations

GitHub, Jira, Datadog and SBX each accept `enabled`:

| Value | Meaning |
| --- | --- |
| Omitted | Detect prerequisites at runtime |
| `false` | Explicitly disabled |
| `true` | Enabled; missing prerequisites are an error |

For example, `github: {enabled: false}` disables GitHub collection. Setup records
your explicit choices for GitHub, Jira and Datadog. API failures do not silently
disable an integration. See [activation rules](installation.md#optional-integrations)
for prerequisites and [repository precedence](installation.md#repository-precedence-and-existing-workspaces)
for SBX overrides and existing-workspace behavior.

Integration-specific configuration belongs in its guide:

- [GitHub tracking and PR/activity rules](integrations/github.md)
- [Jira issue types, status mapping and credentials](integrations/jira.md)
- [Datadog monitor scope and credentials](integrations/datadog.md)
- [Task notes and Obsidian](integrations/obsidian.md)
- [SBX kits, mounts, environment and readiness](integrations/sbx.md)

Existing configuration is never automatically migrated. Review the relevant
integration's rollout instructions when updating older settings.
