package store

import (
	"blux/internal/agent"
	"blux/internal/platform"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"blux/internal/core"
)

type Ticket struct {
	ID                 string    `json:"id"`
	TaskID             string    `json:"task_id"`
	Goal               string    `json:"goal"`
	Dependencies       []string  `json:"dependencies"`
	AcceptanceCriteria string    `json:"acceptance_criteria"`
	Status             string    `json:"status"`
	Attempts           []Attempt `json:"attempts"`
}

type Attempt struct {
	Policy        agent.Policy `json:"policy"`
	ID            string       `json:"id"`
	Number        int          `json:"number"`
	Adapter       string       `json:"adapter"`
	Status        string       `json:"status"`
	WorkspacePath string       `json:"workspace_path"`
	CreatedAt     string       `json:"created_at"`
	StartedAt     string       `json:"started_at"`
	EndedAt       string       `json:"ended_at"`
	ErrorCode     string       `json:"error_code"`
	Error         string       `json:"error"`
	FailureClass  string       `json:"failure_class"`
	SafeToRetry   bool         `json:"safe_to_retry"`
	SnapshotID    string       `json:"snapshot_id"`
	TurnIDs       []string     `json:"turn_ids"`
}

type TicketAction struct {
	Action            string `json:"action"`
	ExpectedAttemptID string `json:"expected_attempt_id"`
	Adapter           string `json:"adapter,omitempty"`
	Automatic         bool   `json:"-"`
}

type EngineHealth struct {
	Status       string `json:"status"`
	FailureClass string `json:"failure_class,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

func (s *Store) EngineHealth(ctx context.Context) (map[string]EngineHealth, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT adapter,status,failure_class,error_code,updated_at FROM engine_health")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]EngineHealth{}
	for rows.Next() {
		var id string
		var h EngineHealth
		if err = rows.Scan(&id, &h.Status, &h.FailureClass, &h.ErrorCode, &h.UpdatedAt); err != nil {
			return nil, err
		}
		result[id] = h
	}
	return result, rows.Err()
}

func (s *Store) Ticket(ctx context.Context, id string) (Ticket, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Ticket{}, err
	}
	defer tx.Rollback()
	t, err := readTicket(ctx, tx, id)
	if err != nil {
		return t, err
	}
	return t, tx.Commit()
}

func readTicket(ctx context.Context, q querier, id string) (Ticket, error) {
	t := Ticket{ID: id, TaskID: id, Attempts: []Attempt{}}
	var deps string
	err := q.QueryRowContext(ctx, "SELECT goal,dependencies,acceptance_criteria,status FROM tickets WHERE id=? AND id NOT IN (SELECT task_id FROM deleted_tasks)", id).Scan(&t.Goal, &deps, &t.AcceptanceCriteria, &t.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Ticket{}, core.ErrNotFound
	}
	if err != nil {
		return Ticket{}, err
	}
	if err = json.Unmarshal([]byte(deps), &t.Dependencies); err != nil {
		return Ticket{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT a.id,a.number,a.adapter,a.status,a.workspace_path,a.created_at,a.started_at,a.ended_at,
      a.error_code,a.error,a.failure_class,a.safe_to_retry,a.snapshot_id,a.policy_json,COALESCE(u.id,'')
      FROM ticket_attempts a LEFT JOIN attempt_turns l ON l.attempt_id=a.id LEFT JOIN turns u ON u.id=l.turn_id
      WHERE a.ticket_id=? ORDER BY a.number,u.created_seq`, id)
	if err != nil {
		return Ticket{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Attempt
		var turn, policy string
		if err = rows.Scan(&a.ID, &a.Number, &a.Adapter, &a.Status, &a.WorkspacePath, &a.CreatedAt, &a.StartedAt, &a.EndedAt, &a.ErrorCode, &a.Error, &a.FailureClass, &a.SafeToRetry, &a.SnapshotID, &policy, &turn); err != nil {
			return Ticket{}, err
		}
		if len(t.Attempts) == 0 || t.Attempts[len(t.Attempts)-1].ID != a.ID {
			if a.FailureClass == "" && a.ErrorCode != "" {
				a.FailureClass = failureClass(a.ErrorCode)
			}
			if err = json.Unmarshal([]byte(policy), &a.Policy); err != nil {
				return Ticket{}, err
			}
			a.TurnIDs = []string{}
			t.Attempts = append(t.Attempts, a)
		}
		if turn != "" {
			last := &t.Attempts[len(t.Attempts)-1]
			last.TurnIDs = append(last.TurnIDs, turn)
		}
	}
	return t, rows.Err()
}

