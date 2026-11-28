package store

import (
	"context"
	"encoding/json"
)

// Queries for session tags and workspace commit links (ADR-0017). They read
// and update rows of schema v10 only: no column or table is added.

// TouchedPath is one path a session changed, as recorded.
type TouchedPath struct {
	Path      string // from the store's root with "/", or absolute when OutOfRepo
	OutOfRepo bool
}

// SessionFiles are the paths a session changed and the files of the commits
// linked to it, the rows a session's tags are derived from.
func (s *Store) SessionFiles(ctx context.Context, sessionID string) ([]TouchedPath, []string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT path, out_of_repo FROM file_touch WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, nil, err
	}
	var touched []TouchedPath
	for rows.Next() {
		var t TouchedPath
		if err := rows.Scan(&t.Path, &t.OutOfRepo); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		touched = append(touched, t)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT files_touched FROM checkpoint WHERE session_id = ? AND link_state = 'linked'`, sessionID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var committed []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, nil, err
		}
		var files []string
		if json.Unmarshal([]byte(raw), &files) == nil {
			committed = append(committed, files...)
		}
	}
	return touched, committed, rows.Err()
}

// ResyncSession sends a session that was sent once more: what it is tagged
// with may have grown, and the drain works its tags out again.
func (s *Store) ResyncSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_session SET `+resync+` WHERE id = ?`, id)
	return err
}

// ResyncSessions sends every session that was sent once more, so the drain
// works out its tags again: what the server stores for them depends on
// which Projects are in the workspace's cloud group (ADR-0018). It answers
// how many will go.
func (s *Store) ResyncSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_session SET `+resync+` WHERE sync_state = 'sent'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LiveTouchesUnder is LiveTouches limited to the paths under prefix
// ("backend/"), spelled as recorded: what a workspace's sessions changed in
// one of its member repositories.
func (s *Store) LiveTouchesUnder(ctx context.Context, workspaceID, prefix string) ([]Touch, error) {
	all, err := s.LiveTouches(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	var out []Touch
	for _, t := range all {
		if len(t.Path) > len(prefix) && t.Path[:len(prefix)] == prefix {
			out = append(out, t)
		}
	}
	return out, nil
}
