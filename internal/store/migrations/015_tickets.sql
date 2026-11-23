CREATE TABLE tickets (
  id TEXT PRIMARY KEY REFERENCES tasks(id),
  goal TEXT NOT NULL,
  dependencies TEXT NOT NULL DEFAULT '[]',
  acceptance_criteria TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL
);
CREATE TABLE ticket_attempts (
  id TEXT PRIMARY KEY,
  ticket_id TEXT NOT NULL REFERENCES tickets(id),
  number INTEGER NOT NULL,
  adapter TEXT NOT NULL,
  status TEXT NOT NULL,
  workspace_path TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  ended_at TEXT NOT NULL DEFAULT '',
  error_code TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  failure_class TEXT NOT NULL DEFAULT '',
  safe_to_retry INTEGER NOT NULL DEFAULT 0,
  snapshot_id TEXT NOT NULL DEFAULT '',
  policy_json TEXT NOT NULL DEFAULT '{"evidence":"历史记录未保存执行策略"}',
  UNIQUE(ticket_id,number)
);
CREATE UNIQUE INDEX ticket_active_attempt ON ticket_attempts(ticket_id)
  WHERE status IN ('queued','running','pausing','cancelling');
CREATE TABLE attempt_turns (
  turn_id TEXT PRIMARY KEY,
  attempt_id TEXT NOT NULL REFERENCES ticket_attempts(id),
  dispatched INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX attempt_turns_attempt ON attempt_turns(attempt_id);
CREATE TABLE ticket_actions (
  ticket_id TEXT NOT NULL REFERENCES tickets(id),
  key TEXT NOT NULL,
  digest TEXT NOT NULL,
  result_json TEXT NOT NULL,
  PRIMARY KEY(ticket_id,key)
);
CREATE TABLE engine_health (
  adapter TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  failure_class TEXT NOT NULL,
  error_code TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- Existing conversations contain turns, but no recorded retry boundaries.
-- Keep each original conversation together and retain its actual turn history.
INSERT INTO tickets(id,goal,status)
SELECT t.id,t.prompt,CASE u.error_code WHEN 'recovery_required' THEN 'recovery_required'
  WHEN 'user_stopped' THEN 'paused' ELSE t.status END
FROM tasks t JOIN turns u ON u.id=t.current_turn_id
WHERE t.id NOT IN (SELECT task_id FROM manager_tasks);
INSERT INTO ticket_attempts(id,ticket_id,number,adapter,status,workspace_path,created_at,started_at,ended_at,error_code,error,snapshot_id)
SELECT 'legacy_' || t.id,t.id,1,t.adapter,k.status,
  COALESCE(json_extract(s.runtime_json,'$.worktree_path'),''),t.created_at,
  COALESCE((SELECT MIN(a.started_at) FROM turns a WHERE a.task_id=t.id AND a.started_at<>''),''),
  u.ended_at,u.error_code,u.error,COALESCE((SELECT id FROM snapshots WHERE task_id=t.id ORDER BY created_at DESC,id DESC LIMIT 1),'')
FROM tickets k JOIN tasks t ON t.id=k.id JOIN turns u ON u.id=t.current_turn_id
JOIN sessions s ON s.task_id=t.id;
INSERT INTO attempt_turns(turn_id,attempt_id,dispatched)
SELECT u.id,a.id,u.status<>'queued' FROM turns u JOIN ticket_attempts a ON a.ticket_id=u.task_id;
