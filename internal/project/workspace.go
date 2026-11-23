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
// repository. Agents started in any of them record into the mother folder's
// store and share its memory. The marker is a sidecar file, not a binding
// column, so sessions.db stays the desktop's schema.

const workspaceFile = "workspace.json"

// marker is .atlas/workspace.json.
type marker struct {
	Workspace bool `json:"workspace"`
	// Folders are members named by hand (atlas-cli workspace add): folders
	// without git, or repositories deeper than one level, relative to the
	// workspace folder.
	Folders []string `json:"folders,omitempty"`
}

func readMarker(dir string) marker {
	var m marker
	b, err := os.ReadFile(filepath.Join(dir, store.DirName, workspaceFile)) //nolint:gosec // G304: the Project's own marker
	if err != nil || json.Unmarshal(b, &m) != nil {
		return marker{}
	}
	return m
}

func writeMarker(dir string, m marker) error {
	path := filepath.Join(dir, store.DirName, workspaceFile)
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

// SetWorkspace writes or removes the marker in dir/.atlas/. Turning it on
// again keeps the folders added by hand.
func SetWorkspace(dir string, on bool) error {
	if !on {
		err := os.Remove(filepath.Join(dir, store.DirName, workspaceFile))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	m := readMarker(dir)
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
	if fi, err := os.Stat(c); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", folder)
	}
	rel, err := filepath.Rel(dir, c)
	if err != nil || rel == "." || !within(c, dir) || strings.HasPrefix(rel, store.DirName) {
		return "", ErrNotMember
	}
	return filepath.ToSlash(rel), nil
}

// AddFolder names folder a member of the workspace dir by hand. It reports
// false when it already was one.
func AddFolder(dir, folder string) (bool, error) {
	rel, err := relMember(dir, folder)
	if err != nil {
		return false, err
	}
	if slices.Contains(Members(dir), filepath.Join(dir, filepath.FromSlash(rel))) {
		return false, nil
	}
	m := readMarker(dir)
	m.Folders = append(m.Folders, rel)
	sort.Strings(m.Folders)
	return true, writeMarker(dir, m)
}

// RemoveFolder takes a folder added by hand out of the workspace dir. It
// reports false when it was not listed (a repository directly below the
// workspace is a member by itself and cannot be removed).
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

// Members are a workspace folder's members, as absolute paths, sorted: its
// child folders holding a .git directory or file (a linked worktree or
// submodule) and no binding of their own, read each time so a repository
// cloned in later is a member without re-running init, and the folders added
// by hand that still exist.
func Members(dir string) []string {
	var out []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == store.DirName {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(child, ".git")); err == nil && !bound(context.Background(), child) {
			out = append(out, child) // one with its own binding is split off: its own Project
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

// Targets are the folders that take agent and git hooks for a Project: its
// root, and each member repository when it is a workspace.
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
// cwd belongs to a workspace, else the repository's scope root (ADR-0007).
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
