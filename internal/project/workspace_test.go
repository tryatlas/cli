package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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
		if p.Root != m || p.ScopeRoot != m || !p.Workspace || p.Cwd != canon(t, dir) {
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
	if p.Root != canon(t, backend) || p.Workspace || p.ScopeRoot != canon(t, backend) {
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
