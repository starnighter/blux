package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"blux/internal/core"
	"blux/internal/store"
)

func TestManualHandoffPinsSelectedArtifactsAndRequestIdentity(t *testing.T) {
	srv, db, repo := testServer(t)
	ctx := context.Background()
	group, _, err := db.CreateCollaboration(ctx, store.CreateCollaborationInput{Goal: "ship", RepositoryPath: repo}, "group")
	if err != nil {
		t.Fatal(err)
	}
	route := "/api/v1/collaborations/" + group.ID + "/tasks"
	create := post(t, srv, route, "author", `{"prompt":"write two files"}`)
	if create.StatusCode != http.StatusCreated {
		t.Fatal(create.Status)
	}
	job, err := db.ClaimNext(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	version := store.Snapshot{ID: "snap-first", TaskID: job.TaskID, TurnID: job.TurnID, Generation: job.Generation, CreatedAt: core.Now(), Artifacts: []store.ArtifactVersion{
		{ID: "file-A", Path: "review.md", Hash: "one"},
		{ID: "file-B", Path: "other.md", Hash: "two"},
	}}
	if err = db.SaveSnapshot(ctx, version); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.AppendEvent(ctx, core.EventInput{TaskID: job.TaskID, SessionID: job.SessionID, TurnID: job.TurnID, Generation: job.Generation, Type: "turn.completed"}); err != nil {
		t.Fatal(err)
	}
	send := func(key, body string, want int) core.TaskDetail {
		t.Helper()
		r := post(t, srv, route, key, body)
		if r.StatusCode != want {
			t.Fatalf("%s: got %d want %d", body, r.StatusCode, want)
		}
		var task core.TaskDetail
		if want < 300 {
			if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
				t.Fatal(err)
			}
		}
		return task
	}
	const onlyA = `{"prompt":"review","artifact_ids":["file-A"]}`
	reader := send("reader", onlyA, 201)
	inputs, err := db.TaskInputs(ctx, reader.Task.ID)
	if err != nil || len(inputs) != 1 || inputs[0].ID != "file-A" {
		t.Fatal(inputs, err)
	}
	if again := send("reader", onlyA, 200); again.Task.ID != reader.Task.ID {
		t.Fatal("duplicate task")
	}
	send("reader", `{"prompt":"review","artifact_ids":["file-B"]}`, 409)
	send("unshared", `{"prompt":"review","artifact_ids":["unshared-file"]}`, 400)
	none := send("empty", `{"prompt":"review","artifact_ids":[]}`, 201)
	inputs, err = db.TaskInputs(ctx, none.Task.ID)
	if err != nil || len(inputs) != 0 {
		t.Fatal(inputs, err)
	}
}
