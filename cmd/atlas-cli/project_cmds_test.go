package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/hooks"
	"github.com/tryatlas/cli/internal/project"
)

// fakeIngest records requests and answers from a route table.
type fakeIngest struct {
	mu     sync.Mutex
	bodies map[string]string
	routes map[string]string
	// respond, when set, answers before the route table (uri is the path
	// and query); ok false falls through to it.
	respond func(method, uri, body string) (answer string, ok bool)
}

func installFakeIngest(t *testing.T, routes map[string]string) *fakeIngest {
	t.Helper()
	f := &fakeIngest{bodies: map[string]string{}, routes: routes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		f.mu.Lock()
		f.bodies[key] = string(b)
		body, ok := f.routes[key]
		if f.respond != nil {
			if a, aok := f.respond(r.Method, r.URL.RequestURI(), string(b)); aok {
				body, ok = a, true
			}
		}
		f.mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(401)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ATLAS_INGEST_URL", srv.URL)
	return f
}

// gitRepo makes a repository with one commit and an origin, and chdirs into it.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(filepath.Join(dir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@e.test", "-c", "commit.gpgsign=false"}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "first")
	run("remote", "add", "origin", "https://github.com/Acme/Backend.git")
	t.Chdir(dir)
	c, _ := project.Canonical(dir)
	return c
}

func binding(t *testing.T, root string) *store.Binding {
	t.Helper()
	s, err := store.Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, err := s.Binding(context.Background())
	if err != nil || b == nil {
		t.Fatalf("binding: %+v %v", b, err)
	}
	return b
}

func TestInitLocal(t *testing.T) {
	root := gitRepo(t)
	code, out, e := runCmd(t, "init", "--local")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	b := binding(t, root)
	if b.Mode != "local" || !b.ImportApproved || !b.Enabled || b.WorkspaceID != root || b.Root != root || b.DrainState != "ok" {
		t.Fatalf("%+v", b)
	}
	if b.RootCommitSHA == nil || *b.GitURL != "github.com/acme/backend" {
		t.Fatalf("fingerprint: %+v", b)
	}
	if !strings.Contains(out, "locally") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(root, ".atlas", ".gitignore")); err != nil {
		t.Fatal(err)
	}
}

func TestInitCloudMatchedByFingerprint(t *testing.T) {
	root := gitRepo(t)
	installFakeAuth(t, routes())
	f := installFakeIngest(t, map[string]string{
		"POST /workspaces/connect": `{"status":"connected","workspaceId":"ws_1","slug":"backend","reason":"fingerprint"}`,
	})
	if code, _, e := runCmd(t, "login", "--no-browser"); code != 0 {
		t.Fatal(e)
	}
	code, out, e := runCmd(t, "init")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if !strings.Contains(out, "Bound "+root+" to Atlas / backend (matched by fingerprint)") {
		t.Fatalf("output: %s", out)
	}
	var req map[string]any
	_ = json.Unmarshal([]byte(f.bodies["POST /workspaces/connect"]), &req)
	if req["orgId"] != "org_1" || req["create"] != false || req["gitUrl"] != "github.com/acme/backend" || req["rootCommitSha"] == nil {
		t.Fatalf("connect body: %s", f.bodies["POST /workspaces/connect"])
	}
	if _, ok := req["workspaceId"]; ok {
		t.Fatal("absent fields must be omitted, not null")
	}
	b := binding(t, root)
	if b.Mode != "cloud" || *b.OrgID != "org_1" || *b.RemoteWorkspaceID != "ws_1" || *b.Slug != "backend" || b.ImportApproved {
		t.Fatalf("%+v", b)
	}
}

