package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/clistate"
	"github.com/tryatlas/cli/internal/hosts"
	"github.com/tryatlas/cli/internal/ingest"
	"github.com/tryatlas/cli/internal/project"
	"github.com/tryatlas/cli/internal/tui"
)

// Workspace groups in the cloud (ADR-0018, server ADR-0032). Where the
// organisation may group Projects, the workspace folder's cloud Project is a
// group and each member Project names it as its parent; elsewhere nothing
// here changes what the CLI sends. Nothing here fails a bind: what could not
// be done in the cloud is a note, and the local binding stands.

// cloudGroup is a cloud workspace's group, as one command sees it.
type cloudGroup struct {
	ic    *ingest.Client
	org   string
	id    string // the workspace folder's cloud Project
	on    bool   // the organisation has groups and id is one
	reg   ingest.Registry
	notes []string
	// adopt asks whether a member Project matched in the cloud, in no group
	// yet, goes into this one. Nil means no.
	adopt func(name string) bool
}

// noteOf is an answer from the server, for a note.
func noteOf(err error) string {
	if ingest.StatusOf(err) == http.StatusForbidden {
		return "only its owner or an org admin may change it"
	}
	return err.Error()
}

// openGroup reads the organisation's Projects and makes the workspace's
// cloud Project a group when the organisation may have groups: registered
// by name before groups existed, it is promoted.
func openGroup(ctx context.Context, ic *ingest.Client, org, id string) *cloudGroup {
	g := &cloudGroup{ic: ic, org: org, id: id}
	if ic == nil || id == "" {
		return g
	}
	reg, err := ic.Registry(ctx, org)
	if err != nil {
		g.note("could not read the organisation's Projects (%v); the workspace's Projects stay on their own in the cloud for now", err)
		return g
	}
	g.reg = reg
	if !reg.Groups {
		return g // as before groups: nothing to say
	}
	w, ok := reg.Find(id)
	switch {
	case !ok:
		g.note("the workspace's cloud Project %s is not in the organisation's list; its Projects stay on their own in the cloud", id)
		return g
	case w.IsGroup():
		g.on = true
		return g
	case w.Parent() != "":
		g.note("the workspace's cloud Project %s is in a group itself, so it cannot be one; its Projects stay on their own in the cloud", w.Label())
		return g
	}
	kind := "group"
	got, err := ic.Update(ctx, org, id, ingest.UpdateRequest{Kind: &kind})
	switch {
	case ingest.CodeOf(err) == "not_promotable":
		g.note("the workspace's cloud Project %s holds a repository or syncs code, so it cannot hold Projects; they stay on their own in the cloud", w.Label())
		return g
	case err != nil:
		g.note("the workspace's cloud Project %s could not become a workspace in the cloud: %s", w.Label(), noteOf(err))
		return g
	case !got.IsGroup(): // a server that does not have them after all
		return g
	}
	g.on = true
	g.put(got)
	g.note("the workspace's cloud Project %s now holds its Projects", w.Label())
	return g
}

func (g *cloudGroup) note(format string, args ...any) {
	g.notes = append(g.notes, fmt.Sprintf(format, args...))
}

// put records w in the registry as the server now has it.
func (g *cloudGroup) put(w ingest.Workspace) {
	for i := range g.reg.Workspaces {
		if g.reg.Workspaces[i].ID == w.ID {
			if w.Slug == "" {
				w.Slug = g.reg.Workspaces[i].Slug
			}
			g.reg.Workspaces[i] = w
			return
		}
	}
	g.reg.Workspaces = append(g.reg.Workspaces, w)
}

// parent is what a new member Project is created with: the group, when
// there is one.
func (g *cloudGroup) parent() *string {
	if g == nil || !g.on {
		return nil
	}
	return &g.id
}

// created records a member Project created with parent().
func (g *cloudGroup) created(id string) {
	if g != nil && g.on {
		g.put(ingest.Workspace{ID: id, ParentID: &g.id})
	}
}

// place puts the member Project id (named name) in the group and says how
// it stands, "" when there is nothing to say. A Project in another group is
// left there; one in none goes in when adopt says so.
func (g *cloudGroup) place(ctx context.Context, name, id string) string {
	if g == nil || !g.on || id == "" {
		return ""
	}
	w, ok := g.reg.Find(id)
	if !ok { // made since the list was read
		if reg, err := g.ic.Registry(ctx, g.org); err == nil {
			g.reg = reg
			w, ok = reg.Find(id)
		}
	}
	switch {
	case !ok:
		return ""
	case w.Parent() == g.id:
		return "in the workspace's group"
	case w.Parent() != "":
		return "in another group in the cloud, left there"
	case g.adopt == nil || !g.adopt(name):
		return "on its own in the cloud (atlas-cli workspace upgrade puts it in the workspace's group)"
	}
	got, err := g.ic.Update(ctx, g.org, id, ingest.UpdateRequest{ParentID: &g.id})
	switch {
	case ingest.CodeOf(err) == "group_full":
		return "on its own in the cloud: the workspace's group holds as many Projects as it can"
	case err != nil:
		return "on its own in the cloud: " + noteOf(err)
	}
	if got.ID == "" {
		got = w
		got.ParentID = &g.id
	}
	g.put(got)
	return "put in the workspace's group"
}

