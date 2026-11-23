package httpapi

import (
	"blux/internal/core"
	"blux/internal/store"
	"context"
	"encoding/json"
	"testing"
)

func TestTicketAPIControlsAndReadback(t *testing.T) {
	s, db, _ := testServer(t)
	ctx := context.Background()
	d, _, err := db.CreateTask(ctx, core.CreateTaskInput{Prompt: "task", RepositoryPath: "/repo", BaseCommit: "base", IdempotencyKey: "ticket-http"})
	if err != nil {
		t.Fatal(err)
	}
	url := "/api/v1/tasks/" + d.Task.ID + "/ticket"
	get, err := s.Client().Get(s.URL + url)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	var ticket store.Ticket
	if err = json.NewDecoder(get.Body).Decode(&ticket); err != nil || len(ticket.Attempts) != 1 || ticket.Status != "queued" {
		t.Fatal(ticket, err)
	}
	a := ticket.Attempts[0]
	body, _ := json.Marshal(store.TicketAction{Action: "pause", ExpectedAttemptID: a.ID})
	if post(t, s, url+"/actions", "", string(body)).StatusCode != 400 {
		t.Fatal("action accepted without key")
	}
	res := post(t, s, url+"/actions", "pause", string(body))
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&ticket) != nil || ticket.Status != "paused" {
		t.Fatal(res.StatusCode, ticket)
	}
	detail, err := db.GetTask(ctx, d.Task.ID)
	if err != nil || detail.Task.Status != "failed" || detail.Task.TicketStatus != "paused" {
		t.Fatal(detail.Task, err)
	}
	stale := post(t, s, url+"/actions", "stale", `{"action":"resume","expected_attempt_id":"obsolete"}`)
	if stale.StatusCode != 409 {
		t.Fatal(stale.StatusCode)
	}
}
