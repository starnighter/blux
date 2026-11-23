package store

import (
	"blux/internal/core"
	"context"
)

func (s *Store) SaveRevision(ctx context.Context, v Snapshot) error {
	return s.transaction(ctx, func(q querier) error {
		if err := taskVisible(ctx, q, v.TaskID); err != nil {
			return err
		}
		var editable bool
		err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=? AND status='completed' AND current_turn_id=?
			AND (SELECT id FROM snapshots WHERE task_id=tasks.id ORDER BY rowid DESC LIMIT 1)=?)`,
			v.TaskID, v.TurnID, v.ParentSnapshotID).Scan(&editable)
		if err != nil {
			return err
		}
		if !editable {
			return core.ErrConflict
		}
		return insertSnapshot(ctx, q, v, v.ParentSnapshotID)
	})
}

type LineComment struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Side      string `json:"side"`
	Line      int    `json:"line"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) LineComments(ctx context.Context, snapshot, kind, path string) ([]LineComment, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,kind,path,side,line,body,created_at FROM snapshot_comments WHERE snapshot_id=? AND kind=? AND path=? ORDER BY rowid", snapshot, kind, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []LineComment{}
	for rows.Next() {
		var c LineComment
		if err := rows.Scan(&c.ID, &c.Kind, &c.Path, &c.Side, &c.Line, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

func (s *Store) AddLineComment(ctx context.Context, snapshot string, c LineComment) (LineComment, error) {
	c.ID, c.CreatedAt = core.NewID("comment"), core.Now()
	_, err := s.db.ExecContext(ctx, "INSERT INTO snapshot_comments(id,snapshot_id,kind,path,side,line,body,created_at) VALUES(?,?,?,?,?,?,?,?)", c.ID, snapshot, c.Kind, c.Path, c.Side, c.Line, c.Body, c.CreatedAt)
	return c, err
}
