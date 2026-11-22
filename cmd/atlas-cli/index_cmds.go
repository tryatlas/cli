package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/tryatlas/cli/internal/clistate"
	"github.com/tryatlas/cli/internal/embed"
	"github.com/tryatlas/cli/internal/memory/corpus"
	"github.com/tryatlas/cli/internal/memory/record"
	"github.com/tryatlas/cli/internal/project"
	"github.com/tryatlas/cli/internal/tui"
)

// atlas-cli memory index: what the app's memory indexer does for a Project:
// the MiniLM model (downloaded once, shared with the app), a vector for
// every durable memory, and the corpus of documents memory_search returns.
func newMemoryIndexCmd() *cobra.Command {
	c := withRun(leaf("index", "Build the memory search index: memory vectors and the project's documents (downloads the model once)", cobra.NoArgs), runMemoryIndex)
	c.Flags().Bool("no-download", false, "do not download the model; index keywords only when it is missing")
	c.Flags().Bool("background", false, "run quietly, never download, skip when another pass runs (used by hooks)")
	_ = c.Flags().MarkHidden("background")
	return c
}

func runMemoryIndex(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	p, err := project.Discover(ctx, cwd)
	if errors.Is(err, project.ErrNotInitialised) {
		return errNoProject
	}
	if err != nil {
		return err
	}
	bg, _ := cmd.Flags().GetBool("background")
	noDL, _ := cmd.Flags().GetBool("no-download")
	if bg {
		release, ok := background(ctx)
		if !ok {
			return nil
		}
		defer release()
		// Passes asked for while this one ran left the mark: one more
		// covers them (a few at most: a hook storm waits for the next).
		for pass := 0; pass < maxBackgroundPasses && (pass == 0 || indexPending(p.Root)); pass++ {
			_, err := indexMemory(ctx, p.Root, p.ScopeRoot, false, func(string) {}, func(float64) {})
			if err != nil {
				if !errors.Is(err, errIndexBusy) {
					// The hooks log is where background work reports.
					fmt.Fprintln(cmd.ErrOrStderr(), "atlas-cli: memory index:", err)
				}
				return nil
			}
		}
		return nil
	}
	return tui.WorkProgress(cmd.OutOrStdout(), "Indexing memory", func(step tui.StepFunc, frac tui.FracFunc) (string, error) {
		return indexMemory(ctx, p.Root, p.ScopeRoot, !noDL, step, frac)
	})
}

var errIndexBusy = errors.New("another index pass is running")

// indexMemory is one pass: model (download when allowed), memory vectors,
// corpus. One pass per Project at a time.
func indexMemory(ctx context.Context, root, scope string, download bool, step tui.StepFunc, frac tui.FracFunc) (string, error) {
	if !project.MemorySharing(scope) {
		_ = os.Remove(clistate.Path(root, indexPendingFile))
		return "Shared memory is off here (atlas-cli memory enable); nothing to index.", nil
	}
	lock, err := os.OpenFile(clistate.Path(root, "index.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return "", errIndexBusy
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	_ = os.Remove(clistate.Path(root, indexPendingFile)) // this pass covers what asked for it

	plan := embed.Current()
	var downloadErr error
	if plan.Backend == embed.CPU && download && embed.LoadError() != nil {
		// Damaged files are kept by a download (it fetches what is missing):
		// they go first. The app's copy is the same files, as damaged.
		step("The model files here do not load; downloading them again")
		if err := embed.Remove(); err != nil {
			return "", err
		}
	}
	if plan.Backend == embed.CPU && !embed.Available() && download {
		step("Downloading the memory search model (" + embed.Size + ", shared with the Atlas app)")
		if err := embed.Download(ctx, func(done, total int64) {
			if total > 0 {
				frac(0.8 * float64(done) / float64(total))
			}
		}); err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("download the model: %w", ctx.Err())
			}
			// The model is an improvement, not a requirement: without it
			// the index is still built, on keywords.
			downloadErr = err
			step("The model could not be downloaded; indexing on keywords")
		}
	}
	vectors := record.Vectors()
	embedded := 0
	if st, err := record.Open(scope, true); err == nil {
		if vectors != nil {
			step("Embedding memories")
			embedded, err = st.SyncVectors(ctx)
			if err != nil && !errors.Is(err, record.ErrNoVectors) {
				_ = st.Close()
				return "", err
			}
		}
		_ = st.Close()
	} else {
		return "", err
	}
	frac(0.85)
	step("Reading the project's documents")
	docs, err := corpus.Collect(ctx, root, scope)
	if err != nil {
		return "", err
	}
	ix, err := corpus.Open(root, true)
	if err != nil {
		return "", err
	}
	defer ix.Close()
	step(fmt.Sprintf("Indexing %d documents", len(docs)))
	stats, err := ix.Update(ctx, docs, corpus.Owned, vectors)
	if err != nil {
		return "", err
	}
	frac(1)
	msg := fmt.Sprintf("Memory index ready: %d documents (%d new, %d changed, %d removed); %d memories embedded.",
		len(docs), stats.Added, stats.Updated, stats.Deleted, embedded)
	if vectors == nil {
		switch plan.Backend {
		case embed.Off:
			msg += " Keyword search only (" + plan.Note + ")."
		case embed.CPU:
			if downloadErr != nil {
				msg += fmt.Sprintf(" The model could not be downloaded (%v): keyword search only for now (atlas-cli memory index tries again).", downloadErr)
			} else if err := embed.LoadError(); err != nil {
				msg += fmt.Sprintf(" The model files do not load (%v): keyword search only (atlas-cli memory index downloads them again).", err)
			} else {
				msg += " No model on this machine: keyword search only (atlas-cli memory index downloads it)."
			}
		}
	}
	return msg, nil
}

// maxBackgroundPasses bounds one background run's passes.
const maxBackgroundPasses = 5

// indexPendingFile marks a Project whose index a hook asked to refresh.
const indexPendingFile = "index.pending"

func indexPending(root string) bool {
	_, err := os.Stat(clistate.Path(root, indexPendingFile))
	return err == nil
}

// indexRunning reports that a pass holds the Project's index lock now.
func indexRunning(root string) bool {
	f, err := os.OpenFile(clistate.Path(root, "index.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return true
	}
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return false
}

// spawnIndex runs a background index pass detached, as hooks do after a
// session (the app indexes when a turn finishes and after extraction). It
// marks the Project first; while a pass runs, the mark is enough: that pass
// runs once more when it ends.
func spawnIndex(root string) {
	_ = os.WriteFile(clistate.Path(root, indexPendingFile), nil, 0o600)
	if indexRunning(root) {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	c := exec.Command(self, "memory", "index", "--background") //nolint:gosec // G204: this binary
	c.Dir = root
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.Start() == nil {
		_ = c.Process.Release()
	}
}
