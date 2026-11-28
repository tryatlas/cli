package project

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tryatlas/cli/internal/capture/store"
)

// Tags (ADR-0017). A session is stored once, in the store of the folder its
// agent started in: its home. It is tagged with every other Project of its
// workspace whose files it changed, or whose commits carry its work. Tags
// are derived from the rows the store already has, so sessions.db keeps the
// desktop's schema.

// Reason is why a session is tagged with a Project.
type Reason string

const (
	// Edited means the session changed files in the Project.
	Edited Reason = "edited"
	// Committed means a commit in the Project carries the session's work.
	Committed Reason = "committed"
)

// Tag links a session to a member of its workspace other than its home.
type Tag struct {
	Folder      string   // the member's folder, from the workspace folder, with "/"
	WorkspaceID *string  // the member's cloud Workspace; nil while it has none
	Reasons     []Reason // edited, then committed
}

// Layout is a workspace as one of its stores sees it, for tags.
type Layout struct {
	Root    string // the workspace folder
	Home    string // the store's root: Root, or a member Project's folder
	members []layoutMember
}

type layoutMember struct {
	dir, folder string
	id          *string
}

// LayoutOf is the layout for the store rooted at root: nil when root is
// neither a workspace nor a member Project whose link holds, so a plain
// Project's sessions carry no tags. A member's cloud Workspace counts only
// in the home store's organisation.
func LayoutOf(ctx context.Context, root string) *Layout {
	home := bindingAt(ctx, root)
	if home == nil {
		return nil
	}
	ws := root
	if m := readMarker(root); !m.Workspace {
		if m.MemberOf == "" {
			return nil
		}
		var why string
		if ws, why = follow(ctx, root, m.MemberOf, home); why != "" {
			return nil
		}
	}
	l := &Layout{Root: ws, Home: root}
	for _, f := range MemberFolders(ws) {
		folder, err := filepath.Rel(ws, f)
		if err != nil {
			continue
		}
		m := layoutMember{dir: f, folder: filepath.ToSlash(folder)}
		switch MembershipOf(ctx, ws, f) {
		case Linked:
			if b := bindingAt(ctx, f); b != nil && b.Mode == store.ModeCloud && b.OrgID != nil &&
				home.OrgID != nil && *b.OrgID == *home.OrgID && b.RemoteWorkspaceID != nil && *b.RemoteWorkspaceID != "" {
				m.id = b.RemoteWorkspaceID
			}
		case Unbound:
		default:
			continue // bound on its own, or in another workspace
		}
		l.members = append(l.members, m)
	}
	// The deepest folder first: a folder added by hand inside another
	// member wins its paths.
	sort.Slice(l.members, func(i, j int) bool { return len(l.members[i].dir) > len(l.members[j].dir) })
	return l
}

// memberAt is the member holding abs.
func (l *Layout) memberAt(abs string) (layoutMember, bool) {
	for _, m := range l.members {
		if abs == m.dir || strings.HasPrefix(abs, m.dir+string(filepath.Separator)) {
			return m, true
		}
	}
	return layoutMember{}, false
}

// abs spells a recorded path absolute: one in the store's tree from its
// root, an out-of-repo one canonical where it still exists.
func (l *Layout) abs(p string, outOfRepo bool) string {
	if !outOfRepo && !filepath.IsAbs(p) {
		return filepath.Join(l.Home, filepath.FromSlash(p))
	}
	return canonicalAsFar(p)
}

// canonicalAsFar spells p canonical through its deepest folder that still
// exists: a deleted file, or a file in a deleted folder, keeps the spelling
// of the tree it was in (/private/var/… on macOS, where /var is a link).
func canonicalAsFar(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for d := p; ; d = filepath.Dir(d) {
		if c, err := Canonical(d); err == nil {
			return filepath.Join(c, rest)
		}
		if d == filepath.Dir(d) {
			return p
		}
		rest = filepath.Join(filepath.Base(d), rest)
	}
}

// Tags derives a session's tags from the paths it changed and the files of
// the commits linked to it, both as its store recorded them. A path in the
// home Project, in the workspace folder itself, or outside the workspace
// adds no tag.
func (l *Layout) Tags(changed []store.TouchedPath, committed []string) []Tag {
	reasons := map[string]map[Reason]bool{}
	ids := map[string]*string{}
	add := func(abs string, r Reason) {
		m, ok := l.memberAt(abs)
		if !ok || m.dir == l.Home {
			return
		}
		if reasons[m.folder] == nil {
			reasons[m.folder] = map[Reason]bool{}
		}
		reasons[m.folder][r] = true
		ids[m.folder] = m.id
	}
	for _, t := range changed {
		add(l.abs(t.Path, t.OutOfRepo), Edited)
	}
	for _, p := range committed {
		add(l.abs(p, false), Committed)
	}
	out := make([]Tag, 0, len(reasons))
	for folder, rs := range reasons {
		t := Tag{Folder: folder, WorkspaceID: ids[folder]}
		for _, r := range []Reason{Edited, Committed} {
			if rs[r] {
				t.Reasons = append(t.Reasons, r)
			}
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out
}
