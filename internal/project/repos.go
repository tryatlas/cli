package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Repo is a git repository whose work a store records (ADR-0017): the
// store's root itself, or, for a workspace folder outside git, one of its
// member repositories, whose paths the workspace's store spells with Prefix.
type Repo struct {
	Dir    string // the repository's folder
	Prefix string // "" for the store's root; "<folder>/" for a member
}

// Repos are the repositories the store rooted at root records work in:
// root itself, or, for a workspace, each member folder holding .git, bound
// on its own or not (what a workspace session changes there is its work
// all the same).
func Repos(root string) []Repo {
	if !IsWorkspace(root) {
		return []Repo{{Dir: root}}
	}
	var out []Repo
	for _, f := range MemberFolders(root) {
		if _, err := os.Stat(filepath.Join(f, ".git")); err != nil {
			continue
		}
		rel, err := filepath.Rel(root, f)
		if err != nil {
			continue
		}
		out = append(out, Repo{Dir: f, Prefix: filepath.ToSlash(rel) + "/"})
	}
	return out
}

// repoWorkers bounds the git processes one sampling runs at once.
const repoWorkers = 8

// EachRepo runs fn on every repository at once, at most repoWorkers at a
// time, and returns the results in order: a workspace of many repositories
// samples git in the time of a few (TRD risk K4).
func EachRepo[T any](repos []Repo, fn func(Repo) T) []T {
	out := make([]T, len(repos))
	if len(repos) == 1 {
		out[0] = fn(repos[0])
		return out
	}
	sem := make(chan struct{}, repoWorkers)
	var wg sync.WaitGroup
	for i, r := range repos {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = fn(r)
		}()
	}
	wg.Wait()
	return out
}

// Heads spells the HEAD of each repository as one string, which hooks keep
// between events: the commit itself for a store's own repository; for a
// workspace one "<prefix>=<commit>" line per member that has a commit,
// sorted. heads[i] is repos[i]'s commit, "" when unborn.
func Heads(repos []Repo, heads []string) string {
	if len(repos) == 1 && repos[0].Prefix == "" {
		return heads[0]
	}
	var lines []string
	for i, r := range repos {
		if heads[i] != "" {
			lines = append(lines, r.Prefix+"="+heads[i])
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// SplitHeads reads Heads back: prefix → commit, "" for a store's own
// repository.
func SplitHeads(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	if !strings.Contains(s, "=") {
		out[""] = s
		return out
	}
	for _, line := range strings.Split(s, "\n") {
		if i := strings.LastIndexByte(line, '='); i > 0 { // a commit never holds "="
			out[line[:i]] = line[i+1:]
		}
	}
	return out
}
