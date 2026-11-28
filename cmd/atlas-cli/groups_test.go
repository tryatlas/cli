package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tryatlas/cli/internal/clistate"
	"github.com/tryatlas/cli/internal/tui"
)

// fakeRegistry is a cloud registry behind the fake ingest: the rows, the
// group capability, and every call that changed something.
type fakeRegistry struct {
	groups bool
	rows   []map[string]any
	// refusePromotion answers a PATCH of kind with a 404.
	refusePromotion bool
	changes         []string // "PATCH ws_x {...}" and "POST {...}"
}

func (r *fakeRegistry) find(id string) map[string]any {
	for _, w := range r.rows {
		if w["id"] == id {
			return w
		}
	}
	return nil
}

// install answers GET/PATCH/POST /workspaces and connect, as a server with
// (or without) groups does.
func (r *fakeRegistry) install(t *testing.T) {
	t.Helper()
	f := installFakeIngest(t, map[string]string{})
	f.respond = func(method, uri, body string) (string, bool) {
		path, _, _ := strings.Cut(uri, "?")
		switch {
		case method == "GET" && path == "/workspaces":
			out := map[string]any{"workspaces": r.rows}
			if r.groups {
				out["groups"] = true
			}
			b, _ := json.Marshal(out)
			return string(b), true
		case strings.HasPrefix(path, "/workspaces/slug-available"):
			return `{"available":true}`, true
		case method == "POST" && path == "/workspaces/connect" && strings.Contains(body, "github.com/acme/backend"):
			ans := `{"status":"connected","workspaceId":"ws_backend","slug":"backend","reason":"root commit"`
			if r.groups {
				ans += `,"parentId":null`
			}
			return ans + `}`, true
		case method == "POST" && path == "/workspaces/connect":
			return `{"status":"no_match"}`, true
		case method == "POST" && path == "/workspaces":
			r.changes = append(r.changes, "POST "+body)
			var req map[string]any
			_ = json.Unmarshal([]byte(body), &req)
			row := map[string]any{"id": "ws_" + req["slug"].(string), "slug": req["slug"]}
			if r.groups {
				row["kind"], row["parentId"] = "workspace", req["parentId"]
			}
			r.rows = append(r.rows, row)
			return `{"workspaceId":"` + row["id"].(string) + `"}`, true
		case method == "PATCH" && strings.HasPrefix(path, "/workspaces/"):
			id := strings.TrimPrefix(path, "/workspaces/")
			if r.refusePromotion && strings.Contains(body, `"kind"`) {
				return "", false
			}
			r.changes = append(r.changes, "PATCH "+id+" "+body)
			row := r.find(id)
			var req map[string]any
			_ = json.Unmarshal([]byte(body), &req)
			if k, ok := req["kind"]; ok {
				row["kind"] = k
			}
			if p, ok := req["parentId"]; ok {
				row["parentId"] = p
			}
			b, _ := json.Marshal(map[string]any{"workspace": row})
			return string(b), true
		}
		return "", false
	}
}

// cloudWorkspace binds acme-shop as a cloud workspace whose Project was
// registered by name, as before groups, with backend matched by its root
// commit and frontend new.
func cloudWorkspace(t *testing.T, groups bool) (reg *fakeRegistry, mother, frontend string) {
	t.Helper()
	mother, frontend, backend := workspaceDir(t)
	commitRepo(t, backend, "https://github.com/Acme/backend.git")
	commitRepo(t, frontend, "https://github.com/Acme/frontend.git")
	installFakeAuth(t, routes())
	reg = &fakeRegistry{groups: groups, rows: []map[string]any{{"id": "ws_backend", "slug": "backend"}}}
	if groups {
		reg.rows[0]["kind"], reg.rows[0]["parentId"] = "workspace", nil
	}
	reg.install(t)
	if code, _, e := runCmd(t, "login", "--no-browser"); code != 0 {
		t.Fatal(e)
	}
	if code, _, e := runCmd(t, "init", "--workspace", "--create", "--yes"); code != 0 {
		t.Fatalf("init: %d %s", code, e)
	}
	if reg.find("ws_acme-shop") == nil {
		t.Fatalf("the workspace was not registered: %v", reg.rows)
	}
	reg.changes = nil
	return reg, mother, frontend
}

