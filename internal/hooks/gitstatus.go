package hooks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tryatlas/cli/internal/project"
)

// gitStatus is the set of paths git status reports under root, renames
// counted on both sides; nil outside a repository. In a workspace each
// member repository's paths are spelled with its folder (ADR-0017).
func gitStatus(ctx context.Context, root string) map[string]bool {
	repos := project.Repos(root)
	parts := project.EachRepo(repos, func(r project.Repo) map[string]bool { return repoStatus(ctx, r.Dir) })
	var out map[string]bool
	for i, m := range parts {
		if m == nil {
			continue
		}
		if out == nil {
			out = make(map[string]bool, len(m))
		}
		for p := range m {
			out[repos[i].Prefix+p] = true
		}
	}
	return out
}

// repoStatus is one repository's git status.
func repoStatus(ctx context.Context, dir string) map[string]bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain=v1", "-z", "--untracked-files=all") //nolint:gosec // G204: fixed read-only git subcommand
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	b, err := cmd.Output()
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	fields := strings.Split(string(b), "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		out[f[3:]] = true
		if f[0] == 'R' || f[0] == 'C' {
			if i+1 < len(fields) && fields[i+1] != "" {
				out[fields[i+1]] = true
			}
			i++
		}
	}
	return out
}

// changedSince is what git status reports now that it did not report at the
// turn start, plus paths it reported then that are gone from the tree now (an
// untracked file the turn deleted). Paths dirty both before and after cannot
// be told apart and are left out.
func changedSince(root string, before, after map[string]bool) []string {
	var out []string
	for p := range after {
		if !before[p] {
			out = append(out, p)
		}
	}
	for p := range before {
		if after[p] {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// relPath makes an agent-reported path Project-relative with "/"; a path
// outside root is returned absolute.
func relPath(root, p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

// gitHead is HEAD's commit, or "" (unborn, not a repository). In a
// workspace it is every member's (project.Heads), so a commit in any of
// them moves it.
func gitHead(ctx context.Context, root string) string {
	repos := project.Repos(root)
	return project.Heads(repos, project.EachRepo(repos, func(r project.Repo) string { return repoHead(ctx, r.Dir) }))
}

func repoHead(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "-q", "--verify", "HEAD") //nolint:gosec // G204: fixed read-only git subcommand
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// committedSince are the paths changed by the commits from..HEAD: files the
// turn committed, which git status no longer shows. from is gitHead's
// answer at the start; in a workspace, each member that moved since counts,
// its paths spelled with its folder.
func committedSince(ctx context.Context, root, from string) []string {
	if from == "" {
		return nil
	}
	heads := project.SplitHeads(from)
	repos := project.Repos(root)
	parts := project.EachRepo(repos, func(r project.Repo) []string {
		if f := heads[r.Prefix]; f != "" {
			return repoCommittedSince(ctx, r.Dir, f)
		}
		return nil
	})
	var out []string
	for i, ps := range parts {
		for _, p := range ps {
			out = append(out, repos[i].Prefix+p)
		}
	}
	return out
}

func repoCommittedSince(ctx context.Context, dir, from string) []string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--name-only", "-z", "--no-renames", from, "HEAD") //nolint:gosec // G204: fixed read-only git subcommand
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	b, err := cmd.Output()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range strings.Split(string(b), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
