package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/tryatlas/cli/internal/auth"
	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/hosts"
	"github.com/tryatlas/cli/internal/ingest"
	"github.com/tryatlas/cli/internal/project"
	"github.com/tryatlas/cli/internal/tui"
)

// Member Projects (ADR-0016): a workspace's repositories bound one by one.
// Each gets its own store, local or in the workspace's organisation as the
// workspace is, and a link to the workspace in its marker. Its sessions
// record there; its memory stays the workspace's.

// memberBinder binds folders as member Projects of one workspace.
type memberBinder struct {
	ws  string         // the workspace folder
	wsb *store.Binding // its binding
	ic  *ingest.Client // nil when the workspace is local
	org auth.Org       // the workspace's organisation, when it is in the cloud
	vis string         // who sees a new cloud Project: org or restricted
	// group is the workspace's cloud group (ADR-0018); nil when local.
	group *cloudGroup
}

// bindingOf reads dir's binding row; nil when dir is not bound.
func bindingOf(ctx context.Context, dir string) (*store.Binding, error) {
	if !store.Exists(dir) {
		return nil, nil
	}
	st, err := store.Open(dir, false)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.Binding(ctx)
}

// newMemberBinder readies binding members of the workspace ws. A cloud
// workspace needs a sign-in to its organisation.
func newMemberBinder(ctx context.Context, ws, vis string) (*memberBinder, error) {
	wsb, err := bindingOf(ctx, ws)
	if err != nil {
		return nil, err
	}
	if wsb == nil {
		return nil, project.ErrNotInitialised
	}
	mb := &memberBinder{ws: ws, wsb: wsb, vis: vis}
	if wsb.Mode != store.ModeCloud || wsb.OrgID == nil {
		return mb, nil
	}
	a, err := newAuth()
	if err != nil {
		return nil, err
	}
	sess, err := a.Refresh(ctx)
	if errors.Is(err, auth.ErrSignedOut) {
		return nil, errors.New("the workspace syncs to the cloud: sign in (atlas-cli login) to bind its repositories as Projects")
	}
	if err != nil {
		return nil, err
	}
	mb.org = auth.Org{ID: *wsb.OrgID, Name: *wsb.OrgID}
	if sess.Identity != nil && sess.Identity.Orgs != nil {
		found := false
		for _, o := range *sess.Identity.Orgs {
			if o.ID == *wsb.OrgID {
				mb.org, found = o, true
			}
		}
		if !found {
			return nil, fmt.Errorf("the workspace is in the organisation %s, which you are not in (atlas-cli org list)", *wsb.OrgID)
		}
	}
	mb.ic = ingest.New(hosts.Ingest(), a.Tokens())
	mb.group = openGroup(ctx, mb.ic, mb.org.ID, deref(wsb.RemoteWorkspaceID))
	mb.group.adopt = askAdopt(false)
	return mb, nil
}

// placed adds how dir's cloud Project stands in the workspace's group.
func (mb *memberBinder) placed(ctx context.Context, dir, how string) string {
	if mb.group == nil {
		return how
	}
	b, err := bindingOf(ctx, dir)
	if err != nil || b == nil || b.Mode != store.ModeCloud {
		return how
	}
	if where := mb.group.place(ctx, filepath.Base(dir), deref(b.RemoteWorkspaceID)); where != "" {
		return how + "; " + where
	}
	return how
}

// bind makes dir a member Project and says how. A folder that is not bound
// gets its own store; one bound on its own joins as it is, when it is in the
// workspace's organisation.
func (mb *memberBinder) bind(ctx context.Context, dir string) (string, error) {
	switch project.MembershipOf(ctx, mb.ws, dir) {
	case project.Linked:
		return mb.placed(ctx, dir, "a Project in the workspace already"), nil
	case project.Elsewhere:
		other, _ := project.LinkOf(dir)
		return "", fmt.Errorf("it is a Project in the workspace %s", other)
	case project.Independent:
		b, err := bindingOf(ctx, dir)
		if err != nil {
			return "", err
		}
		if b == nil || !project.SameOrg(b, mb.wsb) {
			return "", errors.New("it is bound to another organisation than the workspace")
		}
		if err := project.Join(dir, mb.ws); err != nil {
			return "", err
		}
		return mb.placed(ctx, dir, "joined as it was bound: "+describeBinding(b, nil)), nil
	}
	how, err := mb.bindNew(ctx, dir)
	if err != nil || mb.group == nil {
		return how, err
	}
	return mb.placed(ctx, dir, how), nil
}

// bindNew writes dir's binding under the writer lock, then the link: a link
// without a binding means nothing.
func (mb *memberBinder) bindNew(ctx context.Context, dir string) (string, error) {
	lock, err := store.Lock(dir)
	if errors.Is(err, store.ErrLocked) {
		return "", errors.New(lockedHelp)
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Release() }()
	st, err := store.Open(dir, true)
	if err != nil {
		return "", err
	}
	defer st.Close()

	fp := project.Fingerprint(ctx, dir)
	b := store.Binding{
		WorkspaceID: dir, Root: dir, Enabled: true, DrainState: store.DrainOK,
		RootCommitSHA: fp.RootCommitSHA, FingerprintIsShallow: fp.Shallow, GitURL: fp.GitURL,
	}
	how := "local, nothing is synced"
	if mb.ic == nil {
		b.Mode, b.ImportApproved = store.ModeLocal, true
		if err := st.PutBinding(ctx, b, now()); err != nil {
			return "", err
		}
	} else {
		ws, match, err := mb.cloudProject(ctx, dir, fp)
		if err != nil {
			return "", err
		}
		b.OrgID, b.RemoteWorkspaceID = &mb.org.ID, &ws.ID
		if ws.Slug != "" {
			b.Slug = &ws.Slug
		}
		b.ImportApproved = false // as for any cloud Project (bindFolder)
		if err := st.BindCloud(ctx, b, now()); err != nil {
			return "", err
		}
		how = fmt.Sprintf("%s / %s, %s", mb.org.Name, ws.Label(), match)
	}
	if err := project.Join(dir, mb.ws); err != nil {
		return "", err
	}
	return how, nil
}

