package record

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/project"
)

// ADR-0017 (FR-9): a shell call in a workspace folder records what it
// changed in each member repository, spelled with the member's folder; the
// workspace folder alone is no repository and would record nothing.
func TestAShellCallInAWorkspaceRecordsEachMembersChanges(t *testing.T) {
	ctx := context.Background()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	ws := filepath.Join(base, "acme-shop")
	backend := filepath.Join(ws, "backend")
	if err := os.MkdirAll(backend, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "base"}} {
		c := exec.Command("git", append([]string{"-C", backend, "-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	st, err := store.Open(ws, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.PutBinding(ctx, store.Binding{WorkspaceID: ws, Root: ws, Mode: store.ModeLocal, Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := project.SetWorkspace(ws, true); err != nil {
		t.Fatal(err)
	}
	r := New(st, ws)
	s := Session{Agent: "claude-code", NativeID: "s-1", Cwd: ws}
	if _, err := r.Prompt(ctx, s, "generate the client"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"command": "make gen"})
	if err := r.ToolCall(ctx, s, Tool{NativeID: "b1", Name: "Bash", Status: "running", Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backend, "gen.go"), []byte("package gen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.ToolCall(ctx, s, Tool{NativeID: "b1", Name: "Bash", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	var path string
	var existed bool
	if err := st.DB().QueryRow(`SELECT path, existed_before FROM file_touch`).Scan(&path, &existed); err != nil {
		t.Fatal(err)
	}
	if path != "backend/gen.go" || existed {
		t.Fatalf("touch %q existed=%v", path, existed)
	}
}
