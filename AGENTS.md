# Project Agent Guidelines

## Commits

Use conventional commits for commit messages, for example `feat: add tmux source` or `fix: collect tmux panes without ticket keys`.

Always push to the main remote (`origin`) unless the user explicitly asks for a different remote.

## No backwards compatibility shims

Do not add backwards compatibility code unless the user explicitly asks for it.

This project prefers clean model changes over compatibility layers. When renaming or reshaping domain concepts, update the code and tests to the new model directly. Do not add legacy JSON aliases, migration fallbacks, old field readers, compatibility command paths, or similar shims "just in case".

If a compatibility concern comes up, ask before implementing it.
