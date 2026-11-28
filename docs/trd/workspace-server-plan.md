# Server plan: workspace groups and session tags, phase by phase

| | |
|---|---|
| **Status** | **S1–S5 implemented** on server branch `feat/workspace-groups`, not committed: registry (S1), tags stored (S2), group and member boards with per-reader tags (S3), web screens incl. "New workspace" (S4), docs and ADR-0032 accepted (S5). The flag `workspaces.groups` stays off by default: granting it is the pilot step. |
| **Date** | 2026-11-28 |
| **Server commit read** | `cac142c0` (the server repository, read-only) |
| **Design it implements** | [workspace-server-changes.md](workspace-server-changes.md), with the corrections in §3 |
| **CLI side** | P1–P5 are built ([ADR-0016](../adr/0016-a-workspaces-repositories-are-projects-of-their-own.md), [ADR-0017](../adr/0017-sessions-are-tagged-with-the-projects-they-changed.md), [ADR-0018](../adr/0018-a-workspace-is-a-group-in-the-cloud.md)), checked end to end against the mock. |

Paths are relative to the server repo. ✔ marks a fact I read in the code myself; ◦ marks one taken from an investigator's report and not re-checked.

---

## 1. The plan in one picture

```
 S0 Decide        S1 Registry        S2 Tags stored      S3 Reads           S4 Web            S5 Turn on
 ───────────      ─────────────      ──────────────      ───────────        ──────────        ──────────
 ADR-0032 draft   D1 0058            DO v12              tags on rows       group-aware       pilot org
 open questions   flag (off)         projects[] kept     group= board       screens           then everyone
 mock in gate     groups, parentId   erase + archive     tagged=1 board     chips, tree       docs final
                  mock: 1 group      mock: (none)        mock: tags         mock: all views
      │                │                   │                  │                  │                 │
      ▼                ▼                   ▼                  ▼                  ▼                 ▼
  nothing ships    dark: no org      dark: only groups    dark: new params   dark: only a      visible
                   can make a group  get tags             nobody sends yet   group changes UI
```

Every phase is **dark** until S5: it ships to production, and nothing changes for any existing user, client or screen. One feature flag (`workspaces.groups`) is the single switch.

---

## 2. Rules that keep existing things working

| # | Rule | Why (evidence) |
|---|---|---|
| R1 | **Only add.** New D1 columns are defaulted or nullable. New contract fields are optional. A missing `kind` means `"workspace"`; a missing `parentId` means `null`. | `workspace_refs` is "extended in place, never replaced" (`packages/db/src/schema.ts:37-42` ✔). The web persists its query cache, so new web code reads old rows without the fields (◦ `docs/web-data-layer.md` §4). |
| R2 | **No group can exist until the flag is on.** With the flag off, the server ignores `kind` and `parentId` exactly as today's server strips them. | The web does no runtime parsing, so a group row would render as a broken repository project on about ten screens (§6.4). |
| R3 | **A tag can never fail a session.** `projects` is parsed leniently; anything malformed becomes "absent". | A row that fails `CommittedArtifact.safeParse` is answered `accepted: false` (`apps/ingest/src/ingest.ts:142-151` ✔), which the drain treats as permanent. |
| R4 | **Existing request parameters keep their meaning.** `workspace=<id>` on the board stays "sessions homed here". New behaviour comes only through new parameters. | The old web and the mock tests rely on it (◦ `mock/tests/ingest.test.ts:152-155`). Ingest always deploys minutes before web (✔ `deploy.yml`: auth → email → sync → ai → ingest → chat → axis → agent → web). |
| R5 | **A tag never grants access, and never names what the viewer can't read.** | A restricted Workspace answers `404`, not `403`, so its name can't be enumerated (`apps/ingest/src/workspaces.ts` `readable` ✔). A tag naming it would be the same oracle. |
| R6 | **A group never gets a fingerprint or a Mirror.** | `connect` matches by root commit, then git URL (`connect.ts` `matches` ✔). A group with a fingerprint would start capturing members' connects. |
| R7 | **The mock moves with every phase**, and its checks are run by hand. | `mock:typecheck` and `mock:test` are in neither `check:all` nor CI (◦ root `package.json`, `deploy.yml`). A contract change that breaks the mock passes CI today. |

---

## 3. What the code research changed in the design

The earlier design doc was written from a first investigation. Reading the code closely changed nine things:

