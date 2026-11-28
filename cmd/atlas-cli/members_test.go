package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tryatlas/cli/internal/capture/record"
	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/hooks"
	"github.com/tryatlas/cli/internal/project"
	"github.com/tryatlas/cli/internal/tui"
)

// kindOf is the kind of Project an agent started in dir records into, and
// its workspace folder.
func kindOf(t *testing.T, dir string) (project.Kind, string) {
	t.Helper()
	p, err := project.Discover(context.Background(), dir)
	if err != nil {
		t.Fatalf("discover %s: %v", dir, err)
	}
	return p.Kind, p.WorkspaceRoot
}

// ADR-0016: workspace add --all makes each repository a Project of its own;
// remove and off let them go on their own, keeping what they recorded.
func TestWorkspaceAddMakesEachRepositoryAProject(t *testing.T) {
	mother, frontend, backend := workspaceDir(t)
	code, _, e := runCmd(t, "init", "--local", "--workspace", "--agents", "claude-code")
	if code != 0 || !strings.Contains(e, "record into the workspace until each is a Project of its own: atlas-cli workspace add --all") {
		t.Fatalf("init %d: %s", code, e)
	}
	// Without a terminal init binds no repository.
	for _, d := range []string{frontend, backend} {
		if k, _ := kindOf(t, d); k != project.KindWorkspace {
			t.Fatalf("%s: %v before workspace add", d, k)
		}
	}

	code, out, e := runCmd(t, "workspace", "add", "--all")
	if code != 0 {
		t.Fatalf("add --all %d: %s %s", code, out, e)
	}
	for _, d := range []string{frontend, backend} {
		if !strings.Contains(out, filepath.Base(d)+": Project in the workspace (local, nothing is synced).") {
			t.Fatalf("no line for %s: %s", d, out)
		}
		if b := binding(t, d); b.Root != d || b.Mode != "local" {
			t.Fatalf("%s binding: %+v", d, b)
		}
		if k, ws := kindOf(t, filepath.Join(d)); k != project.KindMember || ws != mother {
			t.Fatalf("%s: %v in %q", d, k, ws)
		}
		if !hooked(d) {
			t.Fatalf("%s is not hooked", d)
		}
	}

	t.Chdir(backend)
	if _, out, _ := runCmd(t, "status"); !strings.Contains(out, "Project:   "+backend) || !strings.Contains(out, "Workspace: a Project in "+mother) {
		t.Fatalf("status in a member: %s", out)
	}
	t.Chdir(mother)
	if _, out, _ := runCmd(t, "status"); !strings.Contains(out, "2 repositories: backend, frontend (each a Project of its own; one shared memory)") {
		t.Fatalf("status in the workspace: %s", out)
	}
	if _, out, _ := runCmd(t, "workspace"); !strings.Contains(out, "├─ backend") || !strings.Contains(out, "└─ frontend") || strings.Contains(out, "records here") {
		t.Fatalf("tree: %s", out)
	}
	if _, out, _ := runCmd(t, "workspace", "add", "--all"); !strings.Contains(out, "Every repository in the workspace is a Project already.") {
		t.Fatalf("add --all again: %s", out)
	}
	if code, _, e := runCmd(t, "workspace", "add"); code == 0 || !strings.Contains(e, "--all") {
		t.Fatalf("add without a folder: %d %s", code, e)
	}

	// Removed, backend goes on on its own; added again, it joins as it is.
	if code, out, e := runCmd(t, "workspace", "remove", "backend"); code != 0 || !strings.Contains(out, "is a Project on its own now") {
		t.Fatalf("remove: %d %s %s", code, out, e)
	}
	if k, _ := kindOf(t, backend); k != project.KindPlain || !hooked(backend) {
		t.Fatalf("removed member: %v hooked=%v", k, hooked(backend))
	}
	if _, out, _ := runCmd(t, "workspace"); !strings.Contains(out, "Project on its own") || !strings.Contains(out, "atlas-cli workspace add backend") {
		t.Fatalf("tree after remove: %s", out)
	}
	if code, out, e := runCmd(t, "workspace", "add", "backend"); code != 0 || !strings.Contains(out, "joined as it was bound") {
		t.Fatalf("add back: %d %s %s", code, out, e)
	}

	// off: each Project goes on on its own, hooked; the workspace is a folder again.
	if code, out, e := runCmd(t, "workspace", "off"); code != 0 || !strings.Contains(out, "go on as Projects of their own") {
		t.Fatalf("off: %d %s %s", code, out, e)
	}
	for _, d := range []string{frontend, backend} {
		if _, ok := project.LinkOf(d); ok || !hooked(d) {
			t.Fatalf("%s after off: linked=%v hooked=%v", d, ok, hooked(d))
		}
	}
	if project.IsWorkspace(mother) {
		t.Fatal("still a workspace")
	}
}

