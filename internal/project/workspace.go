package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tryatlas/cli/internal/capture/store"
)

// A workspace is a folder of repositories bound once (ADR-0015): a mother
// folder such as acme-shop/ holding frontend/ and backend/, each its own git
// repository. A member repository can be bound as a Project of its own
// (ADR-0016): its own store, linked to the workspace by its marker, sharing
// the workspace's memory. A member that is not bound records into the
// workspace's store, as under ADR-0015. Markers are sidecar files, not
// binding columns, so sessions.db stays the desktop's schema.

const workspaceFile = "workspace.json"

// marker is .atlas/workspace.json: {"workspace": true} in a workspace
// folder, {"memberOf": ".."} in a member Project. A folder is never both:
// workspaces do not nest.
type marker struct {
	Workspace bool `json:"workspace,omitempty"`
	// Folders are members named by hand (atlas-cli workspace add): folders
	// without git, or repositories deeper than one level, relative to the
	// workspace folder.
	Folders []string `json:"folders,omitempty"`
	// MemberOf is a member Project's workspace folder, relative to the
	// member ("..", or "../.." deeper down), so moving the whole tree keeps
	// the link.
	MemberOf string `json:"memberOf,omitempty"`
}

func (m marker) empty() bool { return !m.Workspace && len(m.Folders) == 0 && m.MemberOf == "" }

func readMarker(dir string) marker {
	var m marker
	b, err := os.ReadFile(filepath.Join(dir, store.DirName, workspaceFile)) //nolint:gosec // G304: the Project's own marker
	if err != nil || json.Unmarshal(b, &m) != nil {
		return marker{}
	}
	return m
}

// writeMarker writes m through a temp file and rename; an empty marker
// removes the file.
func writeMarker(dir string, m marker) error {
	path := filepath.Join(dir, store.DirName, workspaceFile)
	if m.empty() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, _ := json.MarshalIndent(m, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil { //nolint:gosec // G306: a flag, not a secret
		return err
	}
	return os.Rename(tmp, path)
}

// IsWorkspace reports whether dir carries the workspace marker.
func IsWorkspace(dir string) bool { return readMarker(dir).Workspace }

// ErrNested means a workspace would sit inside another one.
var ErrNested = errors.New("a member of a workspace cannot be a workspace itself")

// SetWorkspace writes or removes the workspace marker in dir/.atlas/.
// Turning it on again keeps the folders added by hand. A member Project
// cannot become a workspace: workspaces do not nest.
func SetWorkspace(dir string, on bool) error {
	m := readMarker(dir)
	if !on {
		m.Workspace, m.Folders = false, nil
		return writeMarker(dir, m)
	}
	if m.MemberOf != "" {
		return ErrNested
	}
	m.Workspace = true
	return writeMarker(dir, m)
}

// ErrNotMember means a folder is not below the workspace folder.
var ErrNotMember = errors.New("the folder is not inside the workspace")

// relMember spells folder relative to the workspace dir; it must be a
// directory strictly below it.
func relMember(dir, folder string) (string, error) {
	c, err := Canonical(folder)
	if err != nil {
		return "", err
	}
	if cd, err := Canonical(dir); err == nil {
		dir = cd // both spelled one way (/private/var on macOS)
	}
	if fi, err := os.Stat(c); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", folder)
	}
	rel, err := filepath.Rel(dir, c)
	if err != nil || rel == "." || !within(c, dir) || strings.HasPrefix(rel, store.DirName) {
		return "", ErrNotMember
	}
	return filepath.ToSlash(rel), nil
}

// AddFolder lists folder in the workspace dir's marker, so it is a member
// though it is not a repository directly below dir: a folder without git,
// or a repository deeper down. It reports false when dir finds it already.
func AddFolder(dir, folder string) (bool, error) {
	rel, err := relMember(dir, folder)
	if err != nil {
		return false, err
	}
	if cd, err := Canonical(dir); err == nil {
		dir = cd
	}
	if slices.Contains(MemberFolders(dir), filepath.Join(dir, filepath.FromSlash(rel))) {
		return false, nil
	}
	m := readMarker(dir)
	m.Folders = append(m.Folders, rel)
	sort.Strings(m.Folders)
	return true, writeMarker(dir, m)
}