func TestInitCreate(t *testing.T) {
	gitRepo(t)
	installFakeAuth(t, routes())
	f := installFakeIngest(t, map[string]string{
		"GET /workspaces/slug-available": `{"available":true}`,
		"POST /workspaces":               `{"workspaceId":"ws_new"}`,
	})
	_, _, _ = runCmd(t, "login", "--no-browser")
	code, out, e := runCmd(t, "init", "--create", "--visibility", "restricted")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	var req map[string]any
	_ = json.Unmarshal([]byte(f.bodies["POST /workspaces"]), &req)
	if req["slug"] != "backend" || req["name"] != "backend" || req["visibility"] != "restricted" || req["orgId"] != "org_1" {
		t.Fatalf("create body: %s", f.bodies["POST /workspaces"])
	}
	if !strings.Contains(out, "(created)") {
		t.Fatal(out)
	}
}

func TestInitWithoutMatchNeedsAFlagWhenNotInteractive(t *testing.T) {
	gitRepo(t)
	installFakeAuth(t, routes())
	installFakeIngest(t, map[string]string{
		"POST /workspaces/connect": `{"status":"none"}`,
		"GET /workspaces":          `{"workspaces":[{"id":"ws_1","slug":"other"}]}`,
	})
	_, _, _ = runCmd(t, "login", "--no-browser")
	code, _, e := runCmd(t, "init")
	if code != 1 || !strings.Contains(e, "pass --project <id> (other (ws_1)) or --create") {
		t.Fatalf("exit %d: %s", code, e)
	}
}

func TestInitInSubfolderNeedsYes(t *testing.T) {
	root := gitRepo(t)
	t.Chdir(filepath.Join(root, "svc"))
	code, _, e := runCmd(t, "init", "--local")
	if code != 1 || !strings.Contains(e, "Capture is recorded for this folder only") || !strings.Contains(e, "--yes") {
		t.Fatalf("exit %d: %s", code, e)
	}
	if code, _, e := runCmd(t, "init", "--local", "--yes"); code != 0 {
		t.Fatal(e)
	}
	b := binding(t, filepath.Join(root, "svc"))
	if b.WorkspaceID != filepath.Join(root, "svc") {
		t.Fatalf("%+v", b)
	}
}

func TestInitRefusesWhileAnotherWriterHoldsTheLock(t *testing.T) {
	root := gitRepo(t)
	l, err := store.Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()
	code, _, e := runCmd(t, "init", "--local")
	if code != 1 || !strings.Contains(e, "another Atlas window is recording this Project") {
		t.Fatalf("exit %d: %s", code, e)
	}
}

