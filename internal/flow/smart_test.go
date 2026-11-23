package flow

import (
	"blux/internal/artifact"
	"blux/internal/core"
	"blux/internal/flowmodel"
	"blux/internal/store"
	"blux/internal/workspace"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeResult(t *testing.T, work, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func driveSmart(t *testing.T, s Service, id string, invalidVerifier bool) flowmodel.Delivery {
	t.Helper()
	ctx := context.Background()
	for step := 0; step < 24; step++ {
		if err := s.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		d, err := s.Store.Delivery(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != "running" {
			return d
		}
		for _, p := range d.Run.Phases {
			if p.Status == "awaiting_review" {
				return d
			}
		}
		for {
			job, err := s.Store.ClaimNext(ctx, "test")
			if err != nil {
				t.Fatal(err)
			}
			if job == nil {
				break
			}
			rt, err := (workspace.Manager{DataDir: s.DataDir}).Prepare(ctx, *job)
			if err != nil {
				t.Fatal(err)
			}
			name := ""
			for _, p := range d.Run.Phases {
				if p.Attempt.Units[0].TaskID == job.TaskID {
					name = p.ID
					break
				}
			}
			out := filepath.Join(rt.WorkspacePath, "artifacts")
			switch name {
			case "plan":
				writeResult(t, out, "Spec.md", "Produce two independent files.")
				writeResult(t, out, "Plan.json", `{"tickets":[{"id":"a","title":"A","prompt":"Write A","acceptance":"A exists"},{"id":"b","title":"B","prompt":"Write B","acceptance":"B exists"}]}`)
			case "t-a", "t-b":
				if d.Strategy == "discussion" {
					writeResult(t, out, "Discussion.md", name)
					break
				}
				writeResult(t, rt.WorktreePath, name+".txt", name)
				writeResult(t, out, "Implementation.md", name)
			case "cross-a", "cross-b":
				writeResult(t, out, "CrossCheck.md", "Cited disagreement")
			case "review":
				writeResult(t, out, "Review.md", "Checked the frozen baseline.")
				writeResult(t, out, "Review.json", `{"passed":true}`)
			case "verify":
				writeResult(t, out, "Verification.md", "Independent reproduction.")
				if !invalidVerifier {
					writeResult(t, out, "Verification.json", `{"passed":true}`)
				}
			case "summary":
				writeResult(t, out, "Summary.md", "Accepted evidence.")
			default:
				t.Fatalf("unknown dispatched phase %q", name)
			}
			if err := (artifact.Service{Store: s.Store, DataDir: s.DataDir}).Capture(ctx, *job, rt); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Store.AppendEvent(ctx, core.EventInput{TaskID: job.TaskID, SessionID: job.SessionID, TurnID: job.TurnID, Generation: job.Generation, Type: "turn.completed"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("no gate reached")
	return flowmodel.Delivery{}
}

func approveSmart(t *testing.T, s Service, d flowmodel.Delivery) flowmodel.Delivery {
	t.Helper()
	for _, p := range d.Run.Phases {
		if p.Status == "awaiting_review" {
			a := Action{Kind: "approve", Generation: d.Run.Generation, PhaseID: p.ID, SnapshotIDs: phaseVersions(p)}
			out, err := s.Action(context.Background(), d.ID, "approve-"+p.ID+":"+p.Attempt.ID, a)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("no gate: %s %s", d.Status, d.Reason)
	return d
}

func TestSmartPlanParallelSelectionAndBoundEvidence(t *testing.T) {
	ctx := context.Background()
	s := setup(t)
	s.Probe = func(context.Context, string) error { return nil }
	repo, base := flowFixtureRepo(t)
	in := CreateInput{Orchestration: "smart", Goal: "produce", RepositoryPath: repo, BaseCommit: base, Strategy: "fan_out", Agents: []string{"codex", "claude"}}
	d, err := s.Create(ctx, in, "start")
	if err != nil {
		t.Fatal(err)
	}
	prior, found, err := s.LookupCreate(ctx, in, "start")
	if err != nil || !found || prior.ID != d.ID {
		t.Fatal(found, err)
	}
	d = driveSmart(t, s, d.ID, false)
	if d.Plan == nil || len(d.Run.Phases) != 1 {
		t.Fatalf("started before plan approval: %+v", d)
	}
	approveSmart(t, s, d)
	d = driveSmart(t, s, d.ID, false)
	if d.Summary != "Accepted evidence." {
		t.Fatalf("no summary: %s %s", d.Reason, d.Status)
	}
	a, b := d.Run.Phases[1].Attempt.Units[0], d.Run.Phases[2].Attempt.Units[0]
	if a.Adapter == b.Adapter || a.BaseCommit != base || b.BaseCommit != base {
		t.Fatalf("parallel assignments: %+v %+v", a, b)
	}
	r, v := d.Run.Phases[3].Attempt.Units[0], d.Run.Phases[4].Attempt.Units[0]
	if r.BaseCommit != v.BaseCommit || r.BaseCommit != d.Head || r.TaskID == v.TaskID {
		t.Fatal("reviewer and verifier must independently inspect the same tree")
	}
	ticket, err := s.Store.Ticket(ctx, r.TaskID)
	if err != nil || !slices.Contains(ticket.Dependencies, b.TaskID) || !slices.Contains(ticket.Dependencies, a.TaskID) {
		t.Fatal(ticket, err)
	}
	approveSmart(t, s, d)
	d = driveSmart(t, s, d.ID, false)
	if d.Status != "completed" {
		t.Fatal(d.Status, d.Reason)
	}
	action := Action{Kind: "select_paths", Generation: 1, Paths: []string{"t-b.txt"}}
	d, err = s.Action(ctx, d.ID, "selection", action)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Action(ctx, d.ID, "selection", action)
	if err != nil || again.Run.ID != d.Run.ID {
		t.Fatal(again.Run.ID, d.Run.ID, err)
	}
	if _, err := s.Accept(ctx, d.ID, "stale", 1); !errors.Is(err, core.ErrStaleGeneration) {
		t.Fatal(err)
	}
	d = driveSmart(t, s, d.ID, true)
	if d.Status != "blocked" || d.Run.Phases[2].Attempt.Units[0].TaskID != "" {
		t.Fatalf("invalid verdict allowed: %s %s", d.Status, d.Reason)
	}
	d, err = s.Action(ctx, d.ID, "fix-verdict", Action{Kind: "retry", PhaseID: "verify", Generation: d.Run.Generation})
	if err != nil {
		t.Fatal(err)
	}
	d = driveSmart(t, s, d.ID, false)
	approveSmart(t, s, d)
	d = driveSmart(t, s, d.ID, false)
	writeResult(t, repo, "concurrent.txt", "local")
	if _, err := s.Accept(ctx, d.ID, "concurrent", d.Run.Generation); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed baseline was accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(repo, "concurrent.txt")); err != nil {
		t.Fatal(err)
	}
	d, err = s.Accept(ctx, d.ID, "final", d.Run.Generation)
	if err != nil || d.Status != "accepted" {
		t.Fatal(d.Status, d.Reason, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "t-a.txt")); !os.IsNotExist(err) {
		t.Fatal("excluded file received")
	}
	if b, err := os.ReadFile(filepath.Join(repo, "t-b.txt")); err != nil || string(b) != "t-b" {
		t.Fatal(string(b), err)
	}
}

func TestRetrySummaryRetainsReviewedCode(t *testing.T) {
	template := flowmodel.Template{Phases: flowmodel.Checks("auto", nil)}
	d := flowmodel.Delivery{Head: "verified-commit", BaseCommit: "base", Template: template, Run: flowmodel.NewRun(template, 1)}
	resetSmart(&d, 2, "Clarify report")
	if d.Head != "verified-commit" {
		t.Fatal("summary retry discarded the verified revision")
	}
}

func TestSmartStrategiesAndPlanCycle(t *testing.T) {
	plan := flowmodel.Plan{Tickets: []flowmodel.Ticket{{ID: "a", Title: "A", Prompt: "A", Acceptance: "A"}, {ID: "b", Title: "B", Prompt: "B", Acceptance: "B"}}}
	chain, err := plan.Expand("chain", nil)
	if err != nil || !slices.Contains(chain[1].DependsOn, "t-a") {
		t.Fatal(chain, err)
	}
	discussion, err := plan.Expand("discussion", nil)
	if err != nil || discussion[2].Role != "critic" || !slices.Contains(discussion[2].DependsOn, "t-b") {
		t.Fatal(discussion, err)
	}
	review, err := plan.Expand("review", nil)
	if err != nil || len(review) != 2 || review[0].Role != "reviewer" {
		t.Fatal(review, err)
	}
	plan.Tickets[0].DependsOn = []string{"b"}
	plan.Tickets[1].DependsOn = []string{"a"}
	if _, err := plan.Expand("auto", nil); !errors.Is(err, core.ErrInvalidState) {
		t.Fatal(err)
	}
}

func TestVerifyFrozenHumanRevisionUsesEditedProject(t *testing.T) {
	ctx := context.Background()
	s := setup(t)
	s.Probe = func(context.Context, string) error { return nil }
	repo, base := flowFixtureRepo(t)
	job, _, err := s.Store.CreateTask(ctx, core.CreateTaskInput{Prompt: "build", Mode: "development", Adapter: "codex", RepositoryPath: repo, BaseCommit: base, IdempotencyKey: "source"})
	if err != nil {
		t.Fatal(err)
	}
	finishJobs(t, s, false, "initial")
	v, err := s.Store.CapturedTurnSnapshot(ctx, job.Task.CurrentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := (artifact.Service{Store: s.Store, DataDir: s.DataDir}).Revise(ctx, v, "file", "code.txt", "human revision")
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.Store.CapturedTurnSnapshot(ctx, job.Task.CurrentTurnID)
	if err != nil || original.ID != v.ID {
		t.Fatalf("revision replaced execution capture: %s %v", original.ID, err)
	}
	d, err := s.Create(ctx, CreateInput{Orchestration: "smart", Strategy: "verify", Agents: []string{"codex"}, Goal: "verify edited code", RepositoryPath: repo, BaseCommit: base, InputSnapshotIDs: []string{revision.ID}}, "verify")
	if err != nil {
		t.Fatal(err)
	}
	d = driveSmart(t, s, d.ID, false)
	if len(d.Run.Phases) != 1 || d.Run.Phases[0].Status != "awaiting_review" {
		t.Fatal("verification dispatched before approval")
	}
	approveSmart(t, s, d)
	d = driveSmart(t, s, d.ID, false)
	if d.Run.Phases[1].Role != "verifier" || d.Run.Phases[1].Attempt.Units[0].BaseCommit != revision.Commit || d.Head != revision.Commit {
		t.Fatalf("wrong verification target: %s %s", d.Head, revision.Commit)
	}
	content, err := git(ctx, repo, "show", d.Head+":code.txt")
	if err != nil || content != "human revision" {
		t.Fatal(content, err)
	}
}

func TestSmartFailureRedispatchRespectsAttemptBoundary(t *testing.T) {
	ctx := context.Background()
	s := setup(t)
	s.Probe = func(context.Context, string) error { return nil }
	repo, base := flowFixtureRepo(t)
	d, err := s.Create(ctx, CreateInput{Orchestration: "smart", Agents: []string{"codex", "claude"}, Goal: "plan", RepositoryPath: repo, BaseCommit: base}, "start")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	job, err := s.Store.ClaimNext(ctx, "test")
	if err != nil || job == nil || job.Adapter != "codex" {
		t.Fatal(job, err)
	}
	fail := func(job *core.Job) {
		t.Helper()
		if _, _, err := s.Store.AppendEvent(ctx, core.EventInput{TaskID: job.TaskID, SessionID: job.SessionID, TurnID: job.TurnID, Generation: job.Generation, Type: "turn.failed", Payload: []byte(`{"code":"provider_unavailable","message":"unavailable"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	fail(job)
	if err = s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	retry, err := s.Store.ClaimNext(ctx, "test")
	if err != nil || retry == nil || retry.Adapter != "claude" || retry.TaskID != job.TaskID || retry.BaseCommit != job.BaseCommit {
		t.Fatal(retry, err)
	}
	if err = s.Store.MarkTurnDispatched(ctx, retry.TurnID); err != nil {
		t.Fatal(err)
	}
	fail(retry)
	if err = s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	d, err = s.Store.Delivery(ctx, d.ID)
	if err != nil || d.Status != "blocked" || d.BlockedTask != job.TaskID {
		t.Fatal(d.Status, d.Reason, err)
	}
	ticket, err := s.Store.Ticket(ctx, job.TaskID)
	if err != nil || len(ticket.Attempts) != 2 {
		t.Fatal(ticket, err)
	}
	_, err = s.Store.ActOnTicket(ctx, job.TaskID, "user-retry", store.TicketAction{Action: "retry", Adapter: "codex", ExpectedAttemptID: ticket.Attempts[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	d, err = s.Store.Delivery(ctx, d.ID)
	if err != nil || d.Status != "running" || d.Run.Phases[0].Attempt.Units[0].Adapter != "codex" {
		t.Fatal(d.Status, d.Reason, err)
	}
}
