package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tryatlas/cli/internal/clistate"
	"github.com/tryatlas/cli/internal/embed"
	"github.com/tryatlas/cli/internal/hooks"
	"github.com/tryatlas/cli/internal/project"
	"github.com/tryatlas/cli/internal/tui"
)

// initStepper numbers atlas-cli init's steps: Organisation, Project, Agents and
// Background sync for a cloud Project; Agents only for a local one. A
// workspace whose repositories can become Projects adds Projects.
type initStepper []string

func initSteps(local, members bool) initStepper {
	s := initStepper{"Organisation", "Project", "Agents", "Background sync"}
	if local {
		s = initStepper{"Agents"}
	}
	if members {
		s = slices.Insert(s, slices.Index(s, "Agents"), "Projects")
	}
	if !embed.Available() {
		s = append(s, "Memory search")
	}
	return s
}

func (s initStepper) step(title string) tui.Step {
	for i, t := range s {
		if t == title {
			return tui.Step{N: i + 1, Total: len(s), Title: title}
		}
	}
	return tui.Step{Title: title}
}

// finishInit runs the steps after the binding: which agents to hook, and
// whether to keep syncing in the background.
func finishInit(cmd *cobra.Command, root string, local, guided bool, steps initStepper) error {
	out := cmd.OutOrStdout()
	agents, err := pickAgents(cmd, root, guided, steps.step("Agents"))
	if err != nil {
		return err
	}
	on, ask := false, false
	if !local {
		if on, ask, err = pickSync(cmd, root, guided, steps.step("Background sync")); err != nil {
			return err
		}
	}
	model := false
	if guided && !embed.Available() && project.MemorySharing(project.MemoryRoot(cmd.Context(), root)) {
		i, err := chooseStep(steps.step("Memory search"), "Download the memory search model? Agents then find memories by meaning, not only by words.", []tui.Choice{
			{Label: "Download", Detail: embed.Size + ", once; the same model the Atlas app uses"},
			{Label: "Not now", Detail: "keyword search; atlas-cli memory index downloads it later"},
		}, 0)
		if err != nil {
			return err
		}
		model = i == 0
	}
	if !guided {
		if len(agents) > 0 {
			if err := installAgents(cmd.Context(), out, root, agents, false, false); err != nil {
				return err
			}
		}
		if ask {
			msg, err := setBackgroundSync(root, on)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, msg)
		}
		return nil
	}
	// On a terminal the work runs under the progress bar, each line a step.
	if model {
		fmt.Fprintln(out)
		scope := project.MemoryRoot(cmd.Context(), root)
		if err := tui.WorkProgress(out, "Setting up memory search", func(step tui.StepFunc, frac tui.FracFunc) (string, error) {
			return indexMemory(cmd.Context(), root, scope, true, step, frac)
		}); err != nil {
			if cmd.Context().Err() != nil {
				return err
			}
			// Memory search is the last, optional step: a failed index
			// (the bar has shown why) must not leave the agents unhooked.
			tui.Note(out, "Memory search uses keywords for now; atlas-cli memory index sets it up later.")
			spawnIndex(root)
		}
	} else {
		spawnIndex(root) // the index, in the background (keywords without the model)
	}
	if len(agents) > 0 || ask {
		fmt.Fprintln(out)
		err := tui.Work(out, "Initializing Atlas in "+root, func(step tui.StepFunc) (string, error) {
			w := tui.Lines(io.Discard, func(l string) string {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "note:") {
					step(l)
				}
				return l
			})
			if len(agents) > 0 {
				if err := installAgents(cmd.Context(), w, root, agents, false, false); err != nil {
					return "", err
				}
			}
			if ask {
				msg, err := setBackgroundSync(root, on)
				if err != nil {
					return "", err
				}
				step(msg)
			}
			return "Atlas is ready in " + root, nil
		})
		if err != nil {
			return err
		}
		for _, a := range agents {
			if n, ok := a.(hooks.Noter); ok {
				tui.Note(out, "↳ "+n.AddNote())
			}
		}
	} else {
		tui.Ready(out, "Atlas is ready in "+root)
	}
	fmt.Fprintln(out)
	tui.Note(out, "Use your agents as usual; sessions and memory are captured as they run.")
	tui.Note(out, "atlas-cli status shows what was captured · atlas-cli settings changes any of this")
	return nil
}

// detail says what the machine has of an agent.
func agentDetail(a hooks.Agent, root string) string {
	d := "configured here"
	if _, err := exec.LookPath(a.Binary()); err == nil {
		d = a.Binary() + " on PATH"
	}
	switch a.State(root) {
	case hooks.Current:
		d += " · already hooked"
	case hooks.Outdated:
		d += " · hook outdated"
	}
	return d
}

