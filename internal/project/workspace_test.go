package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tryatlas/cli/internal/capture/store"
)

// newWorkspace is acme-shop/ (outside git) holding the repositories frontend/
// and backend/, a plain folder docs/, and nothing bound.
func newWorkspace(t *testing.T) (mother, frontend, backend string) {
	t.Helper()
	mother = filepath.Join(t.TempDir(), "acme-shop")
	frontend, backend = filepath.Join(mother, "frontend"), filepath.Join(mother, "backend")
	for _, d := range []string{filepath.Join(frontend, "src"), filepath.Join(backend, "api"), filepath.Join(mother, "docs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, frontend, "init", "-q")
	git(t, backend, "init", "-q")
	return mother, frontend, backend
}

func TestMembersAreTheChildRepositories(t *testing.T) {
	mother, frontend, backend := newWorkspace(t)
	// A linked worktree's .git is a file; it counts too.
	if err := os.MkdirAll(filepath.Join(mother, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(mother, "wt", ".git"), []byte("gitdir: elsewhere\n"), 0o644)
	bind(t, mother) // its .atlas/ is not a member

	got := Members(mother)
	want := []string{backend, frontend, filepath.Join(mother, "wt")}
	if len(got) != len(want) {
		t.Fatalf("members %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("members %v, want %v", got, want)
		}
	}
}

func TestWorkspaceMarker(t *testing.T) {
	dir := t.TempDir()
	if IsWorkspace(dir) {
		t.Fatal("no marker must mean no workspace")
	}
	if err := SetWorkspace(dir, true); err != nil {
		t.Fatal(err)
	}
	if !IsWorkspace(dir) {
		t.Fatal("marker not read back")
	}
	if got := Targets(dir); len(got) != 1 || got[0] != dir {
		t.Fatalf("targets of an empty workspace: %v", got)
	}
	if err := SetWorkspace(dir, false); err != nil {
		t.Fatal(err)
	}
	if IsWorkspace(dir) {
		t.Fatal("marker not removed")
	}
	if err := SetWorkspace(dir, false); err != nil {
		t.Fatalf("removing an absent marker: %v", err)
	}
}

func TestDiscoverFindsTheWorkspaceFromAMemberRepository(t *testing.T) {
	ctx := context.Background()
	mother, frontend, backend := newWorkspace(t)
	m := canon(t, mother)

	// A plain binding above a repository is still not found (ADR-0007).
	bind(t, mother)
	if _, err := Discover(ctx, backend); !errors.Is(err, ErrNotInitialised) {
		t.Fatalf("plain binding crossed the scope root: %v", err)
	}
	if got := PlainProjectAbove(ctx, backend); got != m {
		t.Fatalf("plain Project above: %q, want %q", got, m)
	}
	if got := MemoryRoot(ctx, backend); got != canon(t, backend) {
		t.Fatalf("memory without a workspace: %q", got)
	}

	if err := SetWorkspace(mother, true); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(backend, "api"), frontend, filepath.Join(mother, "docs"), mother} {
		p, err := Discover(ctx, dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if p.Root != m || p.ScopeRoot != m || p.Kind != KindWorkspace || p.WorkspaceRoot != m || p.Cwd != canon(t, dir) {
			t.Fatalf("%s: %+v", dir, p)
		}
		if got := MemoryRoot(ctx, dir); got != m {
			t.Fatalf("%s: memory at %q, want %q", dir, got, m)
		}
	}
	if got := PlainProjectAbove(ctx, backend); got != "" {
		t.Fatalf("a workspace is not a plain Project above: %q", got)
	}
	if got := Targets(m); len(got) != 3 || got[0] != m || got[1] != canon(t, backend) || got[2] != canon(t, frontend) {
		t.Fatalf("targets %v", got)
	}

	// A member bound on its own is split off: its own Project and memory.
	bind(t, backend)
	p, err := Discover(ctx, filepath.Join(backend, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != canon(t, backend) || p.Kind != KindPlain || p.WorkspaceRoot != "" || p.ScopeRoot != canon(t, backend) {
		t.Fatalf("split-off member: %+v", p)
	}
	if got := MemoryRoot(ctx, backend); got != canon(t, backend) {
		t.Fatalf("split-off member memory: %q", got)
	}
}

func TestAWorkspaceMarkerWithoutABindingIsIgnored(t *testing.T) {
	mother, _, backend := newWorkspace(t)
	if err := SetWorkspace(mother, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), backend); !errors.Is(err, ErrNotInitialised) {
		t.Fatalf("unbound workspace: %v", err)
	}
}

// bindIn binds dir to the cloud organisation org (a local binding for "").
func bindIn(t *testing.T, dir, org string) {
	t.Helper()
	if org == "" {
		bind(t, dir)
		return
	}
	s, err := store.Open(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := canon(t, dir)
	if err := s.PutBinding(context.Background(), store.Binding{WorkspaceID: c, Root: c, Mode: store.ModeCloud, OrgID: &org, Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// boundWorkspace is newWorkspace with acme-shop/ bound as a workspace and
// backend/ bound and linked to it: a member Project. frontend/ is unbound.
func boundWorkspace(t *testing.T) (mother, frontend, backend string) {
	t.Helper()
	mother, frontend, backend = newWorkspace(t)
	bind(t, mother)
	if err := SetWorkspace(mother, true); err != nil {
		t.Fatal(err)
	}
	bind(t, backend)
	if err := Join(backend, mother); err != nil {
		t.Fatal(err)
	}
	return canon(t, mother), canon(t, frontend), canon(t, backend)
}

// ADR-0016: a member Project records into its own store and shares the
// workspace's memory; an unbound member still records into the workspace.
func TestAMemberProjectHasItsOwnStoreAndTheWorkspacesMemory(t *testing.T) {
	ctx := context.Background()
	mother, frontend, backend := boundWorkspace(t)

	body, err := os.ReadFile(filepath.Join(backend, ".atlas", "workspace.json"))
	if err != nil || !strings.Contains(string(body), `"memberOf": ".."`) || strings.Contains(string(body), `"workspace"`) {
		t.Fatalf("member marker: %s %v", body, err)
	}
	for _, dir := range []string{backend, filepath.Join(backend, "api")} {
		p, err := Discover(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.Root != backend || p.Kind != KindMember || p.WorkspaceRoot != mother || p.ScopeRoot != mother || p.Broken != "" {
			t.Fatalf("%s: %+v", dir, p)
		}
		if got := MemoryRoot(ctx, dir); got != mother {
			t.Fatalf("%s: memory at %q, want the workspace's", dir, got)
		}
	}
	p, err := Discover(ctx, frontend)
	if err != nil || p.Root != mother || p.Kind != KindWorkspace {
		t.Fatalf("unbound member: %+v %v", p, err)
	}

	if got := Members(mother); len(got) != 2 || got[0] != backend || got[1] != frontend {
		t.Fatalf("members %v", got)
	}
	if got := Targets(mother); len(got) != 3 || got[0] != mother {
		t.Fatalf("targets %v", got)
	}
	if got := Targets(backend); len(got) != 1 || got[0] != backend {
		t.Fatalf("a member Project's own targets: %v", got)
	}
	if MembershipOf(ctx, mother, backend) != Linked || MembershipOf(ctx, mother, frontend) != Unbound {
		t.Fatal("membership")
	}
	if ws, ok := LinkOf(backend); !ok || ws != mother {
		t.Fatalf("link of %s: %q %v", backend, ws, ok)
	}
}

func TestABrokenLinkMakesAPlainProject(t *testing.T) {
	ctx := context.Background()
	mother, _, backend := boundWorkspace(t)
	plain := func(want string) {
		t.Helper()
		p, err := Discover(ctx, backend)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kind != KindPlain || p.WorkspaceRoot != "" || p.ScopeRoot != backend || !strings.Contains(p.Broken, want) {
			t.Fatalf("want a plain Project (%s): %+v", want, p)
		}
		if got := MemoryRoot(ctx, backend); got != backend {
			t.Fatalf("memory of a broken member: %q", got)
		}
	}

	// The workspace turned off.
	if err := SetWorkspace(mother, false); err != nil {
		t.Fatal(err)
	}
	plain("is not a workspace")
	if MembershipOf(ctx, mother, backend) != Linked {
		t.Fatal("membership reads the marker only")
	}

	// A marker without a binding.
	if err := SetWorkspace(mother, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(mother, ".atlas", "sessions.db")); err != nil {
		t.Fatal(err)
	}
	plain("is not bound")

	// Two organisations never share a workspace; a local binding goes with any.
	bindIn(t, mother, "org_a")
	bindIn(t, backend, "org_b")
	plain("another organisation")
	bindIn(t, backend, "org_a")
	if p, _ := Discover(ctx, backend); p.Kind != KindMember {
		t.Fatalf("same organisation: %+v", p)
	}
	bindIn(t, backend, "")
	if p, _ := Discover(ctx, backend); p.Kind != KindMember {
		t.Fatalf("a local member of a cloud workspace: %+v", p)
	}
}

// The link is relative: moving the whole tree keeps it.
func TestMovingTheWorkspaceKeepsTheLink(t *testing.T) {
	ctx := context.Background()
	mother, _, _ := boundWorkspace(t)
	moved := filepath.Join(t.TempDir(), "renamed-shop")
	if err := os.Rename(mother, moved); err != nil {
		t.Fatal(err)
	}
	p, err := Discover(ctx, filepath.Join(moved, "backend", "api"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindMember || p.WorkspaceRoot != canon(t, moved) {
		t.Fatalf("after the move: %+v", p)
	}
}

func TestJoinAndLeave(t *testing.T) {
	ctx := context.Background()
	mother, frontend, backend := newWorkspace(t)
	bind(t, mother)
	if err := SetWorkspace(mother, true); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(mother, "services", "api")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Join(deep, mother); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(deep, ".atlas", "workspace.json")); !strings.Contains(string(body), `"memberOf": "../.."`) {
		t.Fatalf("deeper link: %s", body)
	}
	for _, bad := range []string{mother, t.TempDir()} {
		if err := Join(bad, mother); !errors.Is(err, ErrNotMember) {
			t.Fatalf("join %s: %v", bad, err)
		}
	}

	// Workspaces do not nest, either way round.
	if err := SetWorkspace(backend, true); err != nil {
		t.Fatal(err)
	}
	if err := Join(backend, mother); !errors.Is(err, ErrNested) {
		t.Fatalf("a workspace joined another: %v", err)
	}
	if err := SetWorkspace(backend, false); err != nil {
		t.Fatal(err)
	}
	if err := Join(backend, mother); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkspace(backend, true); !errors.Is(err, ErrNested) {
		t.Fatalf("a member became a workspace: %v", err)
	}
	if err := SetWorkspace(backend, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := LinkOf(backend); !ok {
		t.Fatal("turning the workspace marker off dropped the link")
	}

	// Bound on its own: independent, and not a member until it joins.
	bind(t, backend)
	if err := Leave(backend); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(backend, ".atlas", "workspace.json")); !os.IsNotExist(err) {
		t.Fatalf("an empty marker stays: %v", err)
	}
	if MembershipOf(ctx, mother, backend) != Independent || slices.Contains(Members(mother), backend) {
		t.Fatal("an independent repository is a member")
	}
	if got := EnclosingWorkspace(ctx, backend); got != canon(t, mother) {
		t.Fatalf("enclosing workspace of an independent repository: %q", got)
	}

	// Linked to another workspace.
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	m := readMarker(frontend)
	rel, _ := filepath.Rel(canon(t, frontend), canon(t, other))
	m.MemberOf = rel
	if err := writeMarker(frontend, m); err != nil {
		t.Fatal(err)
	}
	bind(t, frontend)
	if MembershipOf(ctx, mother, frontend) != Elsewhere || slices.Contains(Members(mother), frontend) {
		t.Fatal("a repository linked elsewhere is a member")
	}
}

func TestEnclosingWorkspace(t *testing.T) {
	ctx := context.Background()
	mother, _, backend := newWorkspace(t)
	if got := EnclosingWorkspace(ctx, backend); got != "" {
		t.Fatalf("no workspace yet: %q", got)
	}
	bind(t, mother)
	if err := SetWorkspace(mother, true); err != nil {
		t.Fatal(err)
	}
	m := canon(t, mother)
	if got := EnclosingWorkspace(ctx, backend); got != m {
		t.Fatalf("a repository in the workspace: %q", got)
	}
	docs := filepath.Join(mother, "docs")
	for _, dir := range []string{filepath.Join(backend, "api"), docs} {
		if got := EnclosingWorkspace(ctx, dir); got != "" {
			t.Fatalf("%s is not a member folder: %q", dir, got)
		}
	}
	if _, err := AddFolder(mother, docs); err != nil {
		t.Fatal(err)
	}
	if got := EnclosingWorkspace(ctx, docs); got != m {
		t.Fatalf("a folder added by hand: %q", got)
	}
}