| # | Earlier design | Now | Why |
|---|---|---|---|
| C1 | Groups are accepted as soon as S1 ships | Behind a **platform feature flag**, `workspaces.groups` | R2. The flag system exists: a global switch and a per-Organisation override (`packages/contracts/src/features.ts` ✔). |
| C2 | `projects` is `z.array(...).max(64).optional()` | **Lenient**: wrong shape → absent; over 64 → first 64; unknown reason → dropped | R3 |
| C3 | A member's board (`workspace=M`) starts including tagged sessions | A **new parameter**, `include=tagged`. `workspace=M` alone is unchanged. | R4 |
| C4 | The socket's `session.summary` frame carries tags | Socket frames carry **no tags**. The board's 15-second poll brings them. | The Durable Object broadcasts one frame to every socket and doesn't know each viewer's readable set (R5). |
| C5 | Detect support by the echo of `kind` | A **capability on the list** (`GET /workspaces` → `groups: true`), plus the echo as proof | `RegisterWorkspaceResponse` is only `{workspaceId, id, slug}` (`workspaces.ts:166-171` ✔). The CLI should know before it creates anything. |
| C6 | Not covered | **Promote** a plain Workspace to a group (`PATCH kind`) | CLI P1 already registers the workspace folder as a plain Workspace by name. Those rows need a way to become groups. |
| C7 | Not covered | `session_tags` joins the **erase** and **archive** table lists | Both name their tables one by one (`workspace-erase.ts:174-197` ✔, `workspace-do.ts` `ARCHIVE_TABLES` ✔). |
| C8 | Not covered | **Axis guard**: connecting a repository to a group is refused | `AxisLink.setFingerprint` writes the Mirror's git URL and root commit onto the Workspace when it has none (`axis-link.ts:97-122` ✔). That breaks R6. |
| C9 | "Ship the D1 migration first, as its own step" (my note earlier today) | One push is safe **in CI** | CI applies D1 migrations as the first command of the auth step, before any worker deploys (`deploy.yml:438` ✔). The hazard exists only on the manual `wrangler` path; see §9. |

One thing did **not** change: `SessionHash` in the CLI stays as it is, and the server stores tags before its replay check (`workspace-do.ts:2159` ✔).

---

## 4. Decisions needed before S1

These are product choices. Each has a recommendation; S0 is done when they're answered.

