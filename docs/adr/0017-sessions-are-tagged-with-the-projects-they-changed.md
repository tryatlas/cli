# ADR-0017: Sessions are tagged with the Projects they changed

**Status:** Accepted (2026-11-28, phase P2 of
[the workspace TRD](../workspace-projects-and-tagging.md), at the user's
request: "if the user runs the agent in the parent folder and it uses
projects A and B, the session shows in the workspace and also in project
A's timeline"). Builds on ADR-0016.

## Context

ADR-0016 gives each repository of a workspace its own store. Sessions
still start anywhere:
- A session started in the workspace folder, or in a repository not bound
  yet, records into the workspace's store, and it can change files in any
  member.
- A session started in a member can change a sibling's files.

Two things were missing:
- **Git.** In the workspace folder, which is no repository, `git status`
  and the commit walk saw nothing.
- **A link.** Nothing tied such a session to the Projects it changed.

The store must stay the desktop's schema v10, so no table can hold tags.

## Decision

1. **Tags are derived, never stored.** `project.LayoutOf(root)` reads the
   workspace's layout: its members and their cloud Workspaces.
   `Layout.Tags` maps a session's rows onto the members, the deepest folder
   first:
   - each `file_touch` path is an `edited` reason;
   - each file of a linked checkpoint is a `committed` reason.

   Paths in the home Project, in the workspace folder itself, or outside
   the workspace add no tag. A member that isn't bound, or isn't in the
   home store's organisation, is tagged by folder alone (`workspaceId:
   null`).
2. **Git runs in each member repository.** `project.Repos(root)` lists a
   workspace's member repositories, each with its folder as a prefix.
   - The hook dispatcher and the recorder run `git status`, HEAD and
     "committed since" in each repository, up to 8 at a time, and spell
     the paths with the prefix (`backend/api.go`).
   - The hooks' HEAD for a workspace is composite: one `<prefix>=<commit>`
     line per member, so a commit in any member counts as moving it.
3. **Commits link per member.** `gitlink.Link` on a workspace's store walks
   each member repository:
   - Each member has its own cursor, keyed by the member's folder.
   - Only touches under the member's prefix count.
   - Checkpoints keep the prefix, so their files stay spelled from the
     store's root and say which member they belong to.
   - Rewritten history is reconciled per member, by that prefix.
   - A post-commit hook in a member links into the member's store as
     before, then into the workspace's store for that repository only,
     through the workspace's listener when one is running.
     `SyncRequest.Repo` names the repository.
   - `LinkRange` takes the composite HEAD.
4. **The session artifact carries `projects`**, the complete set:
   `[{workspaceId, folder, reasons}]`.
   - The home Workspace is the artifact's own `workspaceId`, so it has
     no entry.
   - A plain Project's sessions leave the field out, so the server keeps
     whatever tags it has.
   - The content hash is unchanged: the server stores tags before its
     replay check (server doc §6.1).
   - The field goes out even to today's server, which strips unknown
     fields. Holding it back would not make tags arrive later.
5. **A session is sent again when its tags may grow**, through the
   existing `resync` rule:
   - in a workspace's store, on a touch inside a folder;
   - in a member's store, on a touch outside it;
   - in either, when a commit in a member links to it.

   The drain works the tags out once per session per pass, and reads the
   layout only when a session is going out. The hook's hot path gains at
   most one `UPDATE` per touch.

## Consequences

- A session started in `acme-shop/` that edits `backend/` and commits there
  goes out with `{"workspaceId": <backend's>, "folder": "backend",
  "reasons": ["edited", "committed"]}`. Commit links and shell changes work
  for sessions started in the workspace folder.
- Today's server drops `projects`. Once it stores tags, the sessions sent
  before need one more send to carry them (a backfill, phase P3). Until
  then, a session only goes out again when it changes.
- A sibling's edit from a member session (TRD S4) is tagged, not
  commit-linked: out-of-repo touches never match a commit (TRD FR-18).
- In a workspace of many repositories every turn runs git in each of them,
  in parallel (TRD risk K4).
- `atlas-cli status` in a workspace shows no commit cursor, because each
  member has its own.
