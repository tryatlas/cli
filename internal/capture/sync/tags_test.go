package sync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/project"
)

// postedSession is the agent_session a drain posted.
func postedSession(t *testing.T, f *fake) map[string]json.RawMessage {
	t.Helper()
	for _, c := range f.calls {
		if c.path != "/ingest" {
			continue
		}
		var body struct {
			Artifacts []map[string]json.RawMessage `json:"artifacts"`
		}
		if err := json.Unmarshal(c.body, &body); err != nil {
			t.Fatal(err)
		}
		for _, a := range body.Artifacts {
			if string(a["type"]) == `"agent_session"` {
				return a
			}
		}
	}
	t.Fatal("no agent_session posted")
	return nil
}

// ADR-0017 (FR-8): a workspace session that changed files in a member
// Project goes out with that Project in its projects; a plain Project's
// sessions carry no projects field at all.
func TestASessionGoesOutWithItsTags(t *testing.T) {
	ctx := context.Background()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	ws, backend := filepath.Join(base, "acme-shop"), filepath.Join(base, "acme-shop", "backend")
	if err := os.MkdirAll(filepath.Join(backend, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	bindCloud := func(dir, id string) *store.Store {
		st, err := store.Open(dir, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if err := st.BindCloud(ctx, store.Binding{WorkspaceID: dir, Root: dir, Enabled: true, OrgID: ptr("org-1"), RemoteWorkspaceID: ptr(id)}, t0); err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := bindCloud(ws, "ws-shop")
	bindCloud(backend, "ws-backend")
	if err := project.SetWorkspace(ws, true); err != nil {
		t.Fatal(err)
	}
	if err := project.Join(backend, ws); err != nil {
		t.Fatal(err)
	}
	sid, err := st.UpsertSession(ctx, store.SessionKey{WorkspaceID: ws, Source: "native", NativeID: "n-1"}, ptr("claude-code"), nil, ptr(ws), nil, t0)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.BeginTurn(ctx, sid, 1, t0)
	call, _, err := st.UpsertToolCall(ctx, store.ToolCall{SessionID: sid, TurnSeq: 1, NativeID: "tu1", ToolName: "Write", Status: "completed"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"backend/api/user.go", "README.md"} {
		if err := st.AddFileTouch(ctx, store.FileTouch{ToolCallID: call, SessionID: sid, TurnSeq: 1, Path: p}, t0); err != nil {
			t.Fatal(err)
		}
	}
	f := &fake{}
	if rep := drain(t, st, f, &tokens{}); rep.Outcome != Done {
		t.Fatalf("%+v", rep)
	}
	a := postedSession(t, f)
	if got := string(a["projects"]); got != `[{"workspaceId":"ws-backend","folder":"backend","reasons":["edited"]}]` {
		t.Fatalf("projects %s", got)
	}
	if string(a["workspaceId"]) != `"ws-shop"` {
		t.Fatalf("home %s", a["workspaceId"])
	}

	// A plain Project: no projects field.
	plain, _ := cloudStore(t)
	g := &fake{}
	drain(t, plain, g, &tokens{})
	if _, ok := postedSession(t, g)["projects"]; ok {
		t.Fatal("a plain Project's session carries projects")
	}
}
