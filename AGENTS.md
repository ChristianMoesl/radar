# Project Agent Guidelines

## Commits

Use conventional commits for commit messages, for example `feat: add tmux source` or `fix: collect tmux panes without ticket keys`.

After making a commit, make sure it lands on remote `main` immediately.

Default target: `origin/main`. Unless the user explicitly asks for a different remote or branch, merge or fast-forward the committed work into `origin/main` and push that. Do not stop after pushing only the current feature/worktree branch; intermediate branches such as `origin/improvements` are not important unless needed as part of getting the commit onto `origin/main`.

## Streamlined product design

Build Radar as a streamlined tool with one clear way to do each task.

Limit optional alternatives wherever possible. Do not add duplicate command paths, aliases, parallel workflows, configuration switches, or fallback behavior unless the user explicitly asks for them or there is a strong product reason.

Prefer simple, opinionated flows over broad configurability. When a new capability overlaps with an existing one, replace or reshape the existing path rather than adding another way to do the same thing.

## No backwards compatibility shims

Do not add backwards compatibility code unless the user explicitly asks for it.

This project prefers clean model changes over compatibility layers. When renaming or reshaping domain concepts, update the code and tests to the new model directly. Do not add legacy JSON aliases, migration fallbacks, old field readers, compatibility command paths, or similar shims "just in case".

If a compatibility concern comes up, ask before implementing it.
