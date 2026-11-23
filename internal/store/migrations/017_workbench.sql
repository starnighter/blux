-- A turn has one agent capture. Each reviewed version can have one successor.
ALTER TABLE snapshots RENAME COLUMN turn_id TO capture_key;
ALTER TABLE snapshots ADD COLUMN turn_id TEXT NOT NULL DEFAULT '';
ALTER TABLE snapshots ADD COLUMN parent_snapshot_id TEXT NOT NULL DEFAULT '';
UPDATE snapshots SET turn_id=capture_key;
CREATE INDEX snapshot_task_version ON snapshots(task_id);
CREATE TABLE snapshot_comments (
 id TEXT PRIMARY KEY, snapshot_id TEXT NOT NULL REFERENCES snapshots(id),
 kind TEXT NOT NULL, path TEXT NOT NULL, side TEXT NOT NULL, line INTEGER NOT NULL,
 body TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX comments_location ON snapshot_comments(snapshot_id,kind,path);
