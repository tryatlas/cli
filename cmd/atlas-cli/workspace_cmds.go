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

	"github.com/tryatlas/cli/internal/hooks"
	"github.com/tryatlas/cli/internal/hooks/mcpconfig"
	"github.com/tryatlas/cli/internal/project"
)

// atlas-cli workspace: one Project for a folder of repositories (ADR-0015).

func newWorkspaceCmd() *cobra.Command {
	c := group("workspace", "One Project for a folder of repositories: show its folders, hook new ones",
		withRun(leaf("on", "Make this Project a workspace: agents started in its repositories record here", cobra.NoArgs), runWorkspaceOn),
		withRun(leaf("off", "Make this Project a single folder again and unhook its repositories", cobra.NoArgs), runWorkspaceOff),
		withRun(leaf("sync", "Hook folders added since: every member gets the agents hooked in the workspace folder", cobra.NoArgs), runWorkspaceSync),
		withRun(leaf("add <folder>", "Add a folder without git (or a repository deeper down) to the workspace and hook it", cobra.ExactArgs(1)), runWorkspaceAdd),
		withRun(leaf("remove <folder>", "Take a folder added by hand out of the workspace and unhook it", cobra.ExactArgs(1)), runWorkspaceRemove),
	)
	c.RunE = runWorkspaceShow
	return c
}

var errNotWorkspace = errors.New("this Project is not a workspace; atlas-cli workspace on (in the folder that holds the repositories) makes it one")

// workspaceHere is the workspace around the cwd.
func workspaceHere(ctx context.Context) (*project.Project, error) {
	p, err := agentRoot(ctx)
	if err != nil {
		return nil, err
	}
	if !p.Workspace {
		return nil, errNotWorkspace
	}
	return p, nil
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

func runWorkspaceShow(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	p, err := agentRoot(cmd.Context())
	if err != nil {
		return err
	}
	if !p.Workspace {
		fmt.Fprintf(out, "%s is a single-folder Project, not a workspace (atlas-cli workspace on).\n", p.Root)
		return nil
	}
	agents := hookedAgents(p.Root)
	fmt.Fprintf(out, "Workspace: %s\n", p.Root)
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "FOLDER\tKIND\tAGENTS")
	missing := false
	for _, t := range project.Targets(p.Root) {
		name, kind := ".", "workspace folder"
		if t != p.Root {
			name, _ = filepath.Rel(p.Root, t)
			kind = "folder"
			if _, err := os.Stat(filepath.Join(t, ".git")); err == nil {
				kind = "repository"
			}
		}
		var on, off []string
		for _, a := range agents {
			if a.State(t) == hooks.Current {
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
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, kind, state)
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
	return nil
}

func runWorkspaceOn(cmd *cobra.Command, _ []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := agentRoot(ctx)
	if err != nil {
		return err
	}
	if p.Workspace {
		fmt.Fprintf(out, "%s is already a workspace.\n", p.Root)
		return syncWorkspace(ctx, out, p.Root)
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
	return syncWorkspace(ctx, out, p.Root)
}

func runWorkspaceOff(cmd *cobra.Command, _ []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := workspaceHere(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range project.Members(p.Root) {
		errs = append(errs, unhookFolder(ctx, out, m)...)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if err := project.SetWorkspace(p.Root, false); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is a single-folder Project again. What was recorded stays.\n", p.Root)
	return nil
}

func runWorkspaceSync(cmd *cobra.Command, _ []string) error {
	p, err := workspaceHere(cmd.Context())
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Workspace %s: %s.\n", p.Root, workspaceRepos(p.Root))
	return syncWorkspace(cmd.Context(), cmd.OutOrStdout(), p.Root)
}

func runWorkspaceAdd(cmd *cobra.Command, args []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := workspaceHere(ctx)
	if err != nil {
		return err
	}
	added, err := project.AddFolder(p.Root, args[0])
	if err != nil {
		return err
	}
	if added {
		fmt.Fprintf(out, "Added %s to the workspace.\n", args[0])
	} else {
		fmt.Fprintf(out, "%s is already in the workspace.\n", args[0])
	}
	return syncWorkspace(ctx, out, p.Root)
}

func runWorkspaceRemove(cmd *cobra.Command, args []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := workspaceHere(ctx)
	if err != nil {
		return err
	}
	removed, err := project.RemoveFolder(p.Root, args[0])
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("%s was not added by hand; a repository directly in the workspace is a member by itself (its own atlas-cli init splits it off)", args[0])
	}
	c, err := project.Canonical(args[0])
	if err != nil {
		return err
	}
	if err := errors.Join(unhookFolder(ctx, out, c)...); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed %s from the workspace.\n", args[0])
	return nil
}