// RemoveFolder takes a folder added by hand out of the workspace dir's
// marker. It reports false when it was not listed (a repository directly
// below the workspace is found by itself and cannot be removed).
func RemoveFolder(dir, folder string) (bool, error) {
	rel, err := relMember(dir, folder)
	if err != nil {
		return false, err
	}
	m := readMarker(dir)
	i := slices.Index(m.Folders, rel)
	if i < 0 {
		return false, nil
	}
	m.Folders = slices.Delete(m.Folders, i, i+1)
	return true, writeMarker(dir, m)
}

// MemberFolders are the folders a workspace dir takes as members,
// whatever their binding: its child folders holding a .git directory or
// file (a linked worktree or submodule), read each time so a repository
// cloned in later counts without re-running init, and the folders added by
// hand that still exist.
func MemberFolders(dir string) []string {
	var out []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == store.DirName {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(child, ".git")); err == nil {
			out = append(out, child)
		}
	}
	for _, rel := range readMarker(dir).Folders {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// Members are a workspace folder's members, as absolute paths, sorted: the
// member folders (MemberFolders) that are not bound, and so record into the
// workspace (ADR-0015), or are member Projects linked to it (ADR-0016). A
// folder bound on its own, or linked to another workspace, is not a member.
func Members(dir string) []string {
	ctx := context.Background()
	var out []string
	for _, f := range MemberFolders(dir) {
		if m := MembershipOf(ctx, dir, f); m == Unbound || m == Linked {
			out = append(out, f)
		}
	}
	return out
}

// Membership is how a folder below a workspace stands with it.
type Membership int

const (
	// Unbound is a member without a binding of its own: its agents record
	// into the workspace's store (ADR-0015).
	Unbound Membership = iota
	// Linked is a member Project: its own binding and store, linked to
	// this workspace (ADR-0016).
	Linked
	// Independent is a folder bound on its own and not linked: outside the
	// workspace.
	Independent
	// Elsewhere is a folder bound and linked to another workspace.
	Elsewhere
)

// MembershipOf says how dir stands with the workspace folder ws. It reads
// the marker only: whether a link holds (the same organisation) is
// Discover's call.
func MembershipOf(ctx context.Context, ws, dir string) Membership {
	if !bound(ctx, dir) {
		return Unbound
	}
	target, ok := linkOf(dir)
	switch {
	case !ok:
		return Independent
	case samePath(target, ws):
		return Linked
	}
	return Elsewhere
}

// LinkOf is the workspace folder dir's marker names, canonical when it
// still exists; ok is false when dir is not linked.
func LinkOf(dir string) (string, bool) { return linkOf(dir) }

func linkOf(dir string) (string, bool) {
	rel := readMarker(dir).MemberOf
	if rel == "" {
		return "", false
	}
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if c, err := Canonical(p); err == nil {
		p = c
	}
	return p, true
}

// samePath compares two folders the way the desktop spells them.
func samePath(a, b string) bool {
	if c, err := Canonical(a); err == nil {
		a = c
	}
	if c, err := Canonical(b); err == nil {
		b = c
	}
	return a == b
}

// Join links the folder dir to the workspace folder ws: dir becomes a
// member Project once it is bound. It must lie below ws.
func Join(dir, ws string) error {
	cd, err := Canonical(dir)
	if err != nil {
		return err
	}
	cw, err := Canonical(ws)
	if err != nil {
		return err
	}
	if cd == cw || !within(cd, cw) {
		return ErrNotMember
	}
	m := readMarker(cd)
	if m.Workspace {
		return ErrNested
	}
	rel, err := filepath.Rel(cd, cw)
	if err != nil {
		return err
	}
	m.MemberOf = filepath.ToSlash(rel)
	return writeMarker(cd, m)
}

// Leave takes dir's link out. It stays bound, as a Project on its own.
func Leave(dir string) error {
	m := readMarker(dir)
	if m.MemberOf == "" {
		return nil
	}
	m.MemberOf = ""
	return writeMarker(dir, m)
}

// follow checks a member's link. The workspace folder it names must be
// above the member, carry the workspace marker, be bound, and be in the
// member's organisation when both are in one (a local binding is in none).
// It returns that folder, or why the link does not hold.
func follow(ctx context.Context, dir, rel string, b *store.Binding) (string, string) {
	ws, err := Canonical(filepath.Join(dir, filepath.FromSlash(rel)))
	switch {
	case err != nil:
		return "", "its workspace folder " + rel + " is gone"
	case ws == dir || !within(dir, ws):
		return "", "its workspace link " + rel + " does not point above it"
	case !IsWorkspace(ws):
		return "", ws + " is not a workspace"
	}
	wb := bindingAt(ctx, ws)
	switch {
	case wb == nil:
		return "", ws + " is not bound"
	case !SameOrg(b, wb):
		return "", ws + " belongs to another organisation"
	}
	return ws, ""
}

// SameOrg reports whether two bindings may share a workspace: a local
// binding is in no organisation, so it goes with any.
func SameOrg(a, b *store.Binding) bool {
	return a.OrgID == nil || b.OrgID == nil || *a.OrgID == *b.OrgID
}

// EnclosingWorkspace is the bound workspace folder that takes dir as a
// member folder (a repository directly in it, or a folder added by hand),
// whatever dir's own binding; "" when there is none.
func EnclosingWorkspace(ctx context.Context, dir string) string {
	c, err := Canonical(dir)
	if err != nil {
		return ""
	}
	for d := filepath.Dir(c); ; d = filepath.Dir(d) {
		if IsWorkspace(d) && bound(ctx, d) {
			if slices.Contains(MemberFolders(d), c) {
				return d
			}
			return ""
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

// Targets are the folders that take agent and git hooks for a Project: its
// root, and each member when it is a workspace.
func Targets(root string) []string {
	if !IsWorkspace(root) {
		return []string{root}
	}
	return append([]string{root}, Members(root)...)
}

// InRepo reports whether dir is inside a git work tree or git dir.
func InRepo(ctx context.Context, dir string) bool {
	_, err := gitOut(ctx, dir, "rev-parse", "--git-dir")
	return err == nil
}

// workspaceAbove finds the nearest bound workspace strictly above dir.
func workspaceAbove(ctx context.Context, dir string) (string, bool) {
	for d := filepath.Dir(dir); ; d = filepath.Dir(d) {
		if IsWorkspace(d) && bound(ctx, d) {
			return d, true
		}
		if d == filepath.Dir(d) {
			return "", false
		}
	}
}

// markerAt reports a workspace marker in dir or any ancestor: a cheap stat
// walk that spares the common case a store open.
func markerAt(dir string) bool {
	for d := dir; ; d = filepath.Dir(d) {
		if IsWorkspace(d) {
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}

// MemoryRoot is where shared memory lives for cwd: the workspace folder when
// cwd belongs to a workspace, as an unbound member or a member Project, else
// the repository's scope root (ADR-0007).
func MemoryRoot(ctx context.Context, cwd string) string {
	scope := ScopeRoot(ctx, cwd)
	start, err := Canonical(cwd)
	if err != nil || !markerAt(start) {
		return scope
	}
	if p, err := Discover(ctx, start); err == nil {
		return p.ScopeRoot
	}
	return scope
}

// PlainProjectAbove is the nearest bound folder above dir's scope root that
// is not a workspace, or "": the Project a repository created inside an
// already bound folder would join once that folder is made a workspace.
func PlainProjectAbove(ctx context.Context, dir string) string {
	for d := filepath.Dir(ScopeRoot(ctx, dir)); ; d = filepath.Dir(d) {
		if bound(ctx, d) {
			if IsWorkspace(d) {
				return ""
			}
			return d
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}