// atlas-cli init inside a repository of a workspace binds it as a Project in
// the workspace; --workspace=false binds it on its own, --workspace takes a
// Project on its own in.
func TestInitInARepositoryOfAWorkspaceJoinsIt(t *testing.T) {
	mother, frontend, backend := workspaceDir(t)
	if code, _, e := runCmd(t, "init", "--local", "--workspace", "--yes"); code != 0 {
		t.Fatal(e)
	}

	t.Chdir(backend)
	code, _, e := runCmd(t, "init", "--local")
	if code != 0 || !strings.Contains(e, "Binding "+backend+" as a Project in the workspace "+mother) {
		t.Fatalf("init in a member: %d %s", code, e)
	}
	if k, ws := kindOf(t, backend); k != project.KindMember || ws != mother {
		t.Fatalf("backend: %v in %q", k, ws)
	}

	// On its own: splitting off asks first, as before.
	t.Chdir(frontend)
	if code, _, e := runCmd(t, "init", "--local", "--workspace=false"); code != 1 || !strings.Contains(e, "is part of the workspace") || !strings.Contains(e, "--yes") {
		t.Fatalf("split off without --yes: %d %s", code, e)
	}
	if code, _, e := runCmd(t, "init", "--local", "--workspace=false", "--yes"); code != 0 {
		t.Fatal(e)
	}
	if k, _ := kindOf(t, frontend); k != project.KindPlain || project.MembershipOf(context.Background(), mother, frontend) != project.Independent {
		t.Fatalf("frontend: %v", k)
	}

	// Init again keeps each as it is.
	for _, c := range []struct {
		dir  string
		want project.Kind
	}{{backend, project.KindMember}, {frontend, project.KindPlain}} {
		t.Chdir(c.dir)
		if code, _, e := runCmd(t, "init", "--local", "--yes"); code != 0 {
			t.Fatal(e)
		}
		if k, _ := kindOf(t, c.dir); k != c.want {
			t.Fatalf("%s after init again: %v, want %v", c.dir, k, c.want)
		}
	}

	// The flag moves each across.
	t.Chdir(frontend)
	if code, _, e := runCmd(t, "init", "--local", "--yes", "--workspace"); code != 0 {
		t.Fatal(e)
	}
	t.Chdir(backend)
	if code, _, e := runCmd(t, "init", "--local", "--yes", "--workspace=false"); code != 0 {
		t.Fatal(e)
	}
	if k, _ := kindOf(t, frontend); k != project.KindMember {
		t.Fatalf("frontend with --workspace: %v", k)
	}
	if k, _ := kindOf(t, backend); k != project.KindPlain {
		t.Fatalf("backend with --workspace=false: %v", k)
	}
	if _, ok := project.LinkOf(backend); ok {
		t.Fatal("backend kept its link")
	}

	// The workspace bound as a single folder again lets its Projects go on
	// their own, as atlas-cli workspace off does.
	t.Chdir(mother)
	if code, _, e := runCmd(t, "init", "--local", "--yes", "--workspace=false"); code != 0 || !strings.Contains(e, "frontend go on as Projects of their own") {
		t.Fatalf("workspace=false in the workspace: %d %s", code, e)
	}
	if _, ok := project.LinkOf(frontend); ok || project.IsWorkspace(mother) {
		t.Fatal("frontend kept its link to a folder that is no workspace")
	}
}

// On a terminal, init lists the repositories, all checked, and binds the
// ones kept; the others record into the workspace.
func TestInitOffersTheRepositoriesAsProjects(t *testing.T) {
	ctx := context.Background()
	mother, frontend, backend := workspaceDir(t)
	if code, _, e := runCmd(t, "init", "--local", "--workspace", "--yes"); code != 0 {
		t.Fatal(e)
	}
	old := chooseMany
	t.Cleanup(func() { chooseMany = old })
	var offered []tui.Choice
	allChecked := false
	chooseMany = func(_ tui.Step, _, _ string, choices []tui.Choice, checked []bool) ([]int, error) {
		offered, allChecked = choices, !slices.Contains(checked, false)
		return []int{0}, nil // backend only
	}
	cmd := newInitCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := offerMembers(cmd, mother, true, initSteps(true, true)); err != nil {
		t.Fatal(err)
	}
	if len(offered) != 2 || offered[0].Label != "backend" || offered[1].Label != "frontend" || !allChecked {
		t.Fatalf("offered %+v (all checked: %v)", offered, allChecked)
	}
	if k, _ := kindOf(t, backend); k != project.KindMember || !strings.Contains(out.String(), "backend: Project in the workspace (local") {
		t.Fatalf("backend: %v; %s", k, out.String())
	}
	if project.MembershipOf(ctx, mother, frontend) != project.Unbound {
		t.Fatal("frontend was bound though unchecked")
	}
}

