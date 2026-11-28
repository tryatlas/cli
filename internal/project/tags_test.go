package project

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tryatlas/cli/internal/capture/store"
)

func TestReposOfAWorkspace(t *testing.T) {
	mother, frontend, backend := boundWorkspace(t)
	if got := Repos(backend); len(got) != 1 || got[0] != (Repo{Dir: backend}) {
		t.Fatalf("a member's own repos: %+v", got)
	}
	// docs/ has no git: it is a member, but no repository.
	if _, err := AddFolder(mother, filepath.Join(mother, "docs")); err != nil {
		t.Fatal(err)
	}
	want := []Repo{{Dir: backend, Prefix: "backend/"}, {Dir: frontend, Prefix: "frontend/"}}
	if got := Repos(mother); !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace repos %+v, want %+v", got, want)
	}

	got := EachRepo(Repos(mother), func(r Repo) string { return r.Prefix })
	if !reflect.DeepEqual(got, []string{"backend/", "frontend/"}) {
		t.Fatalf("EachRepo out of order: %v", got)
	}
}

func TestHeadsRoundTrip(t *testing.T) {
	single := []Repo{{Dir: "/r"}}
	if got := Heads(single, []string{"abc"}); got != "abc" {
		t.Fatalf("a single repository's head: %q", got)
	}
	if m := SplitHeads("abc"); m[""] != "abc" || len(m) != 1 {
		t.Fatalf("split a single head: %v", m)
	}
	ws := []Repo{{Dir: "/w/a=b", Prefix: "a=b/"}, {Dir: "/w/front", Prefix: "front/"}, {Dir: "/w/new", Prefix: "new/"}}
	s := Heads(ws, []string{"111", "222", ""})
	if s != "a=b/=111\nfront/=222" {
		t.Fatalf("workspace heads: %q", s)
	}
	if m := SplitHeads(s); m["a=b/"] != "111" || m["front/"] != "222" || len(m) != 2 {
		t.Fatalf("split workspace heads: %v", m)
	}
	if len(SplitHeads("")) != 0 {
		t.Fatal("no heads")
	}
}

// cloudBind binds dir to org with a cloud Workspace id.
func cloudBind(t *testing.T, dir, org, id string) {
	t.Helper()
	s, err := store.Open(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := canon(t, dir)
	if err := s.BindCloud(context.Background(), store.Binding{WorkspaceID: c, Root: c, OrgID: &org, RemoteWorkspaceID: &id, Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func folders(tags []Tag) string {
	var out []string
	for _, tg := range tags {
		id := "-"
		if tg.WorkspaceID != nil {
			id = *tg.WorkspaceID
		}
		var rs []string
		for _, r := range tg.Reasons {
			rs = append(rs, string(r))
		}
		out = append(out, tg.Folder+"("+id+":"+strings.Join(rs, ",")+")")
	}
	return strings.Join(out, " ")
}

// The TRD's scenarios S1–S7: acme-shop with the member Projects backend and
// frontend in the cloud, mobile/ not bound yet, infra/ bound on its own.
func TestTagsOfTheScenarios(t *testing.T) {
	ctx := context.Background()
	mother, frontend, backend := newWorkspace(t)
	mobile, infra := filepath.Join(mother, "mobile"), filepath.Join(mother, "infra")
	for _, d := range []string{mobile, infra} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, d, "init", "-q")
	}
	cloudBind(t, mother, "org_1", "ws_shop")
	if err := SetWorkspace(mother, true); err != nil {
		t.Fatal(err)
	}
	cloudBind(t, backend, "org_1", "ws_backend")
	cloudBind(t, frontend, "org_1", "ws_frontend")
	cloudBind(t, infra, "org_1", "ws_infra")
	for _, d := range []string{backend, frontend} {
		if err := Join(d, mother); err != nil {
			t.Fatal(err)
		}
	}
	m := canon(t, mother)
	touched := func(paths ...string) []store.TouchedPath {
		var out []store.TouchedPath
		for _, p := range paths {
			out = append(out, store.TouchedPath{Path: p, OutOfRepo: filepath.IsAbs(p)})
		}
		return out
	}

	ws := LayoutOf(ctx, m)
	if ws == nil || ws.Root != m || ws.Home != m {
		t.Fatalf("workspace layout: %+v", ws)
	}
	for _, c := range []struct {
		name      string
		changed   []store.TouchedPath
		committed []string
		want      string
	}{
		{"S1 a chat", nil, nil, ""},
		{"S2 two members", touched("frontend/src/App.tsx", "backend/api/user.go", "backend/api/user_test.go"), nil,
			"backend(ws_backend:edited) frontend(ws_frontend:edited)"},
		{"S5 edited and committed", touched("backend/api/user.go"), []string{"backend/api/user.go"},
			"backend(ws_backend:edited,committed)"},
		{"S6 the workspace folder itself", touched("README.md"), nil, ""},
		{"S7 a folder not bound yet", touched("mobile/app.kt"), nil, "mobile(-:edited)"},
		{"a repository bound on its own", touched("infra/main.tf"), nil, ""},
		{"outside the workspace", touched("/etc/hosts"), nil, ""},
	} {
		if got := folders(ws.Tags(c.changed, c.committed)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	member := LayoutOf(ctx, canon(t, backend))
	if member == nil || member.Root != m || member.Home != canon(t, backend) {
		t.Fatalf("member layout: %+v", member)
	}
	for _, c := range []struct {
		name    string
		changed []store.TouchedPath
		want    string
	}{
		{"S3 its own files", touched("api/user.go"), ""},
		{"S4 a sibling's", touched("api/user.go", filepath.Join(frontend, "src", "App.tsx")), "frontend(ws_frontend:edited)"},
		{"a deleted sibling file", touched(filepath.Join(frontend, "gone", "old.ts")), "frontend(ws_frontend:edited)"},
	} {
		if got := folders(member.Tags(c.changed, nil)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	// A member in another organisation is tagged by folder only.
	cloudBind(t, frontend, "org_2", "ws_elsewhere")
	if got := folders(LayoutOf(ctx, m).Tags(touched("frontend/x.ts"), nil)); got != "frontend(-:edited)" {
		t.Fatalf("a member in another organisation: %q", got)
	}
	// A plain Project has no layout, so no tags.
	if l := LayoutOf(ctx, canon(t, infra)); l != nil {
		t.Fatalf("plain Project layout: %+v", l)
	}
}
