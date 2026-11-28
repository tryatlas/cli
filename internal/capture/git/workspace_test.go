package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tryatlas/cli/internal/capture/record"
	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/project"
)

// ADR-0017 (TRD S5): a session started in the workspace folder changes
// backend/api.go; a commit in the backend repository carries it. The
// workspace's store links the commit, keeps the backend/ prefix on the
// checkpoint, has a cursor for backend, and sends the session again.
func TestAWorkspaceSessionLinksToAMembersCommit(t *testing.T) {
	ctx := context.Background()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	ws := filepath.Join(base, "acme-shop")
	backend := filepath.Join(ws, "backend")
	if err := os.MkdirAll(backend, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := &fixture{t: t, root: backend, past: true}
	repo.git("init", "-q", "-b", "main")
	repo.write("existing.go", "package main\n")
	repo.git("add", ".")
	repo.git("commit", "-q", "-m", "base")
	repo.past = false

	st, err := store.Open(ws, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, id := "org-1", "ws-shop"
	if err := st.BindCloud(ctx, store.Binding{WorkspaceID: ws, Root: ws, OrgID: &org, RemoteWorkspaceID: &id, Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := project.SetWorkspace(ws, true); err != nil {
		t.Fatal(err)
	}
	w := &fixture{t: t, root: ws, st: st, rec: record.New(st, ws), sess: record.Session{Agent: "claude-code", NativeID: "s-1", Cwd: ws}}
	if _, err := w.rec.Prompt(ctx, w.sess, "add the user API"); err != nil {
		t.Fatal(err)
	}
	sent := func() {
		t.Helper()
		if _, err := st.DB().Exec(`UPDATE agent_session SET sync_state = 'sent'`); err != nil {
			t.Fatal(err)
		}
	}
	state := func() string { return q(t, st.DB(), `SELECT sync_state FROM agent_session`) }

	sent()
	w.agentWrites("backend/api.go", "package main\n\nfunc API() {}\n")
	if got := q(t, st.DB(), `SELECT path FROM file_touch`); got != "backend/api.go" {
		t.Fatalf("touch %q", got)
	}
	if state() != store.SyncPending {
		t.Fatal("a touch in a member did not send the session again")
	}

	sent()
	repo.git("add", "api.go")
	repo.git("commit", "-q", "-m", "user API")
	head := repo.git("rev-parse", "HEAD")
	res, err := Link(ctx, st, ws, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checkpoints != 1 {
		t.Fatalf("result %+v", res)
	}
	if got := q(t, st.DB(), `SELECT files_touched FROM checkpoint`); got != `["backend/api.go"]` {
		t.Fatalf("checkpoint files %s", got)
	}
	if got := q(t, st.DB(), `SELECT commit_sha FROM checkpoint`); got != head {
		t.Fatalf("checkpoint commit %s, want %s", got, head)
	}
	if got := q(t, st.DB(), `SELECT consumed_by_commit FROM file_touch`); got != head {
		t.Fatalf("touch consumed by %q", got)
	}
	if cur, _ := st.GetCursor(ctx, backend); cur == nil || cur.LastSeen == nil || *cur.LastSeen != head {
		t.Fatalf("backend's cursor: %+v", cur)
	}
	if state() != store.SyncPending {
		t.Fatal("a commit in a member did not send the session again")
	}

	// A turn's own commit, linked when its touches arrive later (LinkRange
	// with the hooks' heads).
	from := project.Heads(project.Repos(ws), []string{head})
	repo.write("api.go", "package main\n\nfunc API() { _ = 1 }\n")
	repo.git("commit", "-q", "-am", "tweak")
	w.agentWrites("backend/api.go", "package main\n\nfunc API() { _ = 1 }\n")
	n, err := LinkRange(ctx, st, ws, from, time.Now)
	if err != nil || n != 1 {
		t.Fatalf("LinkRange: %d %v", n, err)
	}
	if got := q(t, st.DB(), `SELECT COUNT(*) FROM checkpoint`); got != "2" {
		t.Fatalf("checkpoints: %s", got)
	}

	// Link again: nothing new, and a plain Project's walk is unchanged.
	if res, err := Link(ctx, st, ws, time.Now); err != nil || res.Checkpoints != 0 {
		t.Fatalf("second walk: %+v %v", res, err)
	}
}
