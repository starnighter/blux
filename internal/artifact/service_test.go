package artifact

import (
	"blux/internal/core"
	"blux/internal/store"
	"blux/internal/workspace"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionAndAcceptance(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "success"
		if conflict {
			name = "conflict"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			src := t.TempDir()
			g := func(dir string, args ...string) string {
				t.Helper()
				s, e := git(ctx, dir, args...)
				if e != nil {
					t.Fatal(args, e)
				}
				return s
			}
			g(src, "init")
			os.WriteFile(filepath.Join(src, "code.txt"), []byte("base\n"), 0600)
			g(src, "add", ".")
			g(src, "commit", "-m", "base")
			base := g(src, "rev-parse", "HEAD")
			data := t.TempDir()
			db, e := store.Open(filepath.Join(data, "db"))
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			d, _, e := db.CreateTask(ctx, core.CreateTaskInput{Prompt: "edit", Adapter: "codex", RepositoryPath: src, BaseCommit: base, IdempotencyKey: "test"})
			if e != nil {
				t.Fatal(e)
			}
			j, e := db.ClaimNext(ctx, "worker")
			if e != nil {
				t.Fatal(e)
			}
			rt, e := (workspace.Manager{DataDir: data}).Prepare(ctx, *j)
			if e != nil {
				t.Fatal(e)
			}
			os.WriteFile(filepath.Join(rt.WorktreePath, "code.txt"), []byte("agent\n"), 0600)
			os.WriteFile(filepath.Join(rt.WorkspacePath, "artifacts", "Spec.md"), []byte("version one"), 0600)
			svc := Service{Store: db, DataDir: data}
			if e = svc.Capture(ctx, *j, rt); e != nil {
				t.Fatal(e)
			}
			if _, _, e = db.AppendEvent(ctx, core.EventInput{TaskID: j.TaskID, SessionID: j.SessionID, TurnID: j.TurnID, Generation: j.Generation, Type: "turn.completed"}); e != nil {
				t.Fatal(e)
			}
			versions, e := db.Snapshots(ctx, d.Task.ID)
			if e != nil || len(versions) != 1 || !strings.Contains(versions[0].Diff, "+agent") {
				t.Fatal(versions, e)
			}
			os.WriteFile(filepath.Join(rt.WorkspacePath, "artifacts", "Spec.md"), []byte("mutated"), 0600)
			b, e := os.ReadFile(svc.Blob(versions[0].Artifacts[0].Hash))
			if e != nil || string(b) != "version one" {
				t.Fatal("version changed", e)
			}
			b, _ = os.ReadFile(filepath.Join(src, "code.txt"))
			if string(b) != "base\n" {
				t.Fatal("source changed before acceptance")
			}
			if conflict {
				os.WriteFile(filepath.Join(src, "code.txt"), []byte("source\n"), 0600)
				g(src, "add", ".")
				g(src, "commit", "-m", "source edit")
			}
			a, e := svc.Accept(ctx, versions[0].ID, "accept")
			if e != nil {
				t.Fatal(e)
			}
			want := "accepted"
			if conflict {
				want = "blocked"
			}
			if a.Status != want {
				t.Fatalf("%+v", a)
			}
			again, e := svc.Accept(ctx, versions[0].ID, "accept")
			if e != nil || again.Status != a.Status {
				t.Fatal("retry", again, e)
			}
			b, _ = os.ReadFile(filepath.Join(src, "code.txt"))
			expected := "agent\n"
			if conflict {
				expected = "source\n"
			}
			if string(b) != expected {
				t.Fatal("source mismatch", string(b))
			}
			if conflict {
				status := g(a.IntegrationPath, "status", "--porcelain=v1")
				if !strings.Contains(status, "UU") {
					t.Fatal("conflict lost")
				}
			}
		})
	}
}

func TestAcceptPathsIsScopedAndIdempotent(t *testing.T) {
	for _, gitProject := range []bool{true, false} {
		name := "directory"
		if gitProject {
			name = "repository"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			data, source := t.TempDir(), t.TempDir()
			if gitProject {
				if _, err := git(ctx, source, "init"); err != nil {
					t.Fatal(err)
				}
				if _, err := git(ctx, source, "commit", "--allow-empty", "-m", "initial"); err != nil {
					t.Fatal(err)
				}
			}
			m := workspace.Manager{DataDir: data}
			base, err := m.Freeze(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(data, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			d, _, err := db.CreateTask(ctx, core.CreateTaskInput{Prompt: "prepare", RepositoryPath: source, BaseCommit: base.BaseCommit, IdempotencyKey: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			job, err := db.ClaimNext(ctx, "worker")
			if err != nil {
				t.Fatal(err)
			}
			rt, err := m.Prepare(ctx, *job)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"first", "second"} {
				if err := os.WriteFile(filepath.Join(rt.WorktreePath, path), []byte(path+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			service := Service{DataDir: data, Store: db}
			if err := service.Capture(ctx, *job, rt); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.AppendEvent(ctx, core.EventInput{TaskID: job.TaskID, SessionID: job.SessionID, TurnID: job.TurnID, Generation: job.Generation, Type: "turn.completed"}); err != nil {
				t.Fatal(err)
			}
			versions, err := db.Snapshots(ctx, d.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			id := versions[0].ID
			one, err := service.AcceptPaths(ctx, id, "first-key", []string{"first"})
			if err != nil || one.Status != "accepted" {
				t.Fatal(one, err)
			}
			two, err := service.AcceptPaths(ctx, id, "first-key", []string{"first"})
			if err != nil || two.ID != one.ID || two.ResultCommit != one.ResultCommit {
				t.Fatal(two, err)
			}
			if _, err := service.AcceptPaths(ctx, id, "first-key", []string{"second"}); err != core.ErrConflict {
				t.Fatalf("changed request result: %v", err)
			}
			if _, err := os.Stat(filepath.Join(source, "second")); !os.IsNotExist(err) {
				t.Fatalf("unselected path written: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(source, "first")); err != nil || string(got) != "first\n" {
				t.Fatal(string(got), err)
			}
		})
	}
}
