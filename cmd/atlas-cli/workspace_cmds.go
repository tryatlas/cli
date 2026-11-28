package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/hooks"
	"github.com/tryatlas/cli/internal/hooks/mcpconfig"
	"github.com/tryatlas/cli/internal/project"
)

// atlas-cli workspace: a folder of repositories bound once (ADR-0015), each
// repository a Project of its own once it is added (ADR-0016).

func newWorkspaceCmd() *cobra.Command {
	add := withRun(leaf("add <folder>...", "Make folders Projects in the workspace: its repositories, a folder without git, a repository deeper down", cobra.ArbitraryArgs), runWorkspaceAdd)
	add.Flags().Bool("all", false, "every repository in the workspace that is not a Project yet")
	add.Flags().String("visibility", "org", "who sees a new cloud Project: org or restricted")
	add.Flags().Bool("yes", false, "put Projects matched in the cloud in the workspace's group without asking")
	c := group("workspace", "A folder of repositories bound once: show its Projects, add and remove them, hook new ones",
		withRun(leaf("on", "Make this Project a workspace: agents started in its repositories record here", cobra.NoArgs), runWorkspaceOn),
		withRun(leaf("off", "Make this Project a single folder again: its Projects go on their own, its other repositories are unhooked", cobra.NoArgs), runWorkspaceOff),
		withRun(leaf("sync", "Hook folders added since: every member gets the agents hooked in the workspace folder", cobra.NoArgs), runWorkspaceSync),
		add,
		withRun(leaf("remove <folder>", "Take a folder out of the workspace: a Project in it goes on its own, a folder added by hand is unhooked", cobra.ExactArgs(1)), runWorkspaceRemove),
		newWorkspaceUpgradeCmd(),
	)
	c.RunE = runWorkspaceShow
	return c
}

var errNotWorkspace = errors.New("this Project is not a workspace; atlas-cli workspace on (in the folder that holds the repositories) makes it one")

// workspaceHere is the workspace around the cwd: the one the cwd records
// into, or the one its member Project is linked to.
func workspaceHere(ctx context.Context) (*project.Project, error) {
	p, err := agentRoot(ctx)
	if err != nil {
		return nil, err
	}
	switch p.Kind {
	case project.KindWorkspace:
		return p, nil
	case project.KindMember:
		return project.Discover(ctx, p.WorkspaceRoot)
	}
	return nil, errNotWorkspace
}

// hookedAgents are the agents hooked in root.
func hookedAgents(root string) []hooks.Agent {
	var out []hooks.Agent
	for _, a := range hooks.All() {
		if a.State(root) != hooks.Absent {
			out = append(out, a)
		}
	}
	return out
}

// syncWorkspace gives every member the agents hooked in the workspace
// folder, each with a memory entry when the workspace folder has one.
func syncWorkspace(ctx context.Context, out io.Writer, root string) error {
	agents := hookedAgents(root)
	if len(agents) == 0 {
		fmt.Fprintln(out, "No agent is hooked in the workspace folder yet: atlas-cli agents add <agent> hooks it and every member.")
		return nil
	}
	for _, a := range agents {
		noMem := false
		if mw, ok := mcpconfig.For(a.ID()); ok {
			noMem = mw.State(root) == hooks.Absent
		}
		if err := installAgents(ctx, out, root, []hooks.Agent{a}, false, noMem); err != nil {
			return err
		}
	}
	return nil
}

func isRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// runWorkspaceShow draws the workspace as a tree: its folder, its members
// (a Project of its own, or a folder that records here) and the agents
// hooked in each, then the repositories bound on their own beside it.
func runWorkspaceShow(cmd *cobra.Command, _ []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := agentRoot(ctx)
	if err != nil {
		return err
	}
	if p.Kind == project.KindPlain {
		fmt.Fprintf(out, "%s is a single-folder Project, not a workspace (atlas-cli workspace on).\n", p.Root)
		if p.Broken != "" {
			fmt.Fprintf(out, "Its link to a workspace does not hold: %s.\n", p.Broken)
		}
		return nil
	}
	ws := p.WorkspaceRoot
	agents := hookedAgents(ws)
	b, _ := bindingOf(ctx, ws)
	bound := "not bound"
	if b != nil {
		bound = describeBinding(b, nil)
	}
	fmt.Fprintf(out, "Workspace: %s (%s)\n", ws, bound)

	type row struct{ dir, kind string }
	rows := []row{{ws, "workspace folder"}}
	var beside []row
	records := 0
	for _, f := range project.MemberFolders(ws) {
		switch project.MembershipOf(ctx, ws, f) {
		case project.Linked:
			rows = append(rows, row{f, "Project"})
		case project.Unbound:
			kind := "folder, records here"
			if isRepo(f) {
				kind = "repository, records here"
			}
			rows = append(rows, row{f, kind})
			records++
		case project.Independent:
			beside = append(beside, row{f, "Project on its own"})
		case project.Elsewhere:
			other, _ := project.LinkOf(f)
			beside = append(beside, row{f, "Project in " + other})
		}
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "FOLDER\tKIND\tCLOUD\tAGENTS")
	missing := false
	for i, r := range rows {
		name := "."
		if r.dir != ws {
			glyph := "├─ "
			if i == len(rows)-1 {
				glyph = "└─ "
			}
			rel, _ := filepath.Rel(ws, r.dir)
			name = glyph + rel
		}
		var on, off []string
		for _, a := range agents {
			if a.State(r.dir) == hooks.Current {
				on = append(on, a.ID())
			} else {
				off = append(off, a.ID())
			}
		}
		state := strings.Join(on, ", ")
		if len(off) > 0 {
			missing = true
			state = strings.TrimPrefix(state+" · not hooked: "+strings.Join(off, ", "), " · ")
		}
		if state == "" {
			state = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, r.kind, cloudOf(ctx, r.dir), state)
	}
	for _, r := range beside {
		rel, _ := filepath.Rel(ws, r.dir)
		fmt.Fprintf(w, "   %s\t%s\t%s\tnot in the workspace (atlas-cli workspace add %s)\n", rel, r.kind, cloudOf(ctx, r.dir), rel)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	switch {
	case len(agents) == 0:
		fmt.Fprintln(out, "No agent is hooked yet: atlas-cli agents add <agent>.")
	case missing:
		fmt.Fprintln(out, "Some folders are not hooked: atlas-cli workspace sync.")
	}
	if records > 0 {
		fmt.Fprintln(out, "A folder that records here is not a Project yet: atlas-cli workspace add --all makes each one a Project of its own.")
	}
	fmt.Fprintf(out, "Memory: one for the whole workspace, in %s.\n", filepath.Join(ws, ".atlas", "memory"))
	return nil
}

func runWorkspaceOn(cmd *cobra.Command, _ []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := agentRoot(ctx)
	if err != nil {
		return err
	}
	switch p.Kind {
	case project.KindWorkspace:
		fmt.Fprintf(out, "%s is already a workspace.\n", p.Root)
		return syncWorkspace(ctx, out, p.Root)
	case project.KindMember:
		return fmt.Errorf("%s is a Project in the workspace %s: %w", p.Root, p.WorkspaceRoot, project.ErrNested)
	}
	if p.Cwd != p.Root {
		return fmt.Errorf("run this in the Project folder, %s", p.Root)
	}
	if project.InRepo(ctx, p.Root) {
		return fmt.Errorf("%s is inside a git repository; a workspace is the folder that holds the repositories", p.Root)
	}
	if err := project.SetWorkspace(p.Root, true); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is now a workspace (%s).\n", p.Root, workspaceRepos(p.Root))
	if un := unboundMembers(ctx, p.Root); len(un) > 0 {
		markKnown(p.Root, un)
		fmt.Fprintln(out, "Its repositories record here; atlas-cli workspace add --all makes each one a Project of its own.")
	}
	return syncWorkspace(ctx, out, p.Root)
}

// runWorkspaceOff makes the workspace a single folder again. A member
// Project goes on as a Project on its own, with its store and hooks; a
// member that recorded here is unhooked.
func runWorkspaceOff(cmd *cobra.Command, _ []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := workspaceHere(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range unboundMembers(ctx, p.Root) {
		errs = append(errs, unhookFolder(ctx, out, m)...)
	}
	leaveGroup(ctx, out, p.Root, linkedMembers(ctx, p.Root))
	left, relErr := releaseMembers(ctx, p.Root)
	if err := errors.Join(append(errs, relErr)...); err != nil {
		return err
	}
	if err := project.SetWorkspace(p.Root, false); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is a single-folder Project again. What was recorded stays.\n", p.Root)
	if len(left) > 0 {
		fmt.Fprintf(out, "%s go on as Projects of their own, each with its own memory.\n", memberNames(p.Root, left))
	}
	return nil
}

// releaseMembers unlinks the workspace's member Projects and returns them:
// each goes on as a Project on its own, with its store and hooks.
func releaseMembers(ctx context.Context, ws string) ([]string, error) {
	var left []string
	var errs []error
	for _, m := range project.Members(ws) {
		if project.MembershipOf(ctx, ws, m) != project.Linked {
			continue
		}
		if err := project.Leave(m); err != nil {
			errs = append(errs, err)
			continue
		}
		left = append(left, m)
	}
	return left, errors.Join(errs...)
}

func runWorkspaceSync(cmd *cobra.Command, _ []string) error {
	p, err := workspaceHere(cmd.Context())
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Workspace %s: %s.\n", p.Root, workspaceRepos(p.Root))
	if err := offerNewClones(cmd.Context(), cmd.OutOrStdout(), p.Root); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "New repositories not bound: %v\n", err)
	}
	return syncWorkspace(cmd.Context(), cmd.OutOrStdout(), p.Root)
}