// ADR-0018: where the organisation has groups, the workspace's Project is
// promoted to a group, a new member is created in it, and a matched one is
// put in it only after a yes.
func TestWorkspaceAddGroupsTheProjectsInTheCloud(t *testing.T) {
	reg, mother, _ := cloudWorkspace(t, true)

	code, out, e := runCmd(t, "workspace", "add", "--all")
	if code != 0 {
		t.Fatalf("add --all: %d %s %s", code, out, e)
	}
	if reg.find("ws_acme-shop")["kind"] != "group" {
		t.Fatalf("not promoted: %v", reg.changes)
	}
	if !slices.Contains(reg.changes, `PATCH ws_acme-shop {"kind":"group"}`) {
		t.Fatalf("changes: %v", reg.changes)
	}
	if fe := reg.find("ws_frontend"); fe == nil || fe["parentId"] != "ws_acme-shop" {
		t.Fatalf("frontend: %v (%v)", fe, reg.changes)
	}
	if !strings.Contains(out, "frontend: Project in the workspace (Atlas / frontend, created; in the workspace's group).") {
		t.Fatalf("output: %s", out)
	}
	// Without a terminal and without --yes a matched Project is not moved.
	if reg.find("ws_backend")["parentId"] != nil || !strings.Contains(out, "backend: Project in the workspace (Atlas / backend, matched by root commit; on its own in the cloud (atlas-cli workspace upgrade") {
		t.Fatalf("backend moved unasked: %s %v", out, reg.changes)
	}

	// upgrade --yes puts it in; the sessions of every store go again once.
	reg.changes = nil
	code, out, e = runCmd(t, "workspace", "upgrade", "--yes")
	if code != 0 {
		t.Fatalf("upgrade: %d %s %s", code, out, e)
	}
	if !slices.Contains(reg.changes, `PATCH ws_backend {"parentId":"ws_acme-shop"}`) || reg.find("ws_backend")["parentId"] != "ws_acme-shop" {
		t.Fatalf("backend not put in: %v", reg.changes)
	}
	if !strings.Contains(out, "backend: Project in the workspace (a Project in the workspace already; put in the workspace's group).") {
		t.Fatalf("upgrade output: %s", out)
	}
	sig, err := os.ReadFile(clistate.Path(mother, tagsMarker))
	if err != nil || strings.TrimSpace(string(sig)) != "ws_acme-shop:ws_backend,ws_frontend" {
		t.Fatalf("tags marker: %q %v", sig, err)
	}

	// Again: nothing changes in the cloud.
	reg.changes = nil
	if code, _, e := runCmd(t, "workspace", "upgrade", "--yes"); code != 0 || len(reg.changes) != 0 {
		t.Fatalf("second upgrade: %d %s %v", code, e, reg.changes)
	}

	// Removed from the workspace, backend leaves the group too.
	if code, out, e := runCmd(t, "workspace", "remove", "backend"); code != 0 || !strings.Contains(out, "Cloud: backend is out of the workspace's group.") {
		t.Fatalf("remove: %d %s %s", code, out, e)
	}
	if !slices.Contains(reg.changes, `PATCH ws_backend {"parentId":null}`) {
		t.Fatalf("changes: %v", reg.changes)
	}
}

// Where the organisation has no groups, what the CLI sends is what it sent
// before them: no PATCH, no parentId.
func TestWithoutGroupsNothingChangesInTheCloud(t *testing.T) {
	reg, _, _ := cloudWorkspace(t, false)
	code, out, e := runCmd(t, "workspace", "add", "--all")
	if code != 0 {
		t.Fatalf("add --all: %d %s %s", code, out, e)
	}
	for _, c := range reg.changes {
		if strings.HasPrefix(c, "PATCH") || strings.Contains(c, "parentId") {
			t.Fatalf("sent %s", c)
		}
	}
	if strings.Contains(out, "Cloud:") || strings.Contains(out, "group") {
		t.Fatalf("said something about groups: %s", out)
	}
}