func TestGatesAndStatus(t *testing.T) {
	root := gitRepo(t)
	installFakeAuth(t, routes())
	if code, _, e := runCmd(t, "init", "--local"); code != 0 {
		t.Fatal(e)
	}
	t.Chdir(filepath.Join(root, "svc")) // gates resolve the Project by walking up
	if code, _, e := runCmd(t, "memory", "disable"); code != 0 {
		t.Fatal(e)
	}
	if project.MemorySharing(root) {
		t.Fatal("memory still on")
	}
	if code, _, e := runCmd(t, "capture", "disable"); code != 0 {
		t.Fatal(e)
	}
	if binding(t, root).Enabled {
		t.Fatal("capture still on")
	}
	code, out, e := runCmd(t, "status")
	if code != 0 {
		t.Fatal(e)
	}
	for _, want := range []string{"Account:   not signed in", "Project:   " + root, "Binding:   local, not synced",
		"Memory:    off", "Capture:   paused", "Recorder:  idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

// workspaceDir is acme-shop/ outside git with the repositories frontend/ and
// backend/; the test runs in acme-shop/.
func workspaceDir(t *testing.T) (mother, frontend, backend string) {
	t.Helper()
	base, _ := project.Canonical(t.TempDir())
	mother = filepath.Join(base, "acme-shop")
	frontend, backend = filepath.Join(mother, "frontend"), filepath.Join(mother, "backend")
	for _, d := range []string{frontend, backend} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		c := exec.Command("git", "-C", d, "init", "-q")
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}
	t.Chdir(mother)
	return mother, frontend, backend
}

func TestInitWorkspace(t *testing.T) {
	mother, frontend, backend := workspaceDir(t)
	code, _, e := runCmd(t, "init", "--local", "--workspace", "--agents", "claude-code")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if !strings.Contains(e, "Binding "+mother+" as a workspace of 2 repositories (backend, frontend).") {
		t.Fatalf("no workspace note: %s", e)
	}
	if b := binding(t, mother); b.Root != mother || b.RootCommitSHA != nil {
		t.Fatalf("%+v", b)
	}
	if !project.IsWorkspace(mother) {
		t.Fatal("marker not written")
	}
	// The agent's hook and memory entry are in every member repository.
	for _, d := range []string{mother, frontend, backend} {
		for _, f := range []string{".claude/settings.local.json", ".mcp.json"} {
			if _, err := os.Stat(filepath.Join(d, f)); err != nil {
				t.Errorf("%s: %v", d, err)
			}
		}
	}
	// From a member, status names the workspace Project.
	t.Chdir(backend)
	code, out, e := runCmd(t, "status")
	if code != 0 || !strings.Contains(out, "Project:   "+mother) || !strings.Contains(out, "2 repositories: backend, frontend") {
		t.Fatalf("status %d: %s %s", code, out, e)
	}
	// Removing the agent cleans the members too.
	if code, _, e := runCmd(t, "agents", "remove", "claude-code"); code != 0 {
		t.Fatalf("remove: %s", e)
	}
	for _, d := range []string{mother, frontend, backend} {
		if a, _ := hooks.Get("claude-code"); a.State(d) != hooks.Absent {
			t.Errorf("%s still hooked", d)
		}
	}

	// Init again without the flag (another cloud Project, say) keeps it a workspace.
	t.Chdir(mother)
	if code, _, e := runCmd(t, "init", "--local", "--yes"); code != 0 || !project.IsWorkspace(mother) || strings.Contains(e, "not captured") {
		t.Fatalf("re-init dropped the workspace: exit %d: %s", code, e)
	}

	// --workspace=false binds the folder alone and drops the marker.
	t.Chdir(mother)
	if code, _, e := runCmd(t, "init", "--local", "--yes", "--workspace=false"); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if project.IsWorkspace(mother) {
		t.Fatal("marker kept")
	}
}

func TestInitWorkspaceRefusesAFolderWithoutRepositories(t *testing.T) {
	dir, _ := project.Canonical(t.TempDir())
	t.Chdir(dir)
	code, _, e := runCmd(t, "init", "--local", "--workspace")
	if code == 0 || !strings.Contains(e, "holds no git repository") {
		t.Fatalf("exit %d: %s", code, e)
	}
	// Without the flag a plain folder is a plain Project.
	if code, _, e := runCmd(t, "init", "--local"); code != 0 || project.IsWorkspace(dir) {
		t.Fatalf("exit %d: %s", code, e)
	}
}

func hooked(dir string) bool {
	a, _ := hooks.Get("claude-code")
	return a.State(dir) == hooks.Current
}

// The cli-test case: the folder was bound before any repository existed in
// it. workspace on converts it, sync hooks a repository added later, and
// add/remove handle a folder without git.
func TestWorkspaceCommand(t *testing.T) {
	mother, _ := project.Canonical(t.TempDir())
	t.Chdir(mother)
	if code, _, e := runCmd(t, "init", "--local", "--agents", "claude-code"); code != 0 {
		t.Fatalf("init: %s", e)
	}
	newRepo := func(name string) string {
		d := filepath.Join(mother, name)
		_ = os.MkdirAll(d, 0o755)
		c := exec.Command("git", "-C", d, "init", "-q")
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		return d
	}
	frontend := newRepo("frontend")

	// Not a workspace yet: the repository is on its own, and status says why.
	if code, _, e := runCmd(t, "workspace", "sync"); code == 0 || !strings.Contains(e, "not a workspace") {
		t.Fatalf("sync before on: %d %s", code, e)
	}
	t.Chdir(frontend)
	if _, out, _ := runCmd(t, "status"); !strings.Contains(out, "atlas-cli init --workspace") {
		t.Fatalf("no hint: %s", out)
	}

	t.Chdir(mother)
	code, out, e := runCmd(t, "workspace", "on")
	if code != 0 || !strings.Contains(out, "is now a workspace (1 repository: frontend)") || !hooked(frontend) {
		t.Fatalf("on: %d %s %s", code, out, e)
	}

	// A repository added later: shown as not hooked, then synced. From a member.
	backend := newRepo("backend")
	t.Chdir(frontend)
	if _, out, _ := runCmd(t, "workspace"); !strings.Contains(out, "not hooked: claude-code") || !strings.Contains(out, "atlas-cli workspace sync") {
		t.Fatalf("show: %s", out)
	}
	if code, _, e := runCmd(t, "workspace", "sync"); code != 0 || !hooked(backend) {
		t.Fatalf("sync: %d %s", code, e)
	}
	if _, out, _ := runCmd(t, "workspace"); strings.Contains(out, "not hooked") {
		t.Fatalf("show after sync: %s", out)
	}

	// A folder without git joins by hand, as a Project in the workspace (ADR-0016).
	docs := filepath.Join(mother, "docs")
	_ = os.Mkdir(docs, 0o755)
	t.Chdir(mother)
	if code, out, e := runCmd(t, "workspace", "add", "docs"); code != 0 || !hooked(docs) || !strings.Contains(out, "docs: Project in the workspace (local") {
		t.Fatalf("add: %d %s %s", code, out, e)
	}
	if ws, ok := project.LinkOf(docs); !ok || ws != mother {
		t.Fatalf("docs linked to %q", ws)
	}
	if code, _, e := runCmd(t, "workspace", "add", t.TempDir()); code == 0 || !strings.Contains(e, "not inside the workspace") {
		t.Fatalf("add outside: %d %s", code, e)
	}
	if code, _, e := runCmd(t, "workspace", "remove", "frontend"); code == 0 || !strings.Contains(e, "not added by hand") {
		t.Fatalf("remove a repository: %d %s", code, e)
	}
	// Removed, it goes on as a Project on its own: bound and hooked, not a member.
	if code, out, e := runCmd(t, "workspace", "remove", "docs"); code != 0 || !hooked(docs) || !strings.Contains(out, "is a Project on its own now") {
		t.Fatalf("remove: %d %s %s", code, out, e)
	}
	if slices.Contains(project.Members(mother), docs) {
		t.Fatal("docs is still a member")
	}

	// off unhooks the members and keeps the workspace folder's own hook.
	if code, _, e := runCmd(t, "workspace", "off"); code != 0 {
		t.Fatalf("off: %s", e)
	}
	if project.IsWorkspace(mother) || hooked(frontend) || hooked(backend) || !hooked(mother) {
		t.Fatalf("off left: ws=%v fe=%v be=%v root=%v", project.IsWorkspace(mother), hooked(frontend), hooked(backend), hooked(mother))
	}
}

// A workspace is opt-in: without --workspace and without a terminal, init in
// a folder of repositories binds the folder alone and says how to capture them.
func TestInitWithoutTheWorkspaceFlagLeavesTheRepositoriesAlone(t *testing.T) {
	mother, frontend, backend := workspaceDir(t)
	code, _, e := runCmd(t, "init", "--local", "--agents", "claude-code")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if project.IsWorkspace(mother) {
		t.Fatal("became a workspace without --workspace")
	}
	if !strings.Contains(e, mother+" holds 2 repositories (backend, frontend). Agents started in them are not captured; atlas-cli workspace on captures them here.") {
		t.Fatalf("no hint: %s", e)
	}
	for _, d := range []string{frontend, backend} {
		if hooked(d) {
			t.Fatalf("%s hooked without a workspace", d)
		}
	}
	if !hooked(mother) {
		t.Fatal("the folder itself was not hooked")
	}
	// The hint's command turns it into a workspace.
	if code, out, e := runCmd(t, "workspace", "on"); code != 0 || !project.IsWorkspace(mother) || !hooked(frontend) || !hooked(backend) {
		t.Fatalf("workspace on: exit %d: %s %s", code, out, e)
	}
}