func TestInitStepsHaveProjectsBeforeAgents(t *testing.T) {
	steps := initSteps(false, true)
	if i, j := slices.Index(steps, "Projects"), slices.Index(steps, "Agents"); i != 2 || j != 3 {
		t.Fatalf("cloud steps: %v", steps)
	}
	if steps := initSteps(true, true); steps[0] != "Projects" || steps[1] != "Agents" {
		t.Fatalf("local steps: %v", steps)
	}
	if steps := initSteps(true, false); slices.Contains(steps, "Projects") {
		t.Fatalf("no members, no step: %v", steps)
	}
}

// commitRepo gives the repository dir a first commit and an origin, so the
// server can match it by fingerprint.
func commitRepo(t *testing.T, dir, origin string) {
	t.Helper()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@e.test", "-c", "commit.gpgsign=false"}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte(filepath.Base(dir)), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "first")
	run("remote", "add", "origin", origin)
}

// In a cloud workspace each repository matches its cloud Project by git
// fingerprint, or gets a new one (named after the workspace and the folder
// when the folder's handle is taken), in the workspace's organisation.
func TestWorkspaceAddInTheCloud(t *testing.T) {
	mother, frontend, backend := workspaceDir(t)
	commitRepo(t, backend, "https://github.com/Acme/backend.git")
	commitRepo(t, frontend, "https://github.com/Acme/frontend.git")
	installFakeAuth(t, routes())
	f := installFakeIngest(t, map[string]string{})
	f.respond = func(method, uri, body string) (string, bool) {
		switch {
		case method == "POST" && uri == "/workspaces/connect" && strings.Contains(body, "github.com/acme/backend"):
			return `{"status":"connected","workspaceId":"ws_backend","slug":"backend","reason":"root commit"}`, true
		case method == "POST" && uri == "/workspaces/connect":
			return `{"status":"none"}`, true
		case strings.HasPrefix(uri, "/workspaces/slug-available"):
			return fmt.Sprintf(`{"available":%v}`, !strings.HasSuffix(uri, "slug=frontend")), true
		case method == "POST" && uri == "/workspaces":
			var req struct {
				Slug string `json:"slug"`
			}
			_ = json.Unmarshal([]byte(body), &req)
			return `{"workspaceId":"ws_` + req.Slug + `"}`, true
		}
		return "", false
	}
	if code, _, e := runCmd(t, "login", "--no-browser"); code != 0 {
		t.Fatal(e)
	}
	if code, _, e := runCmd(t, "init", "--workspace", "--create", "--yes"); code != 0 {
		t.Fatalf("init: %d %s", code, e)
	}
	if b := binding(t, mother); *b.RemoteWorkspaceID != "ws_acme-shop" {
		t.Fatalf("workspace binding: %+v", b)
	}

	code, out, e := runCmd(t, "workspace", "add", "--all")
	if code != 0 {
		t.Fatalf("add --all: %d %s %s", code, out, e)
	}
	if !strings.Contains(out, "backend: Project in the workspace (Atlas / backend, matched by root commit).") ||
		!strings.Contains(out, "frontend: Project in the workspace (Atlas / frontend, created).") {
		t.Fatalf("output: %s", out)
	}
	be, fe := binding(t, backend), binding(t, frontend)
	if be.Mode != "cloud" || *be.OrgID != "org_1" || *be.RemoteWorkspaceID != "ws_backend" || *be.Slug != "backend" || be.ImportApproved {
		t.Fatalf("backend: %+v", be)
	}
	if fe.Mode != "cloud" || *fe.OrgID != "org_1" || *fe.RemoteWorkspaceID != "ws_acme-shop-frontend" || *fe.Slug != "acme-shop-frontend" {
		t.Fatalf("frontend: %+v", fe)
	}
	if k, ws := kindOf(t, frontend); k != project.KindMember || ws != mother {
		t.Fatalf("frontend: %v in %q", k, ws)
	}

	// A repository joining by its own init stays in the workspace's organisation.
	infra := filepath.Join(mother, "infra")
	if err := os.MkdirAll(infra, 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("git", "-C", infra, "init", "-q")
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Chdir(infra)
	if code, _, e := runCmd(t, "init", "--org", "org_9"); code != 1 || !strings.Contains(e, "the workspace is in the organisation org_1") {
		t.Fatalf("init in another organisation: %d %s", code, e)
	}
}

// The P1 exit criterion (ADR-0016): an agent started in a member Project
// records there; one started in a repository that is not a Project records
// into the workspace, as before.
func TestAHookInAMemberProjectRecordsThere(t *testing.T) {
	mother, frontend, backend := workspaceDir(t)
	if code, _, e := runCmd(t, "init", "--local", "--workspace", "--yes"); code != 0 {
		t.Fatal(e)
	}
	if code, _, e := runCmd(t, "workspace", "add", "backend"); code != 0 {
		t.Fatal(e)
	}
	old := stdin
	t.Cleanup(func() { stdin = old })
	for _, s := range []struct{ id, cwd string }{{"s-backend", backend}, {"s-frontend", frontend}} {
		stdin = strings.NewReader(`{"session_id":"` + s.id + `","cwd":"` + s.cwd + `"}`)
		if code, _, e := runCmd(t, "hooks", "claude-code", "session-start"); code != 0 {
			t.Fatalf("%s: %d %s", s.id, code, e)
		}
	}
	sessions := func(root string) []string {
		t.Helper()
		states, err := hooks.ListStates(root, "")
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, s := range states {
			ids = append(ids, s.SessionID)
		}
		return ids
	}
	if got := sessions(backend); len(got) != 1 || got[0] != "s-backend" {
		t.Fatalf("backend's sessions: %v", got)
	}
	if got := sessions(mother); len(got) != 1 || got[0] != "s-frontend" {
		t.Fatalf("the workspace's sessions: %v", got)
	}
}

// ADR-0017 (FR-10, TRD S5): a session started in the workspace folder
// changes files in backend (a member Project) and in frontend (not bound);
// the post-commit hook of each repository links its commit into the
// workspace's store, with the folder kept on the checkpoint.
func TestPostCommitInAMemberLinksTheWorkspaceSession(t *testing.T) {
	ctx := context.Background()
	mother, frontend, backend := workspaceDir(t)
	for _, d := range []string{backend, frontend} {
		commitRepo(t, d, "https://github.com/acme/"+filepath.Base(d)+".git")
	}
	if code, _, e := runCmd(t, "init", "--local", "--workspace", "--yes"); code != 0 {
		t.Fatal(e)
	}
	if code, _, e := runCmd(t, "workspace", "add", "backend"); code != 0 {
		t.Fatal(e)
	}

	st, err := store.Open(mother, false)
	if err != nil {
		t.Fatal(err)
	}
	rec := record.New(st, mother)
	sess := record.Session{Agent: "claude-code", NativeID: "s-1", Cwd: mother}
	if _, err := rec.Prompt(ctx, sess, "add the user API and its form"); err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{"backend/api.go", "frontend/form.ts"} {
		abs := filepath.Join(mother, p)
		if err := os.WriteFile(abs, []byte("// "+p+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		args, _ := json.Marshal(map[string]string{"file_path": abs, "content": "// " + p + "\n"})
		id := fmt.Sprintf("w%d", i)
		_ = rec.ToolCall(ctx, sess, record.Tool{NativeID: id, Name: "Write", Status: "running", Arguments: args})
		_ = rec.ToolCall(ctx, sess, record.Tool{NativeID: id, Name: "Write", Status: "completed"})
	}
	_ = st.Close()

	commit := func(dir, file string) {
		t.Helper()
		for _, args := range [][]string{{"add", file}, {"commit", "-q", "-m", "work"}} {
			c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@e.test", "-c", "commit.gpgsign=false"}, args...)...)
			c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
		t.Chdir(dir)
		if code, _, e := runCmd(t, "hooks", "git", "post-commit"); code != 0 {
			t.Fatalf("post-commit in %s: %s", dir, e)
		}
	}
	commit(backend, "api.go")
	commit(frontend, "form.ts")

	st, err = store.Open(mother, false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.DB().Query(`SELECT files_touched FROM checkpoint ORDER BY files_touched`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var f string
		_ = rows.Scan(&f)
		files = append(files, f)
	}
	if strings.Join(files, " ") != `["backend/api.go"] ["frontend/form.ts"]` {
		t.Fatalf("workspace checkpoints: %v", files)
	}
	// The member Project's own store links nothing of the workspace's.
	if b, err := store.Open(backend, false); err == nil {
		var n int
		_ = b.DB().QueryRow(`SELECT COUNT(*) FROM checkpoint`).Scan(&n)
		_ = b.Close()
		if n != 0 {
			t.Fatalf("%d checkpoints in backend's own store", n)
		}
	}
}
