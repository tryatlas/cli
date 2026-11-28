package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeTokens struct{ n, invalidated int }

func (f *fakeTokens) Token(context.Context) (string, error) {
	f.n++
	return "jwt-" + string(rune('0'+f.n)), nil
}
func (f *fakeTokens) Invalidate() { f.invalidated++ }

func server(t *testing.T, h http.HandlerFunc) *Client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, &fakeTokens{})
}

func TestCreateOmitsAbsentFieldsAndAcceptsIDOrWorkspaceID(t *testing.T) {
	var body string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, `{"id":"ws_9"}`)
	})
	id, err := c.Create(context.Background(), CreateRequest{OrgID: "org_1", Slug: "backend", Visibility: "org"})
	if err != nil || id != "ws_9" {
		t.Fatalf("%q %v", id, err)
	}
	if body != `{"orgId":"org_1","slug":"backend","visibility":"org"}` {
		t.Fatalf("body = %s", body)
	}
}

func TestConnectBody(t *testing.T) {
	var body string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, `{"status":"connected","workspaceId":"ws_1","reason":"fingerprint"}`)
	})
	sha, u := "abc", "github.com/acme/backend"
	res, err := c.Connect(context.Background(), ConnectRequest{OrgID: "org_1", RootCommitSHA: &sha, GitURL: &u})
	if err != nil || res.Status != "connected" || res.WorkspaceID != "ws_1" || res.Reason != "fingerprint" {
		t.Fatalf("%+v %v", res, err)
	}
	if body != `{"orgId":"org_1","create":false,"rootCommitSha":"abc","gitUrl":"github.com/acme/backend"}` {
		t.Fatalf("body = %s", body)
	}
}

func TestRefreshesOnceOn401(t *testing.T) {
	calls := 0
	var auths []string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		auths = append(auths, r.Header.Get("Authorization"))
		if calls == 1 {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"workspaces":[{"id":"ws_1","slug":"backend"}]}`)
	})
	ws, err := c.Workspaces(context.Background(), "org 1")
	if err != nil || len(ws) != 1 || ws[0].Label() != "backend" {
		t.Fatalf("%+v %v", ws, err)
	}
	if calls != 2 || c.Tokens.(*fakeTokens).invalidated != 1 || auths[0] == auths[1] {
		t.Fatalf("calls=%d auths=%v", calls, auths)
	}
}

func TestErrorsAndConflicts(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workspaces":
			w.WriteHeader(409)
		case "/workspaces/slug-available":
			if r.URL.Query().Get("orgId") != "org_1" || r.URL.Query().Get("slug") != "x" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			w.WriteHeader(409)
		default:
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"error":{"code":"forbidden","message":"not a member"}}`)
		}
	})
	if _, err := c.Create(context.Background(), CreateRequest{OrgID: "o", Slug: "s", Visibility: "org"}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("create: %v", err)
	}
	if ok, err := c.SlugAvailable(context.Background(), "org_1", "x"); ok || err != nil {
		t.Fatalf("slug: %v %v", ok, err)
	}
	var e *Error
	if _, err := c.Connect(context.Background(), ConnectRequest{OrgID: "o"}); !errors.As(err, &e) || e.Code != "forbidden" || e.Status != 403 {
		t.Fatalf("connect: %v", err)
	}
}

func TestRegistryReadsGroupsAndAServerWithoutThem(t *testing.T) {
	answer := `{"workspaces":[{"id":"g","slug":"fleet","kind":"group","parentId":null},{"id":"a","slug":"api","kind":"workspace","parentId":"g"}],"groups":true}`
	c := server(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, answer) })
	reg, err := c.Registry(context.Background(), "org_1")
	if err != nil || !reg.Groups {
		t.Fatalf("%+v %v", reg, err)
	}
	g, _ := reg.Find("g")
	a, _ := reg.Find("a")
	if !g.IsGroup() || g.Parent() != "" || a.IsGroup() || a.Parent() != "g" {
		t.Fatalf("g=%+v a=%+v", g, a)
	}
	answer = `{"workspaces":[{"id":"a","slug":"api"}]}`
	reg, err = c.Registry(context.Background(), "org_1")
	a, _ = reg.Find("a")
	if err != nil || reg.Groups || a.IsGroup() || a.Parent() != "" {
		t.Fatalf("old server: %+v %v", reg, err)
	}
}

func TestUpdateSendsOnlyTheGroupFieldsGiven(t *testing.T) {
	var method, path, body string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, body = r.Method, r.URL.RequestURI(), string(b)
		_, _ = io.WriteString(w, `{"workspace":{"id":"ws_1","slug":"fleet","kind":"group"}}`)
	})
	group := "group"
	got, err := c.Update(context.Background(), "org_1", "ws_1", UpdateRequest{Kind: &group})
	if err != nil || !got.IsGroup() {
		t.Fatalf("%+v %v", got, err)
	}
	if method != http.MethodPatch || path != "/workspaces/ws_1?org=org_1" || body != `{"kind":"group"}` {
		t.Fatalf("%s %s %s", method, path, body)
	}
}

func TestCodeOfNamesTheServersRefusal(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"not_promotable","message":"no"}}`)
	})
	parent := "g"
	_, err := c.Update(context.Background(), "org_1", "ws_1", UpdateRequest{ParentID: &parent})
	if CodeOf(err) != "not_promotable" || CodeOf(errors.New("x")) != "" {
		t.Fatalf("%v", err)
	}
}

func TestUngroupSendsANullParent(t *testing.T) {
	b, err := json.Marshal(UpdateRequest{Ungroup: true})
	if err != nil || string(b) != `{"parentId":null}` {
		t.Fatalf("%s %v", b, err)
	}
}
