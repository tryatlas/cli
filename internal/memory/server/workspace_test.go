package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/memory/record"
	"github.com/tryatlas/cli/internal/project"
)

// A session in a member repository of a workspace remembers into the
// workspace's one memory, and leaves no .atlas/ in the repository (ADR-0015).
func TestWorkspaceMemberSharesTheWorkspaceMemory(t *testing.T) {
	base, _ := project.Canonical(t.TempDir())
	mother := filepath.Join(base, "acme-shop")
	backend := filepath.Join(mother, "backend")
	_ = os.MkdirAll(filepath.Join(backend, "src"), 0o755)
	if out, err := exec.Command("git", "-C", backend, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = os.WriteFile(filepath.Join(backend, "src", "ttl.go"), []byte("package src\n\n// TTL is fifteen minutes.\nconst TTL = 15\n"), 0o644)
	st, err := store.Open(mother, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutBinding(context.Background(), store.Binding{WorkspaceID: mother, Root: mother, Mode: store.ModeLocal, Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if err := project.SetWorkspace(mother, true); err != nil {
		t.Fatal(err)
	}

	srv := newServer()
	defer srv.Close()
	c := startBridge(t, NewLocalForwarder(srv, Grant{SessionID: "s-b", Agent: "claude-code", Cwd: backend}))
	handshake(c)
	out, errText := c.tool("memory_remember", map[string]any{"kind": "decision", "key": "ttl", "content": "Access tokens live 15 minutes",
		"evidence": []any{map[string]any{"path": "src/ttl.go", "lines": "3-4", "symbol": "TTL"}}})
	if errText != "" || out["outcome"] != "inserted" {
		t.Fatalf("remember: %v %s", out, errText)
	}
	if _, err := os.Stat(record.Path(mother)); err != nil {
		t.Fatalf("no record in the workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backend, ".atlas")); err == nil {
		t.Fatal("an .atlas/ was created in the member repository")
	}
	// The citation is spelled from the workspace folder.
	got, _ := c.tool("memory_why", map[string]any{"path": "src/ttl.go"})
	if target, _ := got["target"].(map[string]any); target["path"] != "backend/src/ttl.go" {
		t.Fatalf("why target: %v", got)
	}
}