// pickAgents is step 3: --agents or --all-agents, else a checklist of the
// agents detected here with a row that selects them all.
func pickAgents(cmd *cobra.Command, root string, guided bool, step tui.Step) ([]hooks.Agent, error) {
	flags := cmd.Flags()
	ids, _ := flags.GetStringSlice("agents")
	all, _ := flags.GetBool("all-agents")
	var detected []hooks.Agent
	for _, a := range hooks.All() {
		if a.Detect(root) {
			detected = append(detected, a)
		}
	}
	switch {
	case len(ids) > 0:
		return resolveAgents(ids, false, nil)
	case all:
		if len(detected) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No supported agent was found on this machine (atlas-cli agents list).")
		}
		return detected, nil
	case !guided:
		return nil, nil
	case len(detected) == 0:
		tui.StepDone(cmd.OutOrStdout(), step, "none found on this machine (atlas-cli agents add <agent> later)")
		return nil, nil
	}
	choices := make([]tui.Choice, len(detected))
	checked := make([]bool, len(detected))
	for i, a := range detected {
		choices[i] = tui.Choice{Label: a.Name(), Detail: agentDetail(a, root)}
		checked[i] = true
	}
	picked, err := chooseMany(step, "Which agents should Atlas capture and give shared memory?", "All agents", choices, checked)
	if err != nil {
		return nil, err
	}
	out := make([]hooks.Agent, len(picked))
	for i, n := range picked {
		out[i] = detected[n]
	}
	return out, nil
}

// pickSync is step 4: background sync on or off. ask is false when nothing
// was asked and no flag given, so the current choice stays.
func pickSync(cmd *cobra.Command, root string, guided bool, step tui.Step) (on, ask bool, err error) {
	switch v, _ := cmd.Flags().GetString("sync"); v {
	case "on":
		return true, true, nil
	case "off":
		return false, true, nil
	case "":
	default:
		return false, false, fmt.Errorf("--sync must be on or off, not %q", v)
	}
	if !guided {
		return false, false, nil
	}
	initial := 1
	if clistate.LoadPrefs(root).BackgroundSync {
		initial = 0
	}
	i, err := chooseStep(step, "Keep syncing in the background? (atlas-cli sync on|off changes it later)", []tui.Choice{
		{Label: "On", Detail: "a background atlas-cli serve sends prompts, tool calls and replies as they happen"},
		{Label: "Off", Detail: "each agent hook syncs on its own; nothing keeps running"},
	}, initial)
	if err != nil {
		return false, false, err
	}
	return i == 0, true, nil
}

// pickWorkspace decides whether the launch dir is bound as a workspace, a
// folder of repositories bound once (ADR-0015). A workspace captures every
// repository below it, so it is opt-in: --workspace when given; else, for a
// folder outside git that holds repositories, asked on a terminal with "This
// folder only" pre-selected, and never without one (init names the
// repositories and how to capture them). A folder that is already a
// workspace stays one unless --workspace=false or the prompt says otherwise.
func pickWorkspace(cmd *cobra.Command, launch, scope string, guided bool) (bool, error) {
	flags := cmd.Flags()
	members := project.Members(launch)
	names := memberNames(launch, members)
	if flags.Changed("workspace") {
		if on, _ := flags.GetBool("workspace"); !on {
			return false, nil
		}
		if launch != scope {
			return false, fmt.Errorf("%s is inside the repository %s; a workspace is the folder that holds the repositories", launch, scope)
		}
		if len(members) == 0 {
			return false, fmt.Errorf("%s holds no git repository; a workspace is a folder of repositories", launch)
		}
		if !guided {
			fmt.Fprintf(cmd.ErrOrStderr(), "Binding %s as a workspace of %s (%s).\n", launch, repos(len(members)), names)
		}
		return true, nil
	}
	current := project.IsWorkspace(launch)
	if len(members) == 0 || (!current && project.InRepo(cmd.Context(), launch)) {
		return current, nil
	}
	if !guided {
		if !current {
			fmt.Fprintf(cmd.ErrOrStderr(), "Note: %s holds %s (%s). Agents started in them are not captured; atlas-cli workspace on captures them here.\n",
				launch, repos(len(members)), names)
		}
		return current, nil
	}
	initial := 0
	if current {
		initial = 1
	}
	i, err := chooseStep(tui.Step{Title: "Workspace"}, fmt.Sprintf("This folder holds %s: %s.", repos(len(members)), names), []tui.Choice{
		{Label: "This folder only", Detail: "agents started in those repositories are not captured here"},
		{Label: "Workspace with " + projects(len(members)), Detail: "each repository is a Project of its own; agents started anywhere share one memory"},
	}, initial)
	if err != nil {
		return false, err
	}
	return i == 1, nil
}

