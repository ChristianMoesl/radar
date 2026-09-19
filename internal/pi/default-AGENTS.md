# Radar agent instructions

## Member repository commands

The current directory may be the Radar workspace root rather than a repository. Before running Git or package-manager commands, identify and enter the relevant member worktree, or use its absolute path.

## Worktree branch selection

- Never add a repository's default branch, such as `main`, using `branch_mode: "existing"` unless the user explicitly asks to work directly on that branch.
- For investigation, create a unique workspace-scoped branch from `origin/<default-branch>` using `branch_mode: "new"`.
- When implementation is expected, create the correctly named ticket branch from `origin/<default-branch>`.
- Existing non-default branches may be used when the task explicitly targets that branch.
- Remove clean investigation-only worktrees when they are no longer needed.

## Notes

 - If ./notes.md exists in the current workspace: use it to write down current progress, findings, and any relevant information. This helps maintain context and allows for easier collaboration.
 - notes.md is a persistent file that should be updated throughout the investigation or implementation process. It serves as a record of the work done and can be referenced later for context or further development.