// runWorkspaceAdd makes folders member Projects (ADR-0016): each gets its
// own store, local or in the workspace's organisation as the workspace is,
// and the workspace's agents. A folder that is not a repository directly in
// the workspace is listed in its marker first.
func runWorkspaceAdd(cmd *cobra.Command, args []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := workspaceHere(ctx)
	if err != nil {
		return err
	}
	all, _ := cmd.Flags().GetBool("all")
	switch {
	case all && len(args) > 0:
		return errors.New("name folders or pass --all, not both")
	case !all && len(args) == 0:
		return errors.New("name a folder to add, or pass --all for every repository that is not a Project yet")
	}
	var folders []string
	if all {
		if folders = unboundMembers(ctx, p.Root); len(folders) == 0 {
			fmt.Fprintln(out, "Every repository in the workspace is a Project already.")
			return nil
		}
	}
	for _, a := range args {
		if _, err := project.AddFolder(p.Root, a); err != nil {
			return fmt.Errorf("%s: %w", a, err)
		}
		c, err := project.Canonical(a)
		if err != nil {
			return err
		}
		folders = append(folders, c)
	}
	vis, _ := cmd.Flags().GetString("visibility")
	if vis != "org" && vis != "restricted" {
		return fmt.Errorf("--visibility must be org or restricted, not %q", vis)
	}
	mb, err := newMemberBinder(ctx, p.Root, vis)
	if err != nil {
		return err
	}
	if yes, _ := cmd.Flags().GetBool("yes"); yes && mb.group != nil {
		mb.group.adopt = askAdopt(true)
	}
	bindErr := bindMembers(ctx, out, mb, folders)
	return errors.Join(bindErr, syncWorkspace(ctx, out, p.Root))
}

// runWorkspaceRemove takes a folder out of the workspace. A member Project
// goes on on its own, keeping its sessions, binding and hooks; a folder
// added by hand that recorded here is unhooked.
func runWorkspaceRemove(cmd *cobra.Command, args []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := workspaceHere(ctx)
	if err != nil {
		return err
	}
	listed, err := project.RemoveFolder(p.Root, args[0])
	if err != nil {
		return err
	}
	c, err := project.Canonical(args[0])
	if err != nil {
		return err
	}
	switch ms := project.MembershipOf(ctx, p.Root, c); {
	case ms == project.Linked:
		leaveGroup(ctx, out, p.Root, []string{c})
		if err := project.Leave(c); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s is a Project on its own now: it keeps its sessions, binding and hooks, and its memory is its own again.\n", args[0])
		return nil
	case !listed:
		return fmt.Errorf("%s was not added by hand; a repository directly in the workspace is a member by itself (atlas-cli init --workspace=false in it binds it on its own)", args[0])
	case ms == project.Unbound:
		if err := errors.Join(unhookFolder(ctx, out, c)...); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Removed %s from the workspace.\n", args[0])
	return nil
}

// cloudOf is where dir's own store syncs, for the tree: its cloud Project's
// handle, "local", or "-" when it records into the workspace.
func cloudOf(ctx context.Context, dir string) string {
	b, err := bindingOf(ctx, dir)
	switch {
	case err != nil || b == nil:
		return "-"
	case b.Mode != store.ModeCloud:
		return "local"
	case b.Slug != nil && *b.Slug != "":
		return *b.Slug
	case b.RemoteWorkspaceID != nil:
		return *b.RemoteWorkspaceID
	}
	return "-"
}
