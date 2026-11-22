package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tryatlas/cli/internal/embed"
)

// A model that cannot be downloaded (offline, a proxy, Hugging Face out of
// reach) leaves keyword search: the index is still built and the command
// succeeds, saying why.
func TestIndexFallsBackToKeywordsWhenTheDownloadFails(t *testing.T) {
	gitRepo(t)
	if code, _, e := runCmd(t, "init", "--local"); code != 0 {
		t.Fatal(e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no route to the model", http.StatusBadGateway)
	}))
	defer srv.Close()
	base := embed.BaseURL
	embed.BaseURL = srv.URL + "/"
	t.Setenv("ATLAS_MODELS_DIR", t.TempDir())
	t.Setenv("ATLAS_EMBED", "cpu")
	embed.Reset()
	t.Cleanup(func() { embed.BaseURL = base; embed.Reset() })

	code, out, e := runCmd(t, "memory", "index")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	for _, want := range []string{"Memory index ready", "could not be downloaded", "502", "keyword search only"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %s", want, out)
		}
	}
	if embed.Available() {
		t.Error("a failed download left a model")
	}
}