// members are the group's members in the registry, sorted.
func (g *cloudGroup) members() []string {
	var ids []string
	for _, w := range g.reg.Workspaces {
		if w.Parent() == g.id {
			ids = append(ids, w.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// askAdopt is the default adopt: on a terminal it asks; without one, yes
// decides.
func askAdopt(yes bool) func(string) bool {
	return func(name string) bool {
		if yes {
			return true
		}
		if !interactive() {
			return false
		}
		i, err := chooseStep(tui.Step{Title: name}, name+"'s cloud Project is in no group. Put it in the workspace's?", []tui.Choice{
			{Label: "Yes", Detail: "its timeline shows in the workspace's, and sessions that changed it show on its own"},
			{Label: "No", Detail: "it stays on its own in the cloud"},
		}, 0)
		return err == nil && i == 0
	}
}

// tagsMarker names the file that says which group a store's sessions were
// last sent for. It lives in the CLI's state, not in .atlas/.
const tagsMarker = "tags-sent"

// resendTags sends every session of the workspace and its member Projects
// again, once for each makeup of the group: the server keeps only the tags
// that name a Workspace of the home's group, so sessions sent before the
// group was made, or before a Project joined it, lost theirs (ADR-0018).
// It answers how many sessions will go.
func resendTags(ctx context.Context, g *cloudGroup, ws string) (int64, error) {
	if g == nil || !g.on {
		return 0, nil
	}
	sig := g.id + ":" + strings.Join(g.members(), ",")
	var total int64
	var errs []error
	for _, dir := range append([]string{ws}, linkedMembers(ctx, ws)...) {
		path := clistate.Path(dir, tagsMarker)
		if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == sig {
			continue
		}
		st, err := store.Open(dir, false)
		if err != nil {
			continue // no store: nothing was sent from it
		}
		n, err := st.ResyncSessions(ctx)
		st.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		total += n
		_ = os.WriteFile(path, []byte(sig+"\n"), 0o600)
	}
	if len(errs) > 0 {
		return total, fmt.Errorf("sending sessions again for their tags: %v", errs)
	}
	return total, nil
}

// linkedMembers are the workspace's member Projects.
func linkedMembers(ctx context.Context, ws string) []string {
	var out []string
	for _, m := range project.Members(ws) {
		if project.MembershipOf(ctx, ws, m) == project.Linked {
			out = append(out, m)
		}
	}
	return out
}

// plural is "1 thing" or "n things".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// leaveGroup takes the cloud Projects of dirs, Projects leaving the
// workspace ws, out of its cloud group, so the cloud says what the folders
// do. Only a Project in that group moves; what could not be done is said.
func leaveGroup(ctx context.Context, out io.Writer, ws string, dirs []string) {
	wsb, err := bindingOf(ctx, ws)
	if err != nil || wsb == nil || wsb.Mode != store.ModeCloud || wsb.OrgID == nil || wsb.RemoteWorkspaceID == nil {
		return
	}
	type member struct{ name, id string }
	var ms []member
	for _, d := range dirs {
		if b, err := bindingOf(ctx, d); err == nil && b != nil && b.Mode == store.ModeCloud && b.RemoteWorkspaceID != nil {
			ms = append(ms, member{filepath.Base(d), *b.RemoteWorkspaceID})
		}
	}
	if len(ms) == 0 {
		return
	}
	later := "it stays in the workspace's group in the cloud until it is moved out on the web"
	a, err := newAuth()
	if err != nil {
		fmt.Fprintf(out, "Cloud: %v; %s.\n", err, later)
		return
	}
	if _, err := a.Refresh(ctx); err != nil {
		fmt.Fprintf(out, "Cloud: not signed in; %s.\n", later)
		return
	}
	ic := ingest.New(hosts.Ingest(), a.Tokens())
	reg, err := ic.Registry(ctx, *wsb.OrgID)
	if err != nil || !reg.Groups {
		return
	}
	for _, m := range ms {
		w, ok := reg.Find(m.id)
		if !ok || w.Parent() != *wsb.RemoteWorkspaceID {
			continue
		}
		if _, err := ic.Update(ctx, *wsb.OrgID, m.id, ingest.UpdateRequest{Ungroup: true}); err != nil {
			fmt.Fprintf(out, "Cloud: %s: %s; %s.\n", m.name, noteOf(err), later)
			continue
		}
		fmt.Fprintf(out, "Cloud: %s is out of the workspace's group.\n", m.name)
	}
}
