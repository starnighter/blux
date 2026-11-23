package httpapi

import (
	"blux/internal/artifact"
	"blux/internal/core"
	"blux/internal/store"
	"blux/internal/workspace"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkbenchRevisionCommentAndAcceptance(t *testing.T) {
	ctx := context.Background()
	source, data := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "design.md"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(data, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := workspace.Manager{DataDir: data}
	repo, err := m.Freeze(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := db.CreateTask(ctx, core.CreateTaskInput{Prompt: "design", RepositoryPath: source, BaseCommit: repo.BaseCommit, IdempotencyKey: "design"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := db.ClaimNext(ctx, "test")
	if err != nil || job == nil {
		t.Fatal(err)
	}
	rt, err := m.Prepare(ctx, *job)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(rt.WorktreePath, "design.md"), []byte("agent version\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(rt.WorkspacePath, "artifacts", "notes.html"), []byte("<p>original notes</p>"), 0600); err != nil {
		t.Fatal(err)
	}
	svc := artifact.Service{DataDir: data, Store: db}
	if err = svc.Capture(ctx, *job, rt); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.AppendEvent(ctx, core.EventInput{TaskID: job.TaskID, SessionID: job.SessionID, TurnID: job.TurnID, Generation: job.Generation, Type: "turn.completed"}); err != nil {
		t.Fatal(err)
	}
	versions, err := db.Snapshots(ctx, d.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	original := versions[0]
	s := Server{Store: db, DataDir: data}
	mux := s.Handler()
	call := func(method, path, body string, status int) string {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body)))
		if rec.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	revision := func(parent, body string) store.Snapshot {
		t.Helper()
		var v store.Snapshot
		if err := json.Unmarshal([]byte(call("POST", "/snapshots/"+parent+"/revisions", body, 201)), &v); err != nil {
			t.Fatal(err)
		}
		if v.ParentSnapshotID != parent || v.TurnID != original.TurnID {
			t.Fatal(v)
		}
		return v
	}
	commentPath := "/snapshots/" + original.ID + "/comments"
	call("POST", commentPath, `{"kind":"file","path":"design.md","side":"new","line":1,"body":"Clarify this line"}`, 201)
	call("POST", commentPath, `{"kind":"file","path":"design.md","side":"new","line":4,"body":"Out of range"}`, 400)
	body := `{"kind":"artifact","path":"project/design.md","content":"human version\n"}`
	newer := revision(original.ID, body)
	call("POST", "/snapshots/"+original.ID+"/revisions", body, 409)
	latest := revision(newer.ID, `{"kind":"artifact","path":"notes.html","content":"<p>human notes</p>"}`)
	if latest.Commit != newer.Commit || newer.Commit == original.Commit {
		t.Fatal("project and artifact revision identities")
	}
	for _, v := range []struct{ id, text string }{{original.ID, "agent version"}, {newer.ID, "human version"}, {latest.ID, "human version"}} {
		data := call("GET", "/tasks/"+d.Task.ID+"/file?snapshot="+v.id+"&path=design.md", "", 200)
		if !strings.Contains(data, v.text) {
			t.Fatal(data)
		}
	}
	if out := call("GET", "/previews/"+original.ID+"/notes.html", "", 200); !strings.Contains(out, "original notes") {
		t.Fatal(out)
	}
	if out := call("GET", "/previews/"+latest.ID+"/notes.html", "", 200); !strings.Contains(out, "human notes") {
		t.Fatal(out)
	}
	if out := call("GET", commentPath+"?kind=file&path=design.md", "", 200); !strings.Contains(out, "Clarify") {
		t.Fatal(out)
	}
	if out := call("GET", "/snapshots/"+latest.ID+"/comments?kind=file&path=design.md", "", 200); strings.Contains(out, "Clarify") {
		t.Fatal(out)
	}
	if b, err := os.ReadFile(filepath.Join(source, "design.md")); err != nil || string(b) != "original\n" {
		t.Fatal("source changed before acceptance", err)
	}
	if b, err := os.ReadFile(filepath.Join(rt.WorktreePath, "design.md")); err != nil || string(b) != "agent version\n" {
		t.Fatal("agent worktree changed during revision", err)
	}
	a, err := svc.Accept(ctx, latest.ID, "accept")
	if err != nil || a.Status != "accepted" {
		t.Fatal(a, err)
	}
	if b, err := os.ReadFile(filepath.Join(source, "design.md")); err != nil || string(b) != "human version\n" {
		t.Fatal(string(b), err)
	}
}