// A workspace Project the server will not promote: the members are bound
// all the same, created at the top level, and the CLI says why.
func TestAWorkspaceThatCannotBeAGroupStillBindsItsProjects(t *testing.T) {
	reg, _, frontend := cloudWorkspace(t, true)
	reg.refusePromotion = true
	code, out, e := runCmd(t, "workspace", "add", "frontend")
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, out, e)
	}
	if !strings.Contains(out, "Cloud: the workspace's cloud Project acme-shop could not become a workspace in the cloud") {
		t.Fatalf("output: %s", out)
	}
	if b := binding(t, frontend); b.RemoteWorkspaceID == nil || *b.RemoteWorkspaceID != "ws_frontend" {
		t.Fatalf("frontend: %+v", b)
	}
	for _, c := range reg.changes {
		if strings.Contains(c, "parentId") {
			t.Fatalf("sent %s", c)
		}
	}
}

// FR-15: a repository cloned into the workspace later is pointed out once.
func TestNewClonesArePointedOutOnce(t *testing.T) {
	mother, _, _ := workspaceDir(t)
	if code, _, e := runCmd(t, "init", "--local", "--workspace", "--yes"); code != 0 {
		t.Fatal(e)
	}
	ctx := context.Background()
	if got := newClones(ctx, mother); len(got) != 0 {
		t.Fatalf("init's repositories are new: %v", got)
	}
	mobile := filepath.Join(mother, "mobile")
	if err := os.MkdirAll(filepath.Join(mobile, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	sayNewClones(ctx, &out, mother)
	if !strings.Contains(out.String(), "New in the workspace: mobile. It records into the workspace folder until each is a Project of its own: atlas-cli workspace add mobile.") {
		t.Fatalf("said: %q", out.String())
	}
	out.Reset()
	sayNewClones(ctx, &out, mother)
	if out.Len() != 0 {
		t.Fatalf("said again: %q", out.String())
	}
}

// FR-17: upgrade --dry-run says what changes and changes nothing; without a
// terminal it needs --yes.
func TestWorkspaceUpgradeLocal(t *testing.T) {
	_, frontend, backend := workspaceDir(t)
	if code, _, e := runCmd(t, "init", "--local", "--workspace", "--yes"); code != 0 {
		t.Fatal(e)
	}
	code, out, _ := runCmd(t, "workspace", "upgrade", "--dry-run")
	if code != 0 || !strings.Contains(out, "backend, frontend become Projects of their own (local)") || !strings.Contains(out, "Nothing changed") {
		t.Fatalf("dry run: %d %s", code, out)
	}
	if _, ok := os.Stat(filepath.Join(backend, ".atlas", "sessions.db")); ok == nil {
		t.Fatal("dry run bound backend")
	}
	if code, _, e := runCmd(t, "workspace", "upgrade"); code == 0 || !strings.Contains(e, "--yes") {
		t.Fatalf("without --yes: %d %s", code, e)
	}
	if code, out, e := runCmd(t, "workspace", "upgrade", "--yes"); code != 0 || !strings.Contains(out, "frontend: Project in the workspace (local, nothing is synced).") {
		t.Fatalf("upgrade: %d %s %s", code, out, e)
	}
	if b := binding(t, frontend); b.Mode != "local" {
		t.Fatalf("frontend: %+v", b)
	}
	// FR-11: a local member has nothing to drain.
	if n := drainMembersOnce(context.Background(), syncClient(), filepath.Dir(frontend)); n != 0 {
		t.Fatalf("drained %d local stores", n)
	}
}

// On a terminal a member matched in the cloud is moved into the group only
// when the user says yes to the question about it.
func TestAMatchedProjectIsMovedOnlyAfterAYes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer int // 0 yes, 1 no
		moved  bool
	}{{"yes", 0, true}, {"no", 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			reg, _, _ := cloudWorkspace(t, true)
			oldI, oldC := interactive, chooseStep
			t.Cleanup(func() { interactive, chooseStep = oldI, oldC })
			interactive = func() bool { return true }
			var asked []string
			chooseStep = func(_ tui.Step, question string, _ []tui.Choice, _ int) (int, error) {
				asked = append(asked, question)
				return tc.answer, nil
			}
			code, out, e := runCmd(t, "workspace", "add", "--all")
			if code != 0 {
				t.Fatalf("add --all: %d %s %s", code, out, e)
			}
			if len(asked) != 1 || asked[0] != "backend's cloud Project is in no group. Put it in the workspace's?" {
				t.Fatalf("asked %q", asked)
			}
			moved := slices.Contains(reg.changes, `PATCH ws_backend {"parentId":"ws_acme-shop"}`)
			if moved != tc.moved {
				t.Fatalf("moved=%v after %s: %v", moved, tc.name, reg.changes)
			}
			want := "on its own in the cloud"
			if tc.moved {
				want = "put in the workspace's group"
			}
			if !strings.Contains(out, want) {
				t.Fatalf("output: %s", out)
			}
		})
	}
}

