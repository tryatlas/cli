package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/project"
	"github.com/tryatlas/cli/internal/tui"
)

// atlas-cli workspace upgrade (FR-17, ADR-0018) brings a workspace bound
// before its repositories could be Projects (ADR-0015), or before the cloud
// had groups, up to date in one run:
//
//  1. each repository that records into the workspace becomes a Project;
//  2. in the cloud, the workspace's Project becomes a group (promoted when it
//     was registered by name) and each Project goes into it;
//  3. the sessions already sent go again, so they get their tags.
//
// Sessions recorded so far stay in the workspace's store: moving them would
// change their ids and break their commit links. Every step skips what is
// done, so it can be run again.

func newWorkspaceUpgradeCmd() *cobra.Command {
	c := withRun(leaf("upgrade", "Bring the workspace up to date: its repositories become Projects, grouped in the cloud, and earlier sessions get their tags", cobra.NoArgs), runWorkspaceUpgrade)
	c.Flags().Bool("dry-run", false, "say what would change and change nothing")
	c.Flags().Bool("yes", false, "do it without asking, and put Projects matched in the cloud in the workspace's group")
	c.Flags().String("visibility", "org", "who sees a new cloud Project: org or restricted")
	return c
}

// upgradePlan is what an upgrade would do.
type upgradePlan struct {
	bind   []string // repositories that record into the workspace
	linked []string // member Projects already
	cloud  bool
}

func planUpgrade(ctx context.Context, ws string) (upgradePlan, error) {
	b, err := bindingOf(ctx, ws)
	if err != nil {
		return upgradePlan{}, err
	}
	if b == nil {
		return upgradePlan{}, project.ErrNotInitialised
	}
	pl := upgradePlan{linked: linkedMembers(ctx, ws), cloud: b.Mode == store.ModeCloud}
	for _, m := range unboundMembers(ctx, ws) {
		if isRepo(m) {
			pl.bind = append(pl.bind, m)
		}
	}
	return pl, nil
}

func (pl upgradePlan) print(out io.Writer, ws string) {
	if len(pl.bind) > 0 {
		fmt.Fprintf(out, "- %s become Projects of their own (%s).\n", memberNames(ws, pl.bind), map[bool]string{true: "in the workspace's organisation", false: "local"}[pl.cloud])
	} else {
		fmt.Fprintln(out, "- Every repository is a Project already.")
	}
	if !pl.cloud {
		fmt.Fprintln(out, "- The workspace is local: nothing changes in the cloud.")
		return
	}
	fmt.Fprintln(out, "- In the cloud, where the organisation has workspaces: the workspace's Project holds its Projects, and each goes into it.")
	fmt.Fprintln(out, "- Sessions already sent go again, so each shows on the Projects it changed. They stay in the workspace's timeline.")
}

func runWorkspaceUpgrade(cmd *cobra.Command, _ []string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	p, err := workspaceHere(ctx)
	if err != nil {
		return err
	}
	ws := p.Root
	pl, err := planUpgrade(ctx, ws)
	if err != nil {
		return err
	}
	dry, _ := cmd.Flags().GetBool("dry-run")
	yes, _ := cmd.Flags().GetBool("yes")
	fmt.Fprintf(out, "Upgrading the workspace %s:\n", ws)
	pl.print(out, ws)
	if dry {
		fmt.Fprintln(out, "Nothing changed (--dry-run).")
		return nil
	}
	if len(pl.bind) == 0 && !pl.cloud {
		fmt.Fprintln(out, "Nothing to do.")
		return nil
	}
	if !yes {
		if !interactive() {
			return errors.New("pass --yes to upgrade without a prompt (--dry-run shows what changes)")
		}
		i, err := chooseStep(tui.Step{Title: "Upgrade"}, "Upgrade the workspace?", []tui.Choice{{Label: "Upgrade"}, {Label: "Cancel"}}, 0)
		if err != nil {
			return err
		}
		if i != 0 {
			return tui.ErrCancelled
		}
	}
	vis, _ := cmd.Flags().GetString("visibility")
	if vis != "org" && vis != "restricted" {
		return fmt.Errorf("--visibility must be org or restricted, not %q", vis)
	}
	mb, err := newMemberBinder(ctx, ws, vis)
	if err != nil {
		return err
	}
	if mb.group != nil {
		mb.group.adopt = askAdopt(yes)
	}
	markKnown(ws, pl.bind)
	// The Projects already linked go through too: in the cloud they are put
	// in the group; locally nothing changes for them.
	bindErr := bindMembers(ctx, out, mb, append(pl.bind, pl.linked...))
	return errors.Join(bindErr, syncWorkspace(ctx, out, ws))
}
