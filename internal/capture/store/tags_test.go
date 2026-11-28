package store

import (
	"context"
	"reflect"
	"testing"
)

func TestSessionFilesAndResync(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, ModeCloud)
	sid, err := s.UpsertSession(ctx, SessionKey{WorkspaceID: s.Root, Source: "native", NativeID: "n-1"}, str("claude-code"), nil, nil, nil, t0)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.BeginTurn(ctx, sid, 1, t0)
	call, _, err := s.UpsertToolCall(ctx, ToolCall{SessionID: sid, TurnSeq: 1, NativeID: "tu1", ToolName: "Write", Status: "completed"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ft := range []FileTouch{
		{Path: "backend/api/user.go"},
		{Path: "backend/api/user.go"}, // touched twice, listed once
		{Path: "/elsewhere/notes.md", OutOfRepo: true},
		{Path: "frontend/App.tsx", Deleted: true},
	} {
		ft.ToolCallID, ft.SessionID, ft.TurnSeq = call, sid, 1
		if err := s.AddFileTouch(ctx, ft, t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertCheckpoint(ctx, Checkpoint{SessionID: sid, CommitSHA: "c1", FilesTouched: []string{"backend/api/user.go"}}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCheckpoint(ctx, Checkpoint{SessionID: sid, CommitSHA: "c2", FilesTouched: []string{"frontend/App.tsx"}}, t0); err != nil {
		t.Fatal(err)
	}
	cps, _ := s.ProjectCheckpoints(ctx, s.Root)
	for _, c := range cps {
		if c.CommitSHA == "c2" {
			if err := s.SetLinkState(ctx, c.ID, "orphaned"); err != nil { // an orphan carries no tag
				t.Fatal(err)
			}
		}
		if c.CommitSHA == "c1" && !reflect.DeepEqual(c.Files, []string{"backend/api/user.go"}) {
			t.Fatalf("checkpoint files %v", c.Files)
		}
	}

	touched, committed, err := s.SessionFiles(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 3 {
		t.Fatalf("touched %+v", touched)
	}
	outOfRepo := 0
	for _, tp := range touched {
		if tp.OutOfRepo {
			outOfRepo++
		}
	}
	if outOfRepo != 1 || !reflect.DeepEqual(committed, []string{"backend/api/user.go"}) {
		t.Fatalf("touched %+v committed %v", touched, committed)
	}

	// Resync sends a sent session again and leaves a pending one alone.
	if _, err := s.db.Exec(`UPDATE agent_session SET sync_state = 'sent' WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	if err := s.ResyncSession(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if got := one[string](t, s, `SELECT sync_state FROM agent_session WHERE id = ?`, sid); got != SyncPending {
		t.Fatalf("after resync: %s", got)
	}
	// ResyncSessions does the same for every sent session, and only those.
	if _, err := s.db.Exec(`UPDATE agent_session SET sync_state = 'sent' WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ResyncSessions(ctx); err != nil || n != 1 {
		t.Fatalf("resync all: %d %v", n, err)
	}
	if n, _ := s.ResyncSessions(ctx); n != 0 {
		t.Fatalf("a pending session was counted: %d", n)
	}

	under, err := s.LiveTouchesUnder(ctx, s.Root, "backend/")
	if err != nil {
		t.Fatal(err)
	}
	if len(under) != 2 || under[0].Path != "backend/api/user.go" {
		t.Fatalf("touches under backend/: %+v", under)
	}
	if none, _ := s.LiveTouchesUnder(ctx, s.Root, "back/"); len(none) != 0 {
		t.Fatalf("a prefix is a whole folder: %+v", none)
	}
}
