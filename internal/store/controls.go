package store

import (
	"blux/internal/core"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type Control struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	TurnID     string `json:"turn_id"`
	Generation int64  `json:"generation"`
	Kind       string `json:"kind"`
	Prompt     string `json:"prompt"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func scanControl(r scanner) (c Control, err error) {
	err = r.Scan(&c.ID, &c.TaskID, &c.TurnID, &c.Generation, &c.Kind, &c.Prompt, &c.Status, &c.Error)
	return
}

const controlColumns = "id,task_id,turn_id,generation,kind,prompt,status,error"

func (s *Store) RequestControl(ctx context.Context, taskID, turnID, key, kind, prompt string) (c Control, err error) {
	if key == "" || (kind != "stop" && kind != "steer") || kind == "steer" && prompt == "" {
		return c, core.ErrInvalidState
	}
	err = s.transaction(ctx, func(q querier) error {
		if err := taskVisible(ctx, q, taskID); err != nil {
			return err
		}
		c, e := scanControl(q.QueryRowContext(ctx, "SELECT "+controlColumns+" FROM controls WHERE id=?", taskID+":"+key))
		if e == nil {
			if c.TurnID != turnID || c.Kind != kind || c.Prompt != prompt {
				return core.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var status, adapter, session string
		var generation int64
		e = q.QueryRowContext(ctx, "SELECT u.status,u.generation,u.session_id,t.adapter FROM turns u JOIN tasks t ON t.id=u.task_id WHERE u.id=? AND u.task_id=?", turnID, taskID).Scan(&status, &generation, &session, &adapter)
		if errors.Is(e, sql.ErrNoRows) {
			return core.ErrNotFound
		}
		if e != nil {
			return e
		}
		if status != "queued" && status != "running" {
			return core.ErrConflict
		}
		if kind == "steer" && (status != "running" || adapter != "codex") {
			return fmt.Errorf("steer unavailable: %w", core.ErrInvalidState)
		}
		c = Control{ID: taskID + ":" + key, TaskID: taskID, TurnID: turnID, Generation: generation, Kind: kind, Prompt: prompt, Status: "pending"}
		if kind == "stop" && status == "queued" {
			p, _ := json.Marshal(map[string]string{"code": "user_stopped", "message": "你已停止排队中的任务。"})
			if _, e = appendEvent(ctx, q, core.EventInput{TaskID: taskID, SessionID: session, TurnID: turnID, Generation: generation, Type: "turn.failed", Payload: p}); e != nil {
				return e
			}
			if _, e = q.ExecContext(ctx, "UPDATE jobs SET status='failed',ended_at=? WHERE turn_id=?", core.Now(), turnID); e != nil {
				return e
			}
			c.Status = "applied"
		}
		_, e = q.ExecContext(ctx, "INSERT INTO controls(id,task_id,turn_id,generation,kind,prompt,status,created_at) VALUES(?,?,?,?,?,?,?,?)", c.ID, c.TaskID, c.TurnID, c.Generation, c.Kind, c.Prompt, c.Status, core.Now())
		return e
	})
	if err == nil {
		c, err = s.GetControl(ctx, taskID, key)
	}
	return
}
func (s *Store) GetControl(ctx context.Context, taskID, key string) (Control, error) {
	if err := taskVisible(ctx, s.db, taskID); err != nil {
		return Control{}, err
	}
	return scanControl(s.db.QueryRowContext(ctx, "SELECT "+controlColumns+" FROM controls WHERE id=?", taskID+":"+key))
}
func (s *Store) ClaimControl(ctx context.Context, turnID string) (*Control, error) {
	return s.claimControl(ctx, turnID, "")
}
func (s *Store) ClaimStop(ctx context.Context, turnID string) (*Control, error) {
	return s.claimControl(ctx, turnID, " AND kind IN ('stop','ticket_pause','ticket_cancel')")
}
func (s *Store) ClaimSteer(ctx context.Context, turnID string) (*Control, error) {
	return s.claimControl(ctx, turnID, " AND kind='steer'")
}
func (s *Store) claimControl(ctx context.Context, turnID, filter string) (c *Control, err error) {
	err = s.transaction(ctx, func(q querier) error {
		v, e := scanControl(q.QueryRowContext(ctx, "SELECT "+controlColumns+" FROM controls WHERE turn_id=? AND status='pending'"+filter+" ORDER BY created_at,id LIMIT 1", turnID))
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, "UPDATE controls SET status='claimed' WHERE id=?", v.ID); e != nil {
			return e
		}
		c = &v
		return nil
	})
	return
}
func (s *Store) FinishControl(ctx context.Context, id string, cause error) error {
	status, msg := "applied", ""
	if cause != nil {
		status, msg = "rejected", cause.Error()
	}
	_, err := s.db.ExecContext(ctx, "UPDATE controls SET status=?,error=? WHERE id=? AND status='claimed'", status, msg, id)
	return err
}

// RecoverRunning never re-dispatches a turn whose external effects are unknown.
// A new user-authored turn can resume its persisted native thread and worktree.
func (s *Store) RecoverRunning(ctx context.Context) error {
	return s.transaction(ctx, func(q querier) error {
		rows, err := q.QueryContext(ctx, "SELECT task_id,session_id,turn_id,generation FROM jobs WHERE status='running'")
		if err != nil {
			return err
		}
		var jobs []core.Job
		for rows.Next() {
			var j core.Job
			if err = rows.Scan(&j.TaskID, &j.SessionID, &j.TurnID, &j.Generation); err != nil {
				rows.Close()
				return err
			}
			jobs = append(jobs, j)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, j := range jobs {
			var captured bool
			exitErr := confirmTicketExit(ctx, q, j.TaskID)
			if exitErr != nil && !errors.Is(exitErr, core.ErrConflict) {
				return exitErr
			}
			if err = q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM snapshots WHERE capture_key=? AND task_id=? AND generation=?)", j.TurnID, j.TaskID, j.Generation).Scan(&captured); err != nil {
				return err
			}
			if captured && exitErr == nil {
				if _, err = appendEvent(ctx, q, core.EventInput{TaskID: j.TaskID, SessionID: j.SessionID, TurnID: j.TurnID, Generation: j.Generation, Type: "turn.completed"}); err != nil {
					return err
				}
				if _, err = q.ExecContext(ctx, "UPDATE jobs SET status='completed',ended_at=? WHERE turn_id=?", core.Now(), j.TurnID); err != nil {
					return err
				}
				continue
			}
			p := json.RawMessage(`{"code":"recovery_required","message":"上次 Worker 意外退出。已保留会话与文件，请检查结果后提交新回合继续；不会自动重复执行。"}`)
			if exitErr == nil {
				var controlID, kind string
				err = q.QueryRowContext(ctx, "SELECT id,kind FROM controls WHERE turn_id=? AND status IN ('pending','claimed') AND kind IN ('ticket_pause','ticket_cancel','stop') ORDER BY created_at,id LIMIT 1", j.TurnID).Scan(&controlID, &kind)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if err == nil {
					if kind == "stop" {
						kind = "user_stopped"
					}
					p, _ = json.Marshal(map[string]string{"code": kind, "message": "已确认原执行进程退出，工作区已保留。"})
					if _, err = q.ExecContext(ctx, "UPDATE controls SET status='applied' WHERE id=?", controlID); err != nil {
						return err
					}
				}
			}
			if _, err = appendEvent(ctx, q, core.EventInput{TaskID: j.TaskID, SessionID: j.SessionID, TurnID: j.TurnID, Generation: j.Generation, Type: "turn.failed", Payload: p}); err != nil {
				return err
			}
		}
		_, err = q.ExecContext(ctx, "UPDATE jobs SET status='failed',ended_at=? WHERE status='running'", core.Now())
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, "UPDATE controls SET status='rejected',error='执行已结束或 Worker 中断；控制操作未重放' WHERE status IN ('pending','claimed') AND turn_id NOT IN (SELECT turn_id FROM jobs WHERE status='queued')")
		return err
	})
}

// The CLI can finish and commit its snapshot just before cancellation or loss
// of the completion response. That capture identifies the successful turn.
func (s *Store) FinishFailedTurn(ctx context.Context, j core.Job, payload json.RawMessage) error {
	var status string
	var captured bool
	if err := s.db.QueryRowContext(ctx, `SELECT u.status,EXISTS(SELECT 1 FROM snapshots s
        WHERE s.capture_key=u.id AND s.generation=u.generation AND s.task_id=u.task_id)
        FROM turns u WHERE u.id=? AND u.generation=? AND u.task_id=?`, j.TurnID, j.Generation, j.TaskID).Scan(&status, &captured); err != nil {
		return err
	}
	if core.IsTerminal(status) {
		return nil
	}
	kind := "turn.failed"
	if captured {
		err := confirmTicketExit(ctx, s.db, j.TaskID)
		if err != nil && !errors.Is(err, core.ErrConflict) {
			return err
		}
		if err == nil {
			kind, payload = "turn.completed", json.RawMessage(`{}`)
		}
	}
	_, _, err := s.AppendEvent(ctx, core.EventInput{TaskID: j.TaskID, SessionID: j.SessionID, TurnID: j.TurnID, Generation: j.Generation, Type: kind, Payload: payload})
	return err
}
