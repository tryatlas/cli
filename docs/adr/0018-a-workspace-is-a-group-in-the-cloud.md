# ADR-0018: A workspace is a group in the cloud

**Status:** Accepted (2026-11-28, phases P3–P5 of
[the workspace TRD](../workspace-projects-and-tagging.md)). Builds on
ADR-0016 and ADR-0017; the server side is its ADR-0032, behind the flag
`workspaces.groups`.

## Context

ADR-0016 binds each repository of a workspace as a Project of its own, and
ADR-0017 sends each session with the Projects it changed. The cloud still
saw them as unrelated Projects: the server keeps only the tags that name a
Workspace of the session's home group, and there was no group.

## Decision

1. **The CLI asks before it relies on groups.** `GET /workspaces` says
   `groups: true` where the organisation has them. Where it does not, the
   CLI sends exactly what it sent before: no `PATCH`, no `parentId`.
2. **The workspace folder's cloud Project becomes the group** the first time
   a member Project is bound (`init`, `workspace add`, `workspace upgrade`).
   One that was registered by name is promoted (`PATCH kind: "group"`). One
   the server will not promote (it holds a repository, or is in a group) stays
   as it is; its Projects are bound all the same, on their own in the cloud,
   and the CLI says why.
3. **A member is created in the group** (`parentId`). A member matched by
   its git fingerprint is moved into the group only after a yes: asked on a
   terminal, `--yes` without one. It may be a teammate's Project. One that is
   in another group is left there.
4. **`workspace remove` and `workspace off` take the Project out of the
   group** (`parentId: null`) when it is in this workspace's group.
5. **Sessions go again once for each makeup of the group.** Tags sent before
   a Project was in the group were dropped by the server, so after binding
   every store of the workspace sends its sent sessions again. A marker in
   the CLI's state (not `.atlas/`) records the group and its members; the
   next change to the members sends them again.
6. **`atlas serve` in the workspace folder drains the member stores** every
   minute (FR-11). It skips a member that has its own listener or whose
   writer lock is held.
7. **New clones are pointed out, never bound unasked** (FR-15). `serve` says
   once per repository that it records into the workspace folder. `workspace
   sync` offers to bind them on a terminal. Binding may send a repository to
   the cloud, so it always waits for a yes.
8. **`workspace upgrade`** (FR-17) brings a workspace bound before ADR-0016,
   or before groups, up to date in one run: it binds the repositories, groups
   them in the cloud and sends earlier sessions again. Sessions stay in the
   workspace's store, because moving them would change their ids and break
   their commit links. `--dry-run` says what would change. Running it again
   changes nothing.

## Consequences

- Nothing in the cloud changes for an organisation without the flag.
- A cloud failure never fails a local bind. The local binding stands, and
  the CLI prints a `Cloud:` note.
- The workspace tree (`atlas-cli workspace`) shows each folder's cloud
  Project. Showing group membership would need the network, and the tree
  works offline.