func createTicket(ctx context.Context, q querier, id string, in core.CreateTaskInput) error {
	deps := in.TicketDependencies
	if deps == nil {
		deps = []string{}
	}
	b, err := json.Marshal(deps)
	if err != nil {
		return err
	}
	goal := in.TicketGoal
	if goal == "" {
		goal = in.Prompt
	}
	if _, err = q.ExecContext(ctx, "INSERT INTO tickets(id,goal,dependencies,acceptance_criteria,status) VALUES(?,?,?,?,'queued')", id, goal, string(b), in.AcceptanceCriteria); err != nil {
		return err
	}
	return newAttempt(ctx, q, id, in.Adapter, 1, "")
}

func newAttempt(ctx context.Context, q querier, id, adapter string, number int, path string) error {
	policy, err := json.Marshal(agent.ExecutionPolicy(adapter))
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO ticket_attempts(id,ticket_id,number,adapter,status,created_at,workspace_path,policy_json) VALUES(?,?,?,?,'queued',?,?,?)", core.NewID("attempt"), id, number, adapter, core.Now(), path, string(policy))
	return err
}

func continueTicket(ctx context.Context, q querier, id string) error {
	var status, adapter, path, code string
	var number int
	err := q.QueryRowContext(ctx, "SELECT status,adapter,workspace_path,number,error_code FROM ticket_attempts WHERE ticket_id=? ORDER BY number DESC LIMIT 1", id).Scan(&status, &adapter, &path, &number, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} // Manager conversations have no worker ticket.
	if err != nil {
		return err
	}
	switch status {
	case "completed":
		return nil
	case "failed", "recovery_required":
		if err := confirmTicketExit(ctx, q, id); err != nil {
			return err
		}
		return newAttempt(ctx, q, id, adapter, number+1, path)
	case "paused":
		if code == "user_stopped" {
			if err := confirmTicketExit(ctx, q, id); err != nil {
				return err
			}
			return newAttempt(ctx, q, id, adapter, number+1, path)
		}
	}
	return core.ErrConflict
}

func confirmTicketExit(ctx context.Context, q querier, id string) error {
	var root, worktree string
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(json_extract(runtime_json,'$.workspace_path'),''),COALESCE(json_extract(runtime_json,'$.worktree_path'),'') FROM sessions WHERE task_id=?", id).Scan(&root, &worktree); err != nil {
		return err
	}
	if root == "" {
		return nil
	}
	if err := platform.ConfirmStopped(filepath.Join(root, ".agent")); err != nil {
		return &core.ExecutionError{Code: "ticket_process_unconfirmed", Message: "尚未确认上次执行进程已退出，请检查后重试。", Err: core.ErrConflict}
	}
	if worktree != "" {
		info, err := os.Stat(worktree)
		if err != nil || !info.IsDir() {
			return &core.ExecutionError{Code: "ticket_workspace_missing", Message: "上次执行的工作区已丢失或无法访问，请恢复该目录后重试。", Err: core.ErrConflict}
		}
	}

	return nil
}
func validTicketAdapter(id string) bool {
	switch id {
	case "codex", "claude", "deepseek", "gemini":
		return true
	}
	return false
}

