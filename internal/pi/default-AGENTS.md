# Radar agent instructions

## Worktree branch selection

- Never add a repository's default branch, such as `main`, using `branch_mode: "existing"` unless the user explicitly asks to work directly on that branch.
- For investigation, create a unique workspace-scoped branch from `origin/<default-branch>` using `branch_mode: "new"`.
- When implementation is expected, create the correctly named ticket branch from `origin/<default-branch>`.
- Existing non-default branches may be used when the task explicitly targets that branch.
- Remove clean investigation-only worktrees when they are no longer needed.
