package tui

import (
	"os"
	"testing"
)

// /dev/null is a character device but no terminal: with it as stdin and
// stdout (cron, CI, a service) a command must not start a prompt or a bar.
func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal(os.DevNull + " was taken for a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) || isTerminal(w) {
		t.Fatal("a pipe was taken for a terminal")
	}
}
