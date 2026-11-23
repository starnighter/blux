package store

import (
	"blux/internal/core"
	"blux/internal/flowmodel"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTicketActionsKeepTurnsAndRetriesDistinct(t *testing.T) {
	ctx := context.Background()
	s := executionStore(t)
	d, _, err := s.CreateTask(ctx, core.CreateTaskInput{Prompt: "fix", TicketGoal: "repair", AcceptanceCriteria: "build passes", RepositoryPath: "/repo", BaseCommit: "base", IdempotencyKey: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	id := d.Task.ID
	get := func() Ticket {
		t.Helper()
		ticket, e := s.Ticket(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		return ticket
	}
	first := get()
	if first.Goal != "repair" || first.AcceptanceCriteria != "build passes" || first.Attempts[0].Policy.Sandbox == "" {
		t.Fatal(first)
	}
	j := executionJob(t, s)
	executionEvent(t, s, j, "turn.completed", map[string]string{})
	next, _, err := s.CreateTurn(ctx, id, core.CreateTurnInput{Prompt: "follow-up", IdempotencyKey: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	same := get()
	if len(same.Attempts) != 1 || !reflect.DeepEqual(same.Attempts[0].TurnIDs, []string{j.TurnID, next.SubmittedTurnID}) {
		t.Fatal(same)
	}
	pause := TicketAction{Action: "pause", ExpectedAttemptID: first.Attempts[0].ID}
	paused, err := s.ActOnTicket(ctx, id, "pause", pause)
	if err != nil || paused.Status != "paused" {
		t.Fatal(paused, err)
	}
	resume := TicketAction{Action: "resume", ExpectedAttemptID: first.Attempts[0].ID, Adapter: "claude"}
	resumed, err := s.ActOnTicket(ctx, id, "resume", resume)
	if err != nil || len(resumed.Attempts) != 2 || resumed.Attempts[1].Adapter != "claude" {
		t.Fatal(resumed, err)
	}
	again, err := s.ActOnTicket(ctx, id, "resume", resume)
	if err != nil || !reflect.DeepEqual(resumed, again) {
		t.Fatal(again, err)
	}
	if _, err = s.ActOnTicket(ctx, id, "stale", resume); !errors.Is(err, core.ErrStaleGeneration) {
		t.Fatal(err)
	}
	wrong := resume
	wrong.Adapter = "deepseek"
	if _, err = s.ActOnTicket(ctx, id, "resume", wrong); !errors.Is(err, core.ErrConflict) {
		t.Fatal(err)
	}
	retry := executionJob(t, s)
	if retry.Adapter != "claude" || retry.TaskID != id || retry.Prompt != "follow-up" || retry.Generation <= j.Generation {
		t.Fatal(retry)
	}
	if err = s.Replay(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumed.Attempts[0], get().Attempts[0]) {
		t.Fatal("history changed on replay")
	}
}

func TestTicketReassignPreservesWorktreeAndResetsNativeSession(t *testing.T) {
	ctx := context.Background()
	s := executionStore(t)
	d, _, err := s.CreateTask(ctx, core.CreateTaskInput{Prompt: "build", Adapter: "codex", Model: "old", ModelProvider: "old-provider", ReasoningEffort: "high", RepositoryPath: "/repo", BaseCommit: "base", IdempotencyKey: "build"})
	if err != nil {
		t.Fatal(err)
	}
	j := executionJob(t, s)
	worktree := t.TempDir()
	executionEvent(t, s, j, "workspace.prepared", core.RuntimeInfo{WorkspacePath: worktree, WorktreePath: worktree, BaseCommit: "base"})
	executionEvent(t, s, j, "agent.session.ready", core.RuntimeInfo{NativeThreadID: "native-old", Model: "old", ModelProvider: "old-provider", ReasoningEffort: "high"})
	executionEvent(t, s, j, "turn.failed", map[string]string{"code": "codex_auth_required", "message": "grant expired"})
	ticket, err := s.Ticket(ctx, d.Task.ID)
	if err != nil || !ticket.Attempts[0].SafeToRetry || ticket.Attempts[0].FailureClass != "authentication" {
		t.Fatal(ticket, err)
	}
	h, err := s.EngineHealth(ctx)
	if err != nil || h["codex"].Status != "degraded" {
		t.Fatal(h, err)
	}
	a := TicketAction{Action: "retry", ExpectedAttemptID: ticket.Attempts[0].ID, Adapter: "claude", Automatic: true}
	_, err = s.ActOnTicket(ctx, d.Task.ID, "reassign", a)
	if err != nil {
		t.Fatal(err)
	}
	j = executionJob(t, s)
	if j.Adapter != "claude" || j.Runtime.NativeThreadID != "" || j.Runtime.WorktreePath != worktree || j.Model != "" || j.ModelProvider != "" || j.ReasoningEffort != "" {
		t.Fatal(j)
	}
	if err = s.MarkTurnDispatched(ctx, j.TurnID); err != nil {
		t.Fatal(err)
	}
	executionEvent(t, s, j, "turn.failed", map[string]string{"code": "claude_usage_limit", "message": "quota"})
	ticket, err = s.Ticket(ctx, j.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	a.ExpectedAttemptID = ticket.Attempts[1].ID
	a.Adapter = "codex"
	if _, err = s.ActOnTicket(ctx, d.Task.ID, "unsafe", a); !errors.Is(err, core.ErrConflict) {
		t.Fatal("automatically retried a dispatched attempt", err)
	}
	if err = os.Mkdir(filepath.Join(worktree, ".agent"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(worktree, ".agent", "app-server-process.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	a.Automatic = false
	if _, err = s.ActOnTicket(ctx, d.Task.ID, "uncertain", a); !errors.Is(err, core.ErrConflict) {
		t.Fatal("retried with uncertain process identity", err)
	}
	if err = s.Replay(ctx, j.TaskID); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverOnlyCapturedTurnAndConfirmRequestedStop(t *testing.T) {
	ctx := context.Background()
	s := executionStore(t)
	for _, kind := range []string{"captured", "raced", "cancel", "unknown"} {
		d, _, err := s.CreateTask(ctx, core.CreateTaskInput{Prompt: kind, RepositoryPath: "/repo", BaseCommit: "base", IdempotencyKey: kind})
		if err != nil {
			t.Fatal(err)
		}
		j := executionJob(t, s)
		status := "recovery_required"
		switch kind {
		case "captured", "raced":
			err = s.SaveSnapshot(ctx, Snapshot{ID: kind, TaskID: j.TaskID, TurnID: j.TurnID, Generation: j.Generation, CreatedAt: core.Now()})
			status = "completed"
		case "cancel":
			ticket, e := s.Ticket(ctx, j.TaskID)
			if e != nil {
				t.Fatal(e)
			}
			_, err = s.ActOnTicket(ctx, j.TaskID, "stop", TicketAction{Action: "cancel", ExpectedAttemptID: ticket.Attempts[0].ID})
			status = "cancelled"
		}
		if err != nil {
			t.Fatal(err)
		}
		if kind == "raced" {
			err = s.FinishFailedTurn(ctx, *j, json.RawMessage(`{"code":"ticket_pause"}`))
		} else {
			err = s.RecoverRunning(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := s.Ticket(ctx, d.Task.ID)
		if err != nil || ticket.Status != status {
			t.Fatal(ticket, err)
		}
		if kind == "captured" && ticket.Attempts[0].SnapshotID != "captured" {
			t.Fatal(ticket)
		}
	}
	if j, err := s.ClaimNext(ctx, "worker"); err != nil || j != nil {
		t.Fatal("duplicate dispatch", j, err)
	}
}

func TestMigrateVersion14TicketUsesOnlyRecordedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v14.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{initialMigration, executionMigration, controlsMigration, artifactsMigration, contextMigration, flowsMigration, managerMigration, turnContextMigration, collaborationsMigration, turnModelsMigration, removeSimulationsMigration, reasoningEffortMigration, deletionMigration, pinsMigration} {
		if _, err = old.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	_, err = old.Exec(`INSERT INTO tasks(id,title,prompt,status,adapter,session_id,current_turn_id,created_at,updated_at)
       VALUES('task_old','Legacy','goal','failed','codex','sess','t2','1','4');
       INSERT INTO sessions(id,task_id,created_at,runtime_json) VALUES('sess','task_old','1','{"worktree_path":"/old/worktree"}');
       INSERT INTO turns(id,task_id,session_id,prompt,status,generation,created_at,started_at,ended_at,error_code,error,created_seq) VALUES
       ('t1','task_old','sess','goal','completed',1,'1','2','3','','',1),
       ('t2','task_old','sess','continue','failed',1,'3','3','4','codex_usage_limit','quota',2);
       INSERT INTO snapshots(id,task_id,turn_id,generation,commit_id,diff,created_at) VALUES('v1','task_old','t1',1,'abc','','3');
       PRAGMA user_version=14;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.Ticket(context.Background(), "task_old")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 1 || got.Goal != "goal" {
		t.Fatal(got)
	}
	a := got.Attempts[0]
	if a.WorkspacePath != "/old/worktree" || a.Policy.Sandbox != "" || a.ErrorCode != "codex_usage_limit" || a.FailureClass != "quota" || a.SafeToRetry || a.SnapshotID != "v1" || !reflect.DeepEqual(a.TurnIDs, []string{"t1", "t2"}) {
		t.Fatal(a)
	}
}

func TestSupersededFlowTicketCannotDispatchAgain(t *testing.T) {
	ctx := context.Background()
	db := executionStore(t)
	d := flowmodel.Delivery{ID: "delivery", Run: flowmodel.FlowRun{Generation: 1}}
	if _, err := db.SaveDelivery(ctx, d, 0, "", "", ""); err != nil {
		t.Fatal(err)
	}
	task, _, err := db.CreateTask(ctx, core.CreateTaskInput{Prompt: "do", RepositoryPath: "/repo", BaseCommit: "abc", DeliveryID: d.ID, FlowGeneration: 1, IdempotencyKey: "task"})
	if err != nil {
		t.Fatal(err)
	}
	j := executionJob(t, db)
	executionEvent(t, db, j, "turn.failed", map[string]string{"code": "codex_not_found"})
	ticket, err := db.Ticket(ctx, task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := TicketAction{Action: "retry", ExpectedAttemptID: ticket.Attempts[0].ID}
	if _, err = db.ActOnTicket(ctx, task.Task.ID, "gone", a); !errors.Is(err, core.ErrConflict) {
		t.Fatal("superseded task was dispatched", err)
	}
	d, err = db.Delivery(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.Run.Generation = 2
	d.Run.Phases = []flowmodel.Phase{{Attempt: flowmodel.Attempt{Units: []flowmodel.Unit{{TaskID: task.Task.ID}}}}}
	if _, err = db.SaveDelivery(ctx, d, d.Revision, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActOnTicket(ctx, task.Task.ID, "still-active", a); err != nil {
		t.Fatal("current unit retained across generation should resume", err)
	}
}
