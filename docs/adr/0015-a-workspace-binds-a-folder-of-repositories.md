# ADR-0015: A workspace binds a folder of repositories once

**Status:** Accepted (2026-11-23, at the user's request for enterprise
layouts: "they create 1 main folder with the project name, under it
frontend, backend etc., each with its own git; they want to init atlas 1 time
in the mother folder and run agents under frontend, backend"). Amends
ADR-0007.

## Context

ADR-0007 stops Project discovery at the scope root, the git repository that
owns the cwd. In the layout above the mother folder is not a git repository
and each child is, so an agent started in `acme-shop/backend` never finds a
binding in `acme-shop/` and reports "not initialised". Walking past the
scope root for every binding is not safe: a stray `.atlas/` in a parent
folder (the home directory, say) would capture unrelated repositories.

The capture store is the desktop's schema v10 byte for byte
(`internal/capture/store/store.go`), so the binding row cannot take a new
column.

## Decision

1. **A workspace is an explicit, opt-in kind of Project.** `atlas init
   --workspace` binds a folder of repositories once (it refuses a folder
   without repositories or inside one). On a terminal, plain `atlas init` in
   a folder that is not inside git and has child folders holding `.git` asks,
   with "This folder only" pre-selected; without a terminal it binds the
   folder alone and prints the repositories and `atlas-cli workspace on`.
   Re-running init keeps a workspace a workspace unless `--workspace=false`
   or the prompt says otherwise. It is never a global default or a setting:
   the first build made a workspace by default, and one init in a folder of
   unrelated projects pulled ten repositories into one cloud Project.
2. **The marker is a sidecar,** `.atlas/workspace.json` `{"workspace": true}`,
   like `memory-sharing.json`. The binding row is unchanged.
3. **Discovery** walks to the scope root as before. Only when nothing is
   bound there does it keep walking up, and then it accepts only a bound
   folder carrying the marker. Nested bindings still win: a member
   repository with its own `atlas init` is split off as its own Project.
4. **Members are read, not stored:** the direct child folders holding a
   `.git` directory or file, each time. A repository cloned in later is a
   member without re-running init.
5. **One memory for the workspace.** Memory lives in the workspace folder
   (`project.MemoryRoot`), so the frontend agent sees what the backend agent
   decided. Citations are spelled from the workspace folder
   (`backend/src/x.go`).
6. **Agent and git hooks go into the workspace folder and every member**
   (`project.Targets`), because each agent reads its config from the folder
   it starts in. `agents remove` and `uninstall` clean them all.

## Consequences

- A workspace has no git fingerprint; a cloud workspace binds by name, as a
  folder outside git does today.
- Git capture (shell changes, commit links) still runs in the Project root,
  which is outside git for a workspace: sessions in members record prompts,
  replies, tool calls and the files tools report, but not yet shell changes
  or commit links. Running git per member repository is the next step.
- A repository cloned in later has no agent hooks until
  `atlas-cli workspace sync` runs. `atlas-cli workspace` shows which folders
  are hooked; `on`/`off` convert a bound folder; `add`/`remove` list a folder
  without git (or a deeper repository) in the marker's `folders`.
- A member with its own binding is not a member: workspace commands never
  hook or unhook it.
- The desktop app does not read the marker: opening a member alone in it
  creates a separate store.
- A mother folder inside a git repository cannot be a workspace.
