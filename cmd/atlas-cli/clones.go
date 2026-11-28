package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/tryatlas/cli/internal/clistate"
	"github.com/tryatlas/cli/internal/tui"
)

// New clones (FR-15, ADR-0018): a repository cloned into a workspace after it
// was bound records into the workspace folder until it is a Project of its
// own. atlas-cli serve says so once per repository; atlas-cli workspace sync
// offers to bind them on a terminal. None is bound without a yes: binding
// may send a repository to the cloud.

// knownFolders names the file listing the repositories already pointed out.
const knownFolders = "known-repositories"

// newClones are the workspace's repositories that record into it and were
// never pointed out.
func newClones(ctx context.Context, ws string) []string {
	known := readKnown(ws)
	var out []string
	for _, m := range unboundMembers(ctx, ws) {
		if isRepo(m) && !slices.Contains(known, m) {
			out = append(out, m)
		}
	}
	return out
}

func readKnown(ws string) []string {
	b, err := os.ReadFile(clistate.Path(ws, knownFolders))
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

// markKnown records dirs as pointed out.
func markKnown(ws string, dirs []string) {
	known := readKnown(ws)
	for _, d := range dirs {
		if !slices.Contains(known, d) && !strings.ContainsAny(d, " \t\n") {
			known = append(known, d)
		}
	}
	_ = os.WriteFile(clistate.Path(ws, knownFolders), []byte(strings.Join(known, "\n")+"\n"), 0o600)
}

// sayNewClones points out the new clones once, with the command that binds
// them, and marks them pointed out.
func sayNewClones(ctx context.Context, out io.Writer, ws string) {
	fresh := newClones(ctx, ws)
	if len(fresh) == 0 {
		return
	}
	names := memberNames(ws, fresh)
	fmt.Fprintf(out, "New in the workspace: %s. %s into the workspace folder until each is a Project of its own: atlas-cli workspace add %s.\n",
		names, verb(len(fresh), "It records", "They record"), strings.ReplaceAll(names, ", ", " "))
	markKnown(ws, fresh)
}

// offerNewClones asks, on a terminal, which new clones become Projects and
// binds those; without a terminal it points them out. The ones not picked
// are not asked about again.
func offerNewClones(ctx context.Context, out io.Writer, ws string) error {
	fresh := newClones(ctx, ws)
	if len(fresh) == 0 {
		return nil
	}
	if !interactive() {
		sayNewClones(ctx, out, ws)
		return nil
	}
	choices := make([]tui.Choice, len(fresh))
	checked := make([]bool, len(fresh))
	for i, f := range fresh {
		choices[i] = tui.Choice{Label: memberNames(ws, []string{f}), Detail: "new since the workspace was bound"}
		checked[i] = true
	}
	picked, err := chooseMany(tui.Step{Title: "New repositories"}, "Which new repositories become Projects in the workspace? The others record into it.", "All new repositories", choices, checked)
	markKnown(ws, fresh)
	if err != nil {
		return err
	}
	if len(picked) == 0 {
		return nil
	}
	var dirs []string
	for _, i := range picked {
		dirs = append(dirs, fresh[i])
	}
	mb, err := newMemberBinder(ctx, ws, "org")
	if err != nil {
		return err
	}
	return bindMembers(ctx, out, mb, dirs)
}

// verb picks the singular or the plural.
func verb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