// cloudProject finds a member's cloud Project: the server's match by git
// fingerprint, else a new one named after the folder (or after the
// workspace and the folder, when that handle is taken). Several matches are
// a choice on a terminal and an error without one.
func (mb *memberBinder) cloudProject(ctx context.Context, dir string, fp project.GitFingerprint) (ingest.Workspace, string, error) {
	name := filepath.Base(dir)
	if fp.RootCommitSHA != nil || fp.GitURL != nil {
		res, err := mb.ic.Connect(ctx, ingest.ConnectRequest{OrgID: mb.org.ID, RootCommitSHA: fp.RootCommitSHA, GitURL: fp.GitURL})
		if err != nil {
			return ingest.Workspace{}, "", err
		}
		switch res.Status {
		case "connected":
			return ingest.Workspace{ID: res.WorkspaceID, Slug: deref(res.Slug)}, "matched by " + res.Reason, nil
		case "ambiguous":
			if !interactive() {
				return ingest.Workspace{}, "", fmt.Errorf("several Projects in %s match it (%s); atlas-cli init --project <id> in %s picks one",
					mb.org.Name, workspaceList(res.Candidates), dir)
			}
			i, err := chooseStep(tui.Step{Title: name}, "Several Projects in "+mb.org.Name+" match "+name+". Which one?", workspaceChoices(res.Candidates), 0)
			if err != nil {
				return ingest.Workspace{}, "", err
			}
			c := res.Candidates[i]
			got, err := mb.ic.Connect(ctx, ingest.ConnectRequest{OrgID: mb.org.ID, WorkspaceID: &c.ID, RootCommitSHA: fp.RootCommitSHA, GitURL: fp.GitURL})
			if err != nil {
				return ingest.Workspace{}, "", err
			}
			if got.Status != "connected" {
				return ingest.Workspace{}, "", fmt.Errorf("the server did not connect Project %s (status %q)", c.ID, got.Status)
			}
			ws := ingest.Workspace{ID: got.WorkspaceID, Slug: deref(got.Slug)}
			if ws.Slug == "" {
				ws.Slug = c.Slug
			}
			return ws, "connected: " + got.Reason, nil
		}
	}
	var tried []string
	for _, slug := range []string{slugify(name), slugify(filepath.Base(mb.ws) + "-" + name)} {
		if slug == "" {
			continue
		}
		tried = append(tried, slug)
		free, err := mb.ic.SlugAvailable(ctx, mb.org.ID, slug)
		if err != nil {
			return ingest.Workspace{}, "", err
		}
		if !free {
			continue
		}
		req := ingest.CreateRequest{OrgID: mb.org.ID, Slug: slug, Visibility: mb.vis, Name: &name,
			RootCommitSHA: fp.RootCommitSHA, GitURL: fp.GitURL, ParentID: mb.group.parent()}
		id, err := mb.ic.Create(ctx, req)
		if ingest.CodeOf(err) == "group_full" { // made on its own; place says so
			req.ParentID = nil
			id, err = mb.ic.Create(ctx, req)
		}
		if errors.Is(err, ingest.ErrSlugTaken) {
			continue
		}
		if err != nil {
			return ingest.Workspace{}, "", err
		}
		if req.ParentID != nil {
			mb.group.created(id)
		}
		return ingest.Workspace{ID: id, Slug: slug, Name: &name}, "created", nil
	}
	return ingest.Workspace{}, "", fmt.Errorf("no Project in %s matches it and the handles %s are taken; atlas-cli init --create --slug <handle> in %s",
		mb.org.Name, strings.Join(tried, " and "), dir)
}

// bindMembers binds each folder as a member Project, one line each. A folder
// that cannot be bound is named with why and stays as it was; the others go
// on, and the failures come back joined.
func bindMembers(ctx context.Context, out io.Writer, mb *memberBinder, folders []string) error {
	var errs []error
	if mb.group != nil {
		for _, n := range mb.group.notes {
			fmt.Fprintf(out, "Cloud: %s.\n", n)
		}
		mb.group.notes = nil
		defer func() {
			n, err := resendTags(ctx, mb.group, mb.ws)
			if n > 0 {
				fmt.Fprintf(out, "Cloud: %s again, so each shows on the Projects it changed.\n", plural(int(n), "earlier session goes", "earlier sessions go"))
			}
			if err != nil {
				fmt.Fprintf(out, "Cloud: %v.\n", err)
			}
		}()
	}
	for _, f := range folders {
		name := memberNames(mb.ws, []string{f})
		how, err := mb.bind(ctx, f)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		fmt.Fprintf(out, "%s: Project in the workspace (%s).\n", name, how)
	}
	return errors.Join(errs...)
}

// unboundMembers are the workspace's members that record into it rather
// than into a Project of their own.
func unboundMembers(ctx context.Context, ws string) []string {
	var out []string
	for _, m := range project.Members(ws) {
		if project.MembershipOf(ctx, ws, m) == project.Unbound {
			out = append(out, m)
		}
	}
	return out
}

// memberProjects names the workspace's member Projects, comma-separated.
func memberProjects(ctx context.Context, ws string) (names string, n int) {
	var linked []string
	for _, m := range project.Members(ws) {
		if project.MembershipOf(ctx, ws, m) == project.Linked {
			linked = append(linked, m)
		}
	}
	return memberNames(ws, linked), len(linked)
}