// pickJoin decides whether launch, a member folder of the workspace ws,
// joins it as a member Project (ADR-0016): --workspace when given; else a
// folder not bound yet joins and a bound one stays what it is. On a
// terminal it is asked, that answer pre-selected; asked reports it was.
func pickJoin(cmd *cobra.Command, launch, ws string, guided bool) (join, asked bool, err error) {
	switch project.MembershipOf(cmd.Context(), ws, launch) {
	case project.Elsewhere:
		other, _ := project.LinkOf(launch)
		return false, false, fmt.Errorf("%s is a Project in the workspace %s; atlas-cli workspace remove %s there first", launch, other, launch)
	case project.Independent:
		join = false
	default:
		join = true
	}
	if flags := cmd.Flags(); flags.Changed("workspace") {
		join, _ = flags.GetBool("workspace")
		return join, false, nil
	}
	if !guided {
		if join {
			fmt.Fprintf(cmd.ErrOrStderr(), "Binding %s as a Project in the workspace %s (--workspace=false binds it on its own).\n", launch, ws)
		}
		return join, false, nil
	}
	initial := 1
	if join {
		initial = 0
	}
	i, err := chooseStep(tui.Step{Title: "Workspace"}, fmt.Sprintf("%s is in the workspace %s.", filepath.Base(launch), ws), []tui.Choice{
		{Label: "A Project in the workspace", Detail: "its own sessions and timeline; the workspace's memory"},
		{Label: "A Project on its own", Detail: "outside the workspace: its own sessions and memory"},
	}, initial)
	if err != nil {
		return false, false, err
	}
	return i == 0, true, nil
}

// memberCandidates are the folders init offers as member Projects of the
// workspace ws: its members not bound yet, and folders bound on their own.
func memberCandidates(ctx context.Context, ws string) []string {
	var out []string
	for _, f := range project.MemberFolders(ws) {
		if m := project.MembershipOf(ctx, ws, f); m == project.Unbound || m == project.Independent {
			out = append(out, f)
		}
	}
	return out
}

// offerMembers binds the workspace's repositories as member Projects
// (ADR-0016). On a terminal each one that is not a Project in it yet is
// listed, checked, and the chosen ones are bound; the rest record into the
// workspace. Without a terminal none is bound, so no repository reaches the
// cloud unasked: init says how (atlas-cli workspace add). A repository that
// cannot be bound is a note, not a failed init.
func offerMembers(cmd *cobra.Command, ws string, guided bool, steps initStepper) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	cands := memberCandidates(ctx, ws)
	if len(cands) == 0 {
		return nil
	}
	markKnown(ws, cands) // asked about here: serve does not point them out again
	if !guided {
		fmt.Fprintf(cmd.ErrOrStderr(), "Note: %s record into the workspace until each is a Project of its own: atlas-cli workspace add --all, or add <folder>.\n",
			memberNames(ws, cands))
		return nil
	}
	step := steps.step("Projects")
	choices := make([]tui.Choice, len(cands))
	checked := make([]bool, len(cands))
	for i, c := range cands {
		choices[i] = tui.Choice{Label: memberNames(ws, []string{c}), Detail: "its own sessions and timeline"}
		if project.MembershipOf(ctx, ws, c) == project.Independent {
			choices[i].Detail = "a Project on its own now; joins as it is"
		}
		checked[i] = true
	}
	picked, err := chooseMany(step, "Which repositories become Projects in the workspace? The others record into it.", "All repositories", choices, checked)
	if err != nil {
		return err
	}
	if len(picked) == 0 {
		return nil // the step's line says none; they record into the workspace
	}
	chosen := make([]string, len(picked))
	for i, n := range picked {
		chosen[i] = cands[n]
	}
	vis, _ := cmd.Flags().GetString("visibility")
	mb, err := newMemberBinder(ctx, ws, vis)
	if err != nil {
		tui.Note(out, "The repositories record into the workspace for now ("+err.Error()+"); atlas-cli workspace add --all binds them later.")
		return nil
	}
	if err := bindMembers(ctx, tui.Indent(out), mb, chosen); err != nil {
		tui.Note(out, "Not bound, they record into the workspace for now: "+err.Error())
	}
	return nil
}

// projects is "1 Project" or "n Projects".
func projects(n int) string {
	if n == 1 {
		return "1 Project"
	}
	return fmt.Sprintf("%d Projects", n)
}

// memberNames lists member repositories by folder name, at most five.
func memberNames(root string, members []string) string {
	var names []string
	for i, m := range members {
		if i == 5 {
			names = append(names, fmt.Sprintf("and %d more", len(members)-5))
			break
		}
		rel, err := filepath.Rel(root, m)
		if err != nil {
			rel = filepath.Base(m)
		}
		names = append(names, rel)
	}
	return strings.Join(names, ", ")
}

// repos is "1 repository" or "n repositories".
func repos(n int) string {
	if n == 1 {
		return "1 repository"
	}
	return fmt.Sprintf("%d repositories", n)
}
