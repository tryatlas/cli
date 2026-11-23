package mcpconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// Removing the entry from files that held nothing else removes the files:
// "{}" left behind would be an untracked .mcp.json in every repository an
// agent was hooked in.
func TestRemoveLeavesNoEmptyFiles(t *testing.T) {
	root := t.TempDir()
	w, ok := For(claudeID)
	if !ok {
		t.Fatal("no Claude Code writer")
	}
	if _, err := w.Write(root); err != nil {
		t.Fatal(err)
	}
	mcp, local := filepath.Join(root, ".mcp.json"), filepath.Join(root, ".claude", "settings.local.json")
	for _, p := range []string{mcp, local} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("not written: %v", err)
		}
	}
	if err := w.Remove(root); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{mcp, local} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			b, _ := os.ReadFile(p)
			t.Fatalf("%s left behind: %q", p, b)
		}
	}
	// Removing again is a no-op, not an error.
	if err := w.Remove(root); err != nil {
		t.Fatal(err)
	}
}
