# Radar agent instructions

## Member repository commands

The current directory may be the Radar workspace root rather than a repository. Before running Git or package-manager commands, identify and enter the relevant member worktree, or use its absolute path.

## Worktree branch selection

- Never add a repository's default branch, such as `main`, using `branch_mode: "existing"` unless the user explicitly asks to work directly on that branch.
- For investigation, create a unique workspace-scoped branch from `origin/<default-branch>` using `branch_mode: "new"`.
- When implementation is expected, create the correctly named ticket branch from `origin/<default-branch>`.
- Existing non-default branches may be used when the task explicitly targets that branch.
- Remove clean investigation-only worktrees when they are no longer needed.

## User-visible files and artifacts

- Before linking or showing any file, visual, or artifact to the user in a Pi session, save it inside the current Radar workspace or the workspace's shared temporary directory. This includes screenshots, images, HTML presentations, PDFs, reports, and other deliverables. Files elsewhere cannot be opened by the user.
- For temporary outputs, use `sandbox.shared_directory` returned by `radar_workspace_context` only when `sandbox.shared_directory_ready` is true. Pass that output path explicitly to generation tools and use unique filenames. Sandbox-local `/tmp`, `$TMPDIR`, and host-only directories are not substitutes for the shared directory.
- Keep deliverables worth retaining in the workspace; the shared temporary directory is removed during workspace cleanup. If the shared directory is unavailable, save outputs in the workspace or reconcile the workspace and reload Pi before using it.
- If an artifact was created elsewhere, copy it into an allowed location before presenting it. Verify that the file exists there and link to that accessible copy, not the original path.
- Always use the file's full absolute path as the link target when linking to workspace or shared temporary directory files. Never use relative paths, `~`, or environment-variable placeholders in link targets.

## Notes

 - If ./notes.md exists in the current workspace: use it to write down current progress, findings, and any relevant information. This helps maintain context and allows for easier collaboration.
 - notes.md is a persistent file that should be updated throughout the investigation or implementation process. It serves as a record of the work done and can be referenced later for context or further development.

## Sandbox attachment

- Inspect the latest context and preserve the complete desired object. For an existing unsandboxed workspace, request attachment only explicitly and only when `sandbox_attachment` is true; otherwise retain `sandbox: null`.
- An attached sandbox cannot be detached through reconciliation. After a retryable failure, inspect fresh context before retrying; recorded intent is not proof of runtime readiness.
- Native Linux/WSL provisioning requires the supported SBX CLI, accessible KVM and the WSL cache policy. Do not bypass failed preflight, edit the registry, switch to Windows SBX or run sandbox-intended setup on the host.