func (s *Store) ActOnTicket(ctx context.Context, id, key string, action TicketAction) (Ticket, error) {
	if key == "" || action.ExpectedAttemptID == "" || action.Adapter != "" && (action.Action != "retry" && action.Action != "resume" || !validTicketAdapter(action.Adapter)) {
		return Ticket{}, core.ErrInvalidState
	}
	hash := digest(struct {
		Action    TicketAction
		Automatic bool
	}{action, action.Automatic})
	var result Ticket
	err := s.transaction(ctx, func(q querier) error {
		if err := taskVisible(ctx, q, id); err != nil {
			return err
		}
		var storedHash, raw string
		err := q.QueryRowContext(ctx, "SELECT digest,result_json FROM ticket_actions WHERE ticket_id=? AND key=?", id, key).Scan(&storedHash, &raw)
		if err == nil {
			if storedHash != hash {
				return core.ErrConflict
			}
			return json.Unmarshal([]byte(raw), &result)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		t, err := readTicket(ctx, q, id)
		if err != nil {
			return err
		}
		a := t.Attempts[len(t.Attempts)-1]
		if a.ID != action.ExpectedAttemptID {
			return core.ErrStaleGeneration
		}
		turnID := a.TurnIDs[len(a.TurnIDs)-1]
		var session, prompt string
		var generation int64
		if err = q.QueryRowContext(ctx, "SELECT session_id,prompt,generation FROM turns WHERE id=?", turnID).Scan(&session, &prompt, &generation); err != nil {
			return err
		}
		switch action.Action {
		case "resume", "retry":
			if action.Action == "resume" && t.Status != "paused" || action.Action == "retry" && t.Status != "failed" && t.Status != "recovery_required" {
				return core.ErrConflict
			}
			var superseded bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_tasks w
                WHERE w.task_id=? AND NOT EXISTS(SELECT 1 FROM deliveries d,json_each(d.state_json,'$.run.phases') p,
                json_each(p.value,'$.attempt.units') u WHERE d.id=w.delivery_id AND json_extract(u.value,'$.task_id')=w.task_id))`, id).Scan(&superseded); err != nil {
				return err
			}
			if superseded {
				return &core.ExecutionError{Code: "ticket_superseded", Message: "这个工单已被当前交付流程替换。", Err: core.ErrConflict}
			}
			if err := confirmTicketExit(ctx, q, id); err != nil {
				return err
			}
			if action.Automatic && !a.SafeToRetry {
				return core.ErrConflict
			}
			adapter := action.Adapter
			if adapter == "" {
				adapter = a.Adapter
			}
			if err = newAttempt(ctx, q, id, adapter, a.Number+1, a.WorkspacePath); err != nil {
				return err
			}
			next := core.NewID("turn")
			if adapter != a.Adapter {
				p, _ := json.Marshal(map[string]string{"adapter": adapter})
				if _, err = appendEvent(ctx, q, core.EventInput{TaskID: id, SessionID: session, TurnID: next, Generation: generation + 1, Type: "ticket.engine_switched", Payload: p}); err != nil {
					return err
				}
			}
			if err = queueTurn(ctx, q, id, session, next, prompt, generation+1); err != nil {
				return err
			}
			_, err = q.ExecContext(ctx, "INSERT INTO turn_context(turn_id,task_id,snapshot_json) SELECT ?,task_id,snapshot_json FROM turn_context WHERE turn_id=?", next, turnID)
		case "pause", "cancel":
			if t.Status == "paused" && action.Action == "cancel" {
				if _, err = q.ExecContext(ctx, "UPDATE tickets SET status='cancelled' WHERE id=?", id); err != nil {
					return err
				}
				break
			}
			if t.Status != "running" && t.Status != "queued" {
				return core.ErrConflict
			}
			kind, pending := "ticket_pause", "pausing"
			if action.Action == "cancel" {
				kind, pending = "ticket_cancel", "cancelling"
			}
			if t.Status == "queued" {
				p, _ := json.Marshal(map[string]string{"code": kind, "message": "工单在排队时已停止。"})
				if _, err = appendEvent(ctx, q, core.EventInput{TaskID: id, SessionID: session, TurnID: turnID, Generation: generation, Type: "turn.failed", Payload: p}); err != nil {
					return err
				}
				_, err = q.ExecContext(ctx, "UPDATE jobs SET status='failed',ended_at=? WHERE turn_id=? AND status='queued'", core.Now(), turnID)
				break
			}
			if _, err = q.ExecContext(ctx, "UPDATE tickets SET status=? WHERE id=?", pending, id); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, "UPDATE ticket_attempts SET status=? WHERE id=?", pending, a.ID); err != nil {
				return err
			}
			_, err = q.ExecContext(ctx, "INSERT INTO controls(id,task_id,turn_id,generation,kind,status,created_at) VALUES(?,?,?,?,?,'pending',?)", core.NewID("control"), id, turnID, generation, kind, core.Now())
		default:
			return core.ErrInvalidState
		}
		if err != nil {
			return err
		}
		result, err = readTicket(ctx, q, id)
		if err != nil {
			return err
		}
		b, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, "INSERT INTO ticket_actions(ticket_id,key,digest,result_json) VALUES(?,?,?,?)", id, key, hash, string(b))
		return err
	})
	return result, err
}

// MarkTurnDispatched is durable before Send: after this point a lost response
// cannot prove that the external CLI did not change files or run a command.
func (s *Store) MarkTurnDispatched(ctx context.Context, turn string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE attempt_turns SET dispatched=1 WHERE turn_id=?", turn)
	return err
}

func failureClass(code string) string {
	switch {
	case code == "":
		return ""
	case strings.Contains(code, "quota") || strings.Contains(code, "usage_limit") || strings.Contains(code, "rate_limit"):
		return "quota"
	case strings.Contains(code, "auth_required"):
		return "authentication"
	case strings.HasSuffix(code, "not_found") && !strings.HasPrefix(code, "repository") || strings.Contains(code, "unsupported_version") || strings.Contains(code, "protocol_unsupported") || strings.HasSuffix(code, "probe_failed") || strings.HasSuffix(code, "_unavailable") || strings.HasSuffix(code, "_start_failed"):
		return "unavailable"
	case code == "recovery_required" || code == "worker_stopped" || code == "stop_unconfirmed":
		return "recovery"
	case code == "ticket_pause" || code == "ticket_cancel" || code == "user_stopped" || code == "user_deleted":
		return "user"
	case strings.Contains(code, "timeout"):
		return "timeout"
	case strings.Contains(code, "workspace") || strings.Contains(code, "worktree") || strings.Contains(code, "repository") || strings.Contains(code, "artifact"):
		return "workspace"
	}
	return "execution"
}

func updateTicketForEvent(ctx context.Context, q querier, e core.Event) error {
	switch e.Type {
	case "turn.queued":
		if _, err := q.ExecContext(ctx, `INSERT INTO attempt_turns(turn_id,attempt_id)
          SELECT ?,id FROM ticket_attempts WHERE ticket_id=? ORDER BY number DESC LIMIT 1`, e.TurnID, e.TaskID); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "UPDATE ticket_attempts SET status='queued',ended_at='' WHERE id=(SELECT attempt_id FROM attempt_turns WHERE turn_id=?)", e.TurnID); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, "UPDATE tickets SET status='queued' WHERE id=?", e.TaskID)
		return err
	case "workspace.prepared":
		_, err := q.ExecContext(ctx, "UPDATE ticket_attempts SET workspace_path=json_extract(?,'$.worktree_path') WHERE id=(SELECT attempt_id FROM attempt_turns WHERE turn_id=?)", string(e.Payload), e.TurnID)
		return err
	case "turn.started":
		if _, err := q.ExecContext(ctx, "UPDATE ticket_attempts SET status='running',started_at=CASE WHEN started_at='' THEN ? ELSE started_at END WHERE id=(SELECT attempt_id FROM attempt_turns WHERE turn_id=?)", e.CreatedAt, e.TurnID); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, "UPDATE tickets SET status='running' WHERE id=?", e.TaskID)
		return err
	case "turn.completed", "turn.failed":
		var attempt, adapter string
		var dispatched bool
		err := q.QueryRowContext(ctx, "SELECT a.id,a.adapter,l.dispatched FROM attempt_turns l JOIN ticket_attempts a ON a.id=l.attempt_id WHERE l.turn_id=?", e.TurnID).Scan(&attempt, &adapter, &dispatched)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var p eventPayload
		if err = json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		status := "completed"
		if e.Type == "turn.failed" {
			status = "failed"
			switch p.Code {
			case "user_stopped", "ticket_pause":
				status = "paused"
			case "ticket_cancel", "user_deleted":
				status = "cancelled"
			case "recovery_required", "stop_unconfirmed", "worker_stopped":
				status = "recovery_required"
			}
		}
		class := failureClass(p.Code)
		engineFailure := class == "quota" || class == "authentication" || class == "unavailable"
		if _, err = q.ExecContext(ctx, `UPDATE ticket_attempts SET status=?,ended_at=?,error_code=?,error=?,failure_class=?,safe_to_retry=?,
          snapshot_id=COALESCE((SELECT id FROM snapshots WHERE capture_key=?),snapshot_id) WHERE id=?`, status, e.CreatedAt, p.Code, p.Message, class, engineFailure && (!dispatched || p.RejectedBeforeStart), e.TurnID, attempt); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, "UPDATE tickets SET status=? WHERE id=?", status, e.TaskID); err != nil {
			return err
		}
		if status == "completed" || engineFailure {
			health := "healthy"
			if engineFailure {
				health = "degraded"
			}
			_, err = q.ExecContext(ctx, `INSERT INTO engine_health VALUES(?,?,?,?,?) ON CONFLICT(adapter) DO UPDATE SET
              status=excluded.status,failure_class=excluded.failure_class,error_code=excluded.error_code,updated_at=excluded.updated_at`, adapter, health, class, p.Code, e.CreatedAt)
		}
		return err
	}
	return nil
}