| ID | Question | Recommendation | What it affects |
|---|---|---|---|
| D1 | Does a group count against the plan's "synced projects" cap? | **No.** A group of 3 would otherwise use 4 slots (Pro's seed cap is 10 ◦). | `limits.ts:185` ✔ and `apps/auth/src/billing/read.ts:286` ◦ |
| D2 | Do members inherit the group's visibility or member list? | **No.** Each Workspace keeps its own. A new member starts with the group's visibility as a default only. | Five copies of the visibility rule stay untouched (ingest, ai, auth, axis, chat ◦). |
| D3 | Who may put a Workspace into a group, or take it out? | The Workspace's **owner or an org admin** (`mayManage`), who can also read the group. | `PATCH /workspaces/{id}` |
| D4 | Deleting a group | **Detach** its members (`parent_id = NULL`). They keep everything. | `archive.ts` |
| D5 | A tagged session whose home the viewer can't read | **Hide it.** No placeholder in v1. | S3 |
| D6 | Can a group have public pages or share links? | **Share links yes** (they're per session). **Public pages no** in v1. | S1 guard, S4 menu |
| D7 | Can a chat channel be tagged with a group, or a code reference point at one? | **No** in v1. | `apps/chat/src/chat-do.ts:6048, 6088` ◦ |
| D8 | The wire field is called `projects`, and `CONTEXT.md` reserves *Project* for a local folder | **Keep `projects`**: each entry *is* a local Project (its `folder`), with its Workspace when it has one. Say so in ADR-0032. | Glossary |

---

## 5. Target shape

```
 D1  workspace_refs                         one Durable Object per Workspace (ADR-0014)
 ┌──────────────────────────────────┐       ┌──────────────────────────────────────┐
 │ id      kind       parent_id     │       │ fleet-apps DO                        │
 │ ──────  ─────────  ────────────  │       │   sessions       (started in parent) │
 │ G       group      NULL          │──────▶│   session_tags   S2 → api, web       │
 │ A       workspace  G             │──┐    └──────────────────────────────────────┘
 │ B       workspace  G             │─┐│    ┌──────────────────────────────────────┐
 │ X       workspace  NULL          │ │└───▶│ api DO                               │
 └──────────────────────────────────┘ │     │   sessions       (started in api/)   │
        kind, parent_id are new       │     │   session_tags   S4 → web            │
                                      │     └──────────────────────────────────────┘
                                      └────▶  web DO …

 Board reads (GET /sessions)
   group=G                  ──▶ fan out to G, A, B              (existing fan-out)
   workspace=A               ──▶ A only                          (unchanged)
   workspace=A&include=tagged ──▶ A, plus G and B filtered to "tagged with A"
```

---

## 6. The phases

Each phase is one push to `main` (pushes are expensive: about an hour of runner time each ◦, so nothing ships as a tiny change on its own).

### S0. Decide and prepare (no deploy)

| Task | Detail |
|---|---|
| Answer D1–D8 | §4 |
| ADR-0032, draft | "A Workspace can group other Workspaces; a Session is tagged with the Workspaces it changed." Says which parts of ADR-0014, ADR-0025 (the cap) and ADR-0029 §1 (Mirror ownership) it amends. Each of those gets a forward pointer when it is accepted (◦ `docs/agents/domain.md`). |
| Linear | A Project in team ATL with one issue per phase (◦ `docs/agents/issue-tracker.md`) |
| Mock in the local gate | Add `pnpm mock:typecheck && pnpm mock:test` to `check:all` (local, free). CI stays as it is unless you want to spend the minutes. |
| **Desktop check** | Confirm the frozen desktop ignores unknown fields on `GET /workspaces` (`crates/atlas-checkpoint/src/sync.rs`, not in this repo). The server's comment says it types `slug` as a plain `String` (`registry.ts` `asRegistered` ✔); nobody has verified `deny_unknown_fields`. **If it rejects unknown fields, S1 must not add `kind` to the list for desktop callers.** |

**Exit:** decisions recorded; the desktop check has an answer.

---

### S1. Registry: groups can exist (dark)

```
 flag off (every org, until S5)          flag on (pilot org)
 ─────────────────────────────           ───────────────────────────────────────
 POST /workspaces {kind:"group"}         POST /workspaces {kind:"group"}
   → plain Workspace, as today             → group row, no fingerprint
   → response has no kind                  → response: kind:"group"
 GET /workspaces                         GET /workspaces
   → no "groups" field                     → groups:true, rows carry kind, parentId
```

**D1: `packages/db/migrations/0058_workspace_groups.sql`** (0057 is the newest ◦)

```sql
ALTER TABLE workspace_refs ADD COLUMN kind TEXT NOT NULL DEFAULT 'workspace';  -- 'workspace' | 'group'
ALTER TABLE workspace_refs ADD COLUMN parent_id TEXT;                          -- a group's id, or NULL
CREATE INDEX workspace_refs_by_parent ON workspace_refs (parent_id);
```

**Flag:** add `workspaces.groups` to `PLATFORM_FEATURES` with `globalDefault: true, orgDefault: false`. Off for every Organisation; an override turns it on for one.

**Contracts** (all optional, R1)

| Shape | Change |
|---|---|
| `RegisteredWorkspace` | `kind?: "workspace" \| "group"`, `parentId?: string \| null` |
| `WorkspaceRegistryList` | `groups?: boolean` (this Organisation may create groups) |
| `RegisterWorkspaceRequest`, `ConnectWorkspaceRequest` | `kind?`, `parentId?` |
| `UpdateWorkspaceRequest` | `parentId?: string \| null` (null detaches), `kind?: "group"` (promote, one way) |
| `RegisterWorkspaceResponse` | `kind?`, `parentId?` (the echo) |
| Error codes | `parent_not_group`, `nested_group`, `group_has_repository`, `group_full` |

**Ingest**

| File | Change |
|---|---|
| `registry.ts` | `COLUMNS` and `WorkspaceRow` gain the two columns ✔. `asRegistered` returns them only when the flag is on. `registerWorkspace` and `updateWorkspace` write them. New `membersOf(groupId)`. |
| `public-profiles.ts:309` ◦ | The hand-built `WorkspaceRow` literal gets the new fields (else typecheck fails). |
| `workspaces.ts` `register` | Flag on and `kind: "group"`: fingerprint fields are ignored. `parentId`: checked by the rules below. |
| `workspaces.ts` `patch` | `parentId` (rules below, D3). `kind: "group"` promotes a row that has no fingerprint, no parent and code sync off. A group refuses `gitUrl` and `rootCommitSha` (`group_has_repository`). |
| `connect.ts` | Never creates a group. `create: true` with `parentId` creates a member. A match **never changes a parent**: the answer carries the matched row's `parentId`, and the client sends the `PATCH` itself. So connect stays as it is for every existing caller. |
| `limits.ts:185` ✔ | `AND kind = 'workspace'` (D1). No group exists yet, so the count is unchanged today. |
| `axis-link.ts` | `setFingerprint` and `writeCodeSync` refuse a group (C8). Ingest deploys before Axis and web, so one check here covers both. |
| `archive.ts` | Archiving a group runs `UPDATE workspace_refs SET parent_id = NULL WHERE parent_id = ?` (D4). |

**Rules for `parentId`**

| Rule | Answer |
|---|---|
| The parent is a live group in the same Organisation that the caller may read | else `404` (never reveals a restricted group, R5) |
| A group can't have a parent | `422 nested_group` |
| The parent isn't a group | `422 parent_not_group` |
| A group holds at most 49 members (`BOARD_FANOUT_MAX - 1` ✔) | `422 group_full` |

**Other workers** (they deploy after the migration, so they may name the column)

| File | Change |
|---|---|
| `apps/auth/src/billing/read.ts:286` ◦ | The "Synced projects" count gets `AND kind = 'workspace'` (D1). Auth applies the migration itself, just before it deploys ✔. |
| `apps/chat/src/chat-do.ts:6048, 6088` ◦ | The channel-tag and code-reference lists get `AND kind = 'workspace'` (D7). |
| `apps/ai`, `apps/axis`, `apps/agent`, `apps/auth` presence | **No change.** They select explicit columns, and a group is a readable Workspace like any other ◦. |

**Tests** (`apps/ingest/test/`, real miniflare D1 and Durable Objects ◦)

| File | New cases |
|---|---|
| `workspaces.test.ts` | flag off: `kind` ignored, list unchanged · create group · the four parent rules · promote · a group refuses a fingerprint |
| `sync.test.ts` | connect never matches a group · `create` with `parentId` · existing "ambiguous" behaviour unchanged |
| `plan-limits.test.ts` | a group doesn't use a slot |
| `lifecycle.test.ts` | deleting a group detaches its members |
| `axis-link.test.ts` | `setFingerprint` on a group writes nothing |

**Mock S1**: see §7.

**Exit:** `pnpm check:all` green · mock checks green · with the flag off, every existing ingest test passes unchanged · the mock's group opens without errors on the current (not yet group-aware) web screens.

**Unblocks in the CLI:** registering the workspace folder as a group, and members with `parentId`.

---

### S2. Tags are stored (dark)

```
 CLI ── POST /ingest ──▶ ingest worker ──────────────▶ home Workspace DO
        agent_session      1 parse leniently (R3)       4 tombstone check (existing)
        + projects[]       2 home in a group? else      5 REPLACE session_tags      ← new, first
                             drop projects              6 replay check on contentHash (existing)
                           3 keep only ids of the       7 upsert the session        (existing)
                             group and its members;
                             others → workspaceId null
```

**Contract: `AgentSessionArtifact.projects`**

```ts
// Never a reason to refuse the row: anything unexpected becomes "absent".
projects: SessionProjects.optional().catch(undefined)
// SessionProjects: at most 64 entries (extra ones are cut, not refused), each
//   { workspaceId: string | null, folder: string (1–512), reasons: ("edited" | "committed")[] }
// Unknown reasons are dropped. An entry with no reason left is dropped.
```

`undefined` (old CLI, desktop) leaves stored tags alone. `[]` clears them.

**Durable Object: migration v12** (v11 is the last ✔; plain DDL only, because a throwing step would fail every request to that object)

```sql
CREATE TABLE session_tags (
  session_id   TEXT NOT NULL,
  folder       TEXT NOT NULL,
  workspace_id TEXT,              -- NULL while the folder has no Workspace
  reasons      TEXT NOT NULL,     -- JSON array
  PRIMARY KEY (session_id, folder)
);
CREATE INDEX session_tags_by_workspace ON session_tags (workspace_id, session_id);
```

| File | Change |
|---|---|
| `ingest.ts` | After access is checked, and only when the target row is a group or has a parent: load the group's member ids once per batch, and rewrite each tag (R5). A home outside any group gets `projects` removed. Never refuses. |
| `workspace-do.ts` `upsertSession` ✔ | Replace the session's tag rows **before** the replay early return at `:2159`. Skip the write when the set is unchanged. |
| `workspace-do.ts` `ARCHIVE_TABLES` ✔ | Add `{ name: "session_tags", kind: "session_tag" }` (C7). |
| `workspace-erase.ts:174-197` ✔ | Add `DELETE FROM session_tags WHERE session_id = ?1` (C7). |
| `embed-queue.ts` | Strip `projects` before queueing: tags aren't embedded. Leaving it would also be safe: the AI worker parses with the same contract and deploys before ingest ✔. |

Erased sessions are already filtered out before any upsert (`workspace-do.ts:2585-2599` ✔), so no tag is ever stored for one.

**Tests**

| File | New cases |
|---|---|
| `artifacts.test.ts` | the CLI's exact JSON parses · malformed `projects` → the row is still accepted · 65 tags → 64 kept |
| `artifact-store.test.ts` (replay tests at `:120-168` ◦) | a re-send with the **same** `contentHash` and new tags updates `session_tags` · `[]` clears · absent leaves them |
| `ingest.test.ts` | a tag naming a Workspace outside the group → `workspaceId` null · a home outside any group stores nothing |
| `erase.test.ts`, `lifecycle.test.ts` | erasing a session removes its tags · an archive contains them |

**Mock S2:** nothing. The mock has no push door (◦ no `POST /ingest` route in `mock/server/services/ingest`).

**Exit:** `pnpm check:all` green · the replay test proves tags land on an unchanged hash.

**Unblocks in the CLI:** nothing new to send (P2 already sends `projects`). The CLI adds a one-time re-send of sessions in workspace and member stores, so sessions sent before S2 get their tags.

---

### S3. Reads: tags on rows, the group board, the member board (dark)

```
 GET /sessions?org=…
   &group=G                     targets: G + readable members of G
   &workspace=A                 targets: A                               (unchanged, R4)
   &workspace=A&include=tagged  targets: A  +  G and readable siblings, each asked
                                              with tagged=A
                 │
                 ▼  same cursor to every object, limit+1 from each, merge   (existing ✔)
                 ▼  tags filtered to what this viewer may read              (new, R5)
```

| File | Change |
|---|---|
| `packages/contracts/src/sessions.ts` | `SessionSummary.tags?: Array<{ workspaceId: string \| null, workspaceSlug: string \| null, number: number \| null, folder: string, reasons }>`. Optional, so the five places that build a summary keep compiling ◦. |
| `workspace-reads.ts` `boardFilters` ✔ | A `tagged` clause: `session_id IN (SELECT session_id FROM session_tags WHERE workspace_id = ?)`. One place covers the page **and** both facets, because all three share the function. |
| `workspace-reads.ts` `summarise` | One grouped query on `session_tags` for the page's sessions. |
| `reads.ts` `board` ✔ | `group=` and `include=tagged` build the target list; each target gets its own forwarded query. `workspaces` in the answer lists every target, so rows from siblings get their `wp-N` link. |
| `reads.ts` (all summary answers) | Fill `workspaceSlug` and `number` from the registry. **Drop** a tag whose Workspace the viewer can't read. Keep a tag with no Workspace (it shows a folder of the home, which the viewer can already read). |
| `workspace-do.ts` broadcast (`:3770` ✔) | The `session.summary` frame carries **no** `tags` (C4). |
| `public-shares.ts:368, 398` ✔ | Strip `tags` for share-link readers. The public timeline is already an allowlist ◦. |

**Not in v1:** `group=` on search, dashboard, activity and ROI. A group's card numbers are the sum of its members' rows from `GET /workspaces/stats`, which the web can add up itself (the endpoint already returns one row per Workspace ✔).

**Tests** (`reads.test.ts`, fan-out cases at `:166-357` ◦)

- `group=` returns the group's and members' sessions in one order, with a working cursor across objects.
- `workspace=A` alone is byte-for-byte what it was.
- `include=tagged` adds exactly the sessions tagged with A; facets count the same rows.
- A viewer who can't read the home never sees the session (D5).
- A viewer who can't read a tagged member gets the row without that tag.
- A share link's summary has no `tags`.

**Mock S3**: see §7.

**Exit:** `pnpm check:all` and mock checks green · old web against new ingest shows no difference (it sends neither new parameter).

**Unblocks in the CLI:** nothing; this is for the web.

---

### S4. Web (dark: only a group changes what anyone sees)

Three pushes at most. 4a can go out any time after S1.

**4a. Know what a group is.** One helper, `isGroup(w)`, and every list decides:

| Screen | File ◦ | For a group |
|---|---|---|
| Projects list | `routes/_app/projects.tsx`, `components/projects/project-list.tsx` | A group row with its members nested under it; counts are the members' sum; no repository line, no sync state |
| Project frame | `components/axis-pages/project-layout.tsx` | Tabs: Overview · Timeline · **Projects**. No Code tab, no "Connect repository", no "Public pages" (D6). A member gets a breadcrumb to its group. |
| New route | `routes/_app/projects_.$project.projects.tsx` | The group's member list |
| Delete dialog | `components/projects/project-dialogs.tsx` | Says its projects stay and become standalone (D4) |
| ⌘K palette | `design/shell/command-palette.tsx` | `fleet-apps › api` |
| Dashboard widgets | `components/dashboard/widgets/{projects,project-health,setup}.tsx` | Groups left out; "Create a project" isn't done by a group |
| ROI selector, Atlas Loops, @project mentions | `routes/_app/roi.tsx`, `components/atlas/loops.tsx`, `lib/atlas/mention-sources.ts` | Groups left out |

**4b. Timelines and tags**

| File ◦ | Change |
|---|---|
| `lib/artifacts.ts`, `lib/queries/timeline.ts` | `BoardQuery.group`, `includeTagged`; both are part of the query key automatically |
| `components/timeline/timeline-screen.tsx` | A group page passes `group=`; a member page passes `include=tagged`. Today `fixedProject` hides the project column; it must show for a row whose home is another Workspace. |
| `components/timeline/session-row.tsx` | Tag chips (Astryx `Token`) and "from fleet-apps ↗". The row is a `<button>`, so the link follows the comment marker's `<span onClick>` pattern, not an `<a>`. |
| `components/timeline/timeline-filters.tsx` | The Project filter lists groups with their members indented |
| `lib/workspace-socket.ts` | A frame without `tags` keeps the row's existing tags (C4) |

**4c. Manage**

| File ◦ | Change |
|---|---|
| `components/projects/new-project-page.tsx`, `lib/queries/projects.ts:88` | "New workspace", and an optional "Part of workspace" on a new project. The optimistic row literal gets the new fields. |
| Project settings | Move a project into or out of a group (`PATCH parentId`) |

**Web rules that apply** ◦: StyleX and Astryx only (the ratchet test refuses Tailwind, `cn`, colour literals); every write through `optimistic()`; keys from the area's factory with the org id second; a unit test for each patch function (`docs/web-data-layer.md` §6). Reuse Astryx `TreeList`, `Breadcrumbs`, `Token`, and the kit's `DataList`, `Handle`, `ProjectArt`.

**Checks:** `tests/axis-pages/helpers.test.ts` (tabs), `src/lib/artifacts.test.ts` (`boardQuery`), `e2e/navigation.e2e.ts`, then every screen in the mock at 390, 820, 1280 and 1600 wide, both themes, as **Maya** (admin) and **Liam** (member, can't read `infra`) ◦.

The static preview at [workspace-ui-preview.html](workspace-ui-preview.html) is the visual target.

---

### S5. Turn it on

```
 1  override workspaces.groups = on for the pilot Organisation     (Ops, no deploy)
 2  CLI P3 against production: group + members + tags + backfill
 3  walk the TRD scenarios S1–S7 in the web app
 4  catalogue: orgDefault → true                                    (one push)
 5  ADR-0032 accepted · CONTEXT.md terms · docs/api updated
```

| Doc | Change |
|---|---|
| `docs/adr/0032-…` | Accepted. ADR-0014, 0025 and 0029 get forward pointers. |
| `CONTEXT.md` | **Group Workspace**, **Member Workspace**, **Session tag**. The *Workspace* entry's "there is only one of them" wording (`:13-18` ◦) is amended. |
| `docs/api/atlas-artifacts-api.md` | §2 Workspaces (`kind`, `parentId`, `groups`, the new codes) · §3 the artifact union (`projects`) · §5 `GET /sessions` (`group`, `include=tagged`, `tags`) · §11 Lifecycle (deleting a group) |

**Kill switch:** turn the global flag off. Groups stop being created and `kind` stops being reported; rows and tags already stored stay, and the web falls back to treating every row as a plain project.

---

### S6. Later (not needed to ship)

| Item | Why it waits |
|---|---|
| **Commit links across a group.** A checkpoint stored in the group's object never links to a commit in a member's Mirror, because links are per object (`workspace-code.ts:20-31` ◦). The member's Code tab won't show sessions started in the workspace folder. | Needs a design for cross-object links |
| A "1 session you can't open" placeholder | Needs a D1 tag index to count without reading the home |
| Group roll-ups for dashboard, ROI, search | Each is its own fan-out |
| Live tags on the socket | Needs a per-viewer send |
| A teammate's machine finding the same group by its members' root commits | v1 finds it by slug or id |
| Public pages for a group; chat channels tagged with a group | D6, D7 |

---

## 7. The mock, phase by phase

The mock is the real web app on a fake backend, built on one deterministic world. `mock/BUILDING.md` says "Do not edit `mock/world/*`" ◦. Groups are a world change, so **this plan overrides that rule on purpose**; say so in the PR.

**The fixture: one group in Meridian Labs, made from projects that already exist.**

```
 Fleet Apps            wp-9   group      org-visible      created by maya
 ├── Meridian API      wp-1   member     (existing)
 ├── Meridian Web      wp-2   member     (existing)
 ├── Meridian Mobile   wp-3   member     (existing)
 └── Infrastructure    wp-5   member     (existing, restricted: kenji, jonas, maya)

 standalone: Telemetry Ingest, Design System, Data Platform, Meridian CLI
```

| Choice | Why ◦ |
|---|---|
| Re-parent existing projects, add no new ones | The mock's Axis has a 10-repository cap and Meridian has 8; world tests count mirrors (`axis-routes.test.ts:43`) |
| The group is appended last, as `wp-9` | Existing handles (`wp-1`…`wp-8`) and code that takes `[0]` stay the same |
| Infrastructure is a member | It is the only restricted project. Liam can't read it, which makes it the test for R5 and D5. |
| The name "Fleet Apps" | The Atlas mock turns `@<project name>` in seeded chat into mentions and matches project names in questions. The name must not be a prefix of an existing one. **Run `mock/tests/agent*.test.ts` to confirm.** |
| Tags come from their **own** seeded stream (`rng("tags:" + sessionId)`) | Drawing from `sessions:<slug>` or `background:<org>` would shift every later id and timestamp, and break bookmarked URLs |
| The group is not added to `SHARE` or `CAST` | Same reason: it would reshuffle all background sessions |

| Phase | Files | Change |
|---|---|---|
| **S1** | `mock/world/workspaces.ts` | `kind`, `parentId` on the seed; the group; `parentId` on the four members |
| | `mock/world/git.ts` | Skip groups (it invents 6 commits for every Workspace) |
| | `mock/server/services/ingest/registry.ts` | `registered()` returns the fields; `POST`/`PATCH` accept them with the S1 rules; delete detaches; `matches` never returns a group |
| | `mock/server/services/axis/state.ts` | Skip groups in `providerRepos` and `mirrors` (else a group gets an empty-named repository and Simulation runs) |
| | `mock/server/services/platform/billing-routes.ts:57` | Count only `kind === "workspace"` (D1) |
| | `mock/server/services/chat/routes.ts:302` | Leave groups out of channel tags (D7) |
| | Tests | `ingest.test.ts:297` (`toHaveLength(8)` → 9) · `axis-engine.test.ts:159-165` (skip groups) · `world.test.ts` new invariants: a parent is a group in the same org; a group has no parent, no commits |
| **S3** | `mock/world/sessions.ts`, `ingest/seeds.ts`, `ingest/model.ts` | `tags` on sessions: a handful of Fleet Apps sessions tagged with api and web, **two tagged with Infrastructure**, one with a folder that has no Workspace |
| | `ingest/summary.ts` | `summaryOf` takes the viewer and filters tags (its callers: `reads.ts`, `socket.ts`, `live.ts`) |
| | `ingest/reads.ts` | `group=` and `include=tagged`, same cursor |
| | `ingest/socket.ts` | Frames without `tags` |
| | `collab/sessions.ts:220`, `collab/shares/public.ts:136,145` | They build summaries themselves; no tags for share readers |
| | Tests | group board · tagged member board · as Liam: no Infrastructure-homed row, no Infrastructure chip · facet sums still equal row counts (`ingest.test.ts:132-135`) · `workspace=ws_meridian_cli` unchanged (`:152-155`) |
| **S4** | `mock/server/control.ts` | "A workspace" → `/projects/wp-9` in the screens list |
| | `mock/README.md` | "eight projects" → "eight projects, four of them in one workspace" |

**Mock gate for every phase:** `bun run mock:typecheck` · `bun run mock:test` · open the changed screens as Maya and as Liam.

---

## 8. What each phase can break, and the guard

| Risk | Phase | Guard |
|---|---|---|
| A group row renders as a broken repository on ~10 web screens | S1 | Flag off for everyone until 4a is out (R2) |
| The frozen desktop rejects unknown fields in the list | S1 | The S0 desktop check; with the flag off the fields aren't sent at all |
| A group takes a plan slot → `403 plan_limit` for a user who was under the cap | S1 | `AND kind = 'workspace'` in both counts (D1) |
| A Mirror connected to a group gives it a fingerprint → members' connects match the group | S1 | `AxisLink` refuses a group (C8) |
| A member created with the same root commit as an existing Workspace turns "connected" into "ambiguous" for every client | S1 | Server: unchanged behaviour. CLI: it connects by fingerprint first and adopts the match (already built in P1), and only creates when nothing matches. |
| A malformed tag permanently fails a session row | S2 | Lenient parsing (R3), with a test |
| An erased session leaves tag rows | S2 | `session_tags` in the erase list (C7), with a test |
| A re-send with new tags is dropped as a replay | S2 | Tags are written before the replay check, with a test |
| A tag reveals a restricted project's name | S3 | Dropped per viewer in the worker; none on the socket; none on share links (R5) |
| A member's board changes for the old web or old tests | S3 | `include=tagged` is a new parameter (R4) |
| Group rows use up the 50-Workspace fan-out | S3 | A group and its members are one board; the cap of 49 members keeps it under 50 |
| The mock silently stops typechecking | all | The mock gate (R7) |
| Web typecheck breaks on new required fields | S1, S3 | Every new field is optional (R1) |

---

## 9. Deploy notes

- **CI order** ✔: D1 migrations (first command of the auth step) → auth → email → sync → ai → ingest → chat → axis → agent → web.
- During a deploy, **new ingest runs with old web** for minutes (longer if the Axis step retries). Every phase is safe in that state because old web sends no new parameter and ignores new fields.
- **New web with old ingest** can't happen in CI. After a manual rollback of ingest it can; new web then gets rows without `kind` and treats them as plain projects (R1).
- **Manual `wrangler` path:** apply the D1 migration before deploying ingest. `registry.ts` selects its columns by name ✔, so ingest code that names `kind` against a database without it fails every Workspace listing.
- **Durable Object migration** runs lazily in each object's constructor ✔. An object already at v12 is fine under a rolled-back ingest (`if (from >= MIGRATIONS.length) return` ✔).

---

## 10. What the CLI does after each server phase

| Server phase | CLI change (P3) |
|---|---|
| S1 | Read `groups` from `GET /workspaces`. When true: register the workspace folder with `kind: "group"`, or promote the plain Workspace it already has (`PATCH kind`). Create members with `parentId`; for a matched member with no parent, ask, then `PATCH parentId`. When false: keep today's behaviour. |
| S2 | Once per store, re-send its sessions so earlier ones get tags (`ResyncSession` already exists). |
| S3, S4 | None |
| S5 | `atlas serve` in a workspace folder drains member stores too (TRD FR-11). |

---

## 11. How this was researched

- I read the ingest registry, connect, push, Durable Object migrations and session upsert, board fan-out, erase, archive, plan cap, the Axis fingerprint write, the share reader, the flag catalogue, the embedding queue and the deploy workflow.
- Three read-only investigators covered the mock, the web app and the other workers. Their findings are marked ◦.
- **Nothing was run** in the server repo: no tests, no typecheck, no `drizzle-kit generate`, no mock. The desktop's source isn't in the repo, so its handling of unknown fields is unverified (the S0 check).
