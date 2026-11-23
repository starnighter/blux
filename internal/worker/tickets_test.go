package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blux/internal/agent"
	"blux/internal/core"
	"blux/internal/store"
)

// Stage process shutdown separately from cancellation so the test can observe
// the externally visible state while the CLI is still exiting.
type shutdownSession struct {
	waitingSession
	started chan struct{}
	closing chan struct{}
	exited  chan struct{}
}

func (s *shutdownSession) Send(ctx context.Context, r agent.TurnRequest) (<-chan agent.Update, error) {
	close(s.started)
	return s.waitingSession.Send(ctx, r)
}
func (s *shutdownSession) Close() error {
	close(s.closing)
	<-s.exited
	return nil
}

type shutdownAdapter struct {
	waitingAdapter
	session *shutdownSession
}

func (a shutdownAdapter) Start(context.Context, agent.SessionConfig) (agent.Session, error) {
	return a.session, nil
}

func TestRunningTicketRequiresExitBeforePauseOrCancellation(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=test@local", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	base, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ action, pending, final string }{{"pause", "pausing", "paused"}, {"cancel", "cancelling", "cancelled"}} {
		t.Run(test.action, func(t *testing.T) {
			data := t.TempDir()
			db, err := store.Open(filepath.Join(data, "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			task, _, err := db.CreateTask(ctx, core.CreateTaskInput{Prompt: "work", RepositoryPath: repo, BaseCommit: strings.TrimSpace(string(base)), IdempotencyKey: test.action})
			if err != nil {
				t.Fatal(err)
			}
			j, err := db.ClaimNext(ctx, "w")
			if err != nil || j == nil {
				t.Fatal(j, err)
			}
			sess := &shutdownSession{started: make(chan struct{}), closing: make(chan struct{}), exited: make(chan struct{})}
			w := Worker{Store: db, DataDir: data, Adapter: shutdownAdapter{session: sess}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			done := make(chan error, 1)
			go func() {
				err := w.execute(ctx, *j)
				if err != nil {
					err = w.fail(*j, err)
				}
				done <- err
			}()
			select {
			case <-sess.started:
			case <-ctx.Done():
				t.Fatal("worker never dispatched")
			}
			get := func() store.Ticket {
				t.Helper()
				ticket, err := db.Ticket(ctx, task.Task.ID)
				if err != nil {
					t.Fatal(err)
				}
				return ticket
			}
			a := get().Attempts[0]
			if _, err = db.ActOnTicket(ctx, task.Task.ID, "stop", store.TicketAction{Action: test.action, ExpectedAttemptID: a.ID}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-sess.closing:
			case <-ctx.Done():
				t.Fatal("worker did not observe stop request")
			}
			if get().Status != test.pending {
				t.Fatal("reported termination before CLI exit", get())
			}
			if _, err = db.ActOnTicket(ctx, task.Task.ID, "early", store.TicketAction{Action: "resume", ExpectedAttemptID: a.ID}); !errors.Is(err, core.ErrConflict) {
				t.Fatal("resumed before exit", err)
			}
			close(sess.exited)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("worker never confirmed exit")
			}
			if get().Status != test.final {
				t.Fatal(get())
			}
		})
	}
}
