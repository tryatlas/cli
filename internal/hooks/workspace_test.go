package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tryatlas/cli/internal/project"
)

// ADR-0017 (FR-9): a turn started in a workspace folder sees git in each
// member repository. A shell change in backend and a commit in frontend are
// the turn's touches, spelled with the member's folder; the hooks' head
// names each member's commit.
func TestATurnInAWorkspaceSeesEachMembersGit(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	ws := filepath.Join(base, "acme-shop")
	run := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	heads := map[string]string{}
	for _, m := range []string{"backend", "frontend"} {
		dir := filepath.Join(ws, m)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		run(dir, "init", "-q", "-b", "main")
		_ = os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old\n"), 0o644)
		run(dir, "add", ".")
		run(dir, "commit", "-q", "-m", "init")
		heads[m] = run(dir, "rev-parse", "HEAD")
	}
	st := enabledStore(t, ws)
	if err := project.SetWorkspace(ws, true); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if got, want := gitHead(ctx, ws), "backend/="+heads["backend"]+"\nfrontend/="+heads["frontend"]; got != want {
		t.Fatalf("head %q, want %q", got, want)
	}
	if got := gitStatus(ctx, ws); len(got) != 0 {
		t.Fatalf("clean workspace status: %v", got)
	}

	transcript := filepath.Join(t.TempDir(), "s.jsonl")
	_ = os.WriteFile(transcript, nil, 0o600)
	d := &Dispatcher{Root: ws, Version: "test", Now: time.Now, Deliver: Deliver("test", nil)}
	sess := map[string]any{"Session": "s-1", "Transcript": transcript}
	runHook(t, d, "start", sess)
	runHook(t, d, "prompt", map[string]any{"Session": "s-1", "Transcript": transcript, "Prompt": "change both"})

	// The agent's shell changes backend and commits in frontend.
	_ = os.WriteFile(filepath.Join(ws, "backend", "by-shell.txt"), []byte("x\n"), 0o644)
	_ = os.WriteFile(filepath.Join(ws, "frontend", "committed.txt"), []byte("y\n"), 0o644)
	run(filepath.Join(ws, "frontend"), "add", ".")
	run(filepath.Join(ws, "frontend"), "commit", "-q", "-m", "work")
	if got := committedSince(ctx, ws, "backend/="+heads["backend"]+"\nfrontend/="+heads["frontend"]); len(got) != 1 || got[0] != "frontend/committed.txt" {
		t.Fatalf("committed since the turn began: %v", got)
	}
	_ = os.WriteFile(transcript, []byte(`{"kind":"prompt","body":"change both"}`+"\n"+`{"kind":"text","body":"Done."}`+"\n"), 0o600)
	runHook(t, d, "stop", sess)

	rows, err := st.DB().Query(`SELECT path FROM file_touch ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var touched []string
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		touched = append(touched, p)
	}
	if strings.Join(touched, ",") != "backend/by-shell.txt,frontend/committed.txt" {
		t.Fatalf("touches = %v", touched)
	}
}