// On a terminal, "+ Create a new Project" asks for the name, starting from
// the folder's; a name whose handle is taken is refused under the field.
func TestInitAsksTheNewProjectsName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lilygo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	installFakeAuth(t, routes())
	var created map[string]any
	f := installFakeIngest(t, map[string]string{})
	f.respond = func(method, uri, body string) (string, bool) {
		path, _, _ := strings.Cut(uri, "?")
		switch {
		case method == "GET" && path == "/workspaces":
			return `{"workspaces":[{"id":"ws_lilygo","slug":"lilygo","name":"lilygo"}]}`, true
		case strings.HasPrefix(path, "/workspaces/slug-available"):
			return fmt.Sprintf(`{"available":%v}`, !strings.HasSuffix(uri, "slug=lilygo")), true
		case method == "POST" && path == "/workspaces":
			_ = json.Unmarshal([]byte(body), &created)
			return `{"workspaceId":"ws_new"}`, true
		}
		return "", false
	}
	if code, _, e := runCmd(t, "login", "--no-browser"); code != 0 {
		t.Fatal(e)
	}
	oldI, oldS, oldM, oldN := interactive, chooseStep, chooseMany, chooseName
	t.Cleanup(func() { interactive, chooseStep, chooseMany, chooseName = oldI, oldS, oldM, oldN })
	interactive = func() bool { return true }
	// Every other step is answered so that nothing leaves the test: no
	// model download (CI has a network), no background listener.
	chooseStep = func(_ tui.Step, question string, choices []tui.Choice, _ int) (int, error) {
		switch {
		case strings.HasPrefix(question, "Which Project"):
			if last := choices[len(choices)-1]; last.Label != "+ Create a new Project" || last.Detail != "you name it next" {
				t.Errorf("create row: %+v", last)
			}
			return len(choices) - 1, nil
		case strings.HasPrefix(question, "Download the memory search model"):
			return 1, nil // Not now
		}
		t.Fatalf("an unexpected question: %q", question)
		return 0, nil
	}
	chooseMany = func(_ tui.Step, _, _ string, _ []tui.Choice, _ []bool) ([]int, error) { return nil, nil }
	var initial string
	var refused string
	chooseName = func(_ tui.Step, _, _ string, start string, check func(string) string) (string, error) {
		initial = start
		refused = check("lilygo")
		if p := check("Lilygo v2"); p != "" {
			t.Errorf("a free name was refused: %s", p)
		}
		return "Lilygo v2", nil
	}
	code, out, e := runCmd(t, "init", "--sync", "off", "--agents", "claude-code")
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, e)
	}
	if initial != "lilygo" {
		t.Fatalf("the field started from %q", initial)
	}
	if !strings.Contains(refused, `the handle "lilygo" is taken`) {
		t.Fatalf("a taken handle was not refused: %q", refused)
	}
	if created["name"] != "Lilygo v2" || created["slug"] != "lilygo-v2" {
		t.Fatalf("created %v", created)
	}
	if b := binding(t, dir); *b.RemoteWorkspaceID != "ws_new" || *b.Slug != "lilygo-v2" {
		t.Fatalf("binding: %+v", b)
	}
}
