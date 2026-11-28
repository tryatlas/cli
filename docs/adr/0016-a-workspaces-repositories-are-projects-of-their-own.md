# ADR-0016: A workspace's repositories are Projects of their own

**Status:** Accepted (2026-11-28, at the user's request: "when I init a
workspace in the parent folder a .atlas folder is created there and in every
child folder… a session started in a child syncs to workspace → project
timeline"). Amends ADR-0015 and ADR-0007. Design and diagrams:
[the workspace TRD](../workspace-projects-and-tagging.md). This ADR records
phase P1, the local model. Tags and per-member git are ADR-0017 (P2); the
cloud's group timelines follow in later phases.

## Context

Under ADR-0015 a workspace is one Project: one store, one memory and one
cloud Project for a whole folder of repositories. You can't tell which
repository a session worked on. Git capture never runs, because the store's
root is not a repository. The desktop opens a member as a separate, empty
store. And `atlas init` inside a member quietly splits it off from the
workspace.

The store is still the desktop's schema v10, byte for byte, so the binding
row can't take a column.

## Decision

1. **A member repository can be bound as a Project of its own**, a
   *member Project*. It has its own `.atlas/sessions.db` and a normal
   binding, and its marker links it to the workspace:
   `.atlas/workspace.json` is `{"memberOf": ".."}`. The path is relative
   (`"../.."` for a folder added by hand deeper down), so moving the whole
   tree keeps the link. A folder is never both a workspace and a member:
   workspaces don't nest.
2. **Discovery is unchanged except for one step.** The nearest binding
   still wins. When the nearest binding is a member's, its link is
   followed. The link holds only when the folder it names is above the
   member, carries the workspace marker, is bound, and is in the member's
   organisation when both are in one (a local binding is in none). A link
   that doesn't hold makes a plain Project, and `status` says why.
   `project.Project` has `Kind` (plain, workspace or member) and
   `WorkspaceRoot` in place of the `Workspace` flag.
3. **Memory stays one per workspace.** A member's `ScopeRoot` is the
   workspace folder, so `MemoryRoot` and every memory reader follow the
   link without a special case.
4. **A member stands with its workspace in one of four ways**
   (`project.MembershipOf`):

   | Membership | Binding | Where its agents record | A member? |
   |---|---|---|---|
   | Unbound | none | the workspace's store (ADR-0015) | yes |
   | Linked | its own, with `memberOf` here | its own store | yes |
   | Independent | its own, no link | its own store | no |
   | Elsewhere | its own, linked to another workspace | its own store | no |

   Hooks go into the workspace folder and every member, Unbound or Linked
   (`Targets`), as before.
5. **Binding a repository to the cloud is never implicit** (ADR-0015's
   lesson):
   - On a terminal, `atlas-cli init` in a folder of repositories offers
     "Workspace with N Projects". Then it lists the repositories, all
     checked, and binds the ones you keep.
   - Without a terminal, init binds the workspace folder alone and prints
     `atlas-cli workspace add --all`.
   - `atlas-cli workspace add <folder>…` or `--all` binds them on request.

   A member of a cloud workspace goes to the workspace's organisation. It
   matches its cloud Project by git fingerprint. Without a match it gets a
   new one named after the folder, or after the workspace and the folder
   when that handle is taken.
6. **`atlas-cli init` in a member folder joins the workspace.** A folder
   that isn't bound joins; one bound on its own stays on its own.
   `--workspace` and `--workspace=false` move it in or out, and a terminal
   asks. Its organisation is fixed to the workspace's.
7. **Leaving keeps everything.** `atlas-cli workspace remove <folder>`,
   `atlas-cli workspace off` and `init --workspace=false` in the workspace
   folder unlink member Projects. Each goes on as a Project on its own, with
   its sessions, binding and hooks. Its memory is its own again.

## Consequences

- Sessions started in a member Project land in its store. Commit links
  and shell changes work for them, because that store's root is the
  repository. Sessions started in the workspace folder, or in an unbound
  member, still record into the workspace's store.
- An ADR-0015 workspace keeps working unchanged: all its members are
  Unbound. `atlas-cli workspace add --all` upgrades it, and its old
  sessions stay in the workspace's store.
- Each member Project syncs to its own cloud Project through its hooks.
  The cloud has no group yet (the server change, `kind` and `parentId`, is
  pending), so a member's sessions aren't yet listed under the workspace.
  Background sync is a per-Project setting: a member keeps its own.
- Discovery in a member opens two stores, the member's and its workspace's,
  to check the link.
- A member's agent config is its own: `atlas-cli agents add` in a member
  hooks that member only. In the workspace folder it hooks every member.
- An older CLI ignores `memberOf` and treats a member as a plain Project,
  so its memory splits off. Update the CLI on every machine that shares a
  workspace.
