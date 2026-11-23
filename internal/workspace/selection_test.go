package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSelectedCommitUsesExactVersionsAndKeepsRenameTogether(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@localhost", "-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return out
	}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	run("init")
	write("old.txt", []byte("rename me\n"))
	write("untouched.txt", []byte("original\n"))
	write("removed.txt", []byte("delete me\n"))
	run("add", "-A")
	run("commit", "-m", "base")
	base := string(bytes.TrimSpace(run("rev-parse", "HEAD")))
	run("mv", "old.txt", "new name.txt")
	run("rm", "removed.txt")
	write("binary.dat", []byte{0, 1, 2, 0, 255})
	write("untouched.txt", []byte("excluded change\n"))
	run("add", "-A")
	run("commit", "-m", "changes")
	head := string(bytes.TrimSpace(run("rev-parse", "HEAD")))
	paths := []string{"new name.txt", "removed.txt", "binary.dat"}
	commit, err := SelectedCommit(ctx, root, base, head, paths)
	if err != nil {
		t.Fatal(err)
	}
	again, err := SelectedCommit(ctx, root, base, head, []string{"binary.dat", "removed.txt", "new name.txt"})
	if err != nil || again != commit {
		t.Fatalf("unstable selection %s %s: %v", commit, again, err)
	}
	if got := run("show", commit+":untouched.txt"); string(got) != "original\n" {
		t.Fatalf("included unrelated file: %q", got)
	}
	if got := run("show", commit+":binary.dat"); !bytes.Equal(got, []byte{0, 1, 2, 0, 255}) {
		t.Fatalf("binary changed: %v", got)
	}
	if got := run("ls-tree", "--name-only", commit); string(got) != "binary.dat\nnew name.txt\nuntouched.txt\n" {
		t.Fatalf("wrong tree: %q", got)
	}
	if _, err := SelectedCommit(ctx, root, base, head, []string{"old.txt"}); err == nil {
		t.Fatal("rename source accepted independently")
	}
	if got := string(bytes.TrimSpace(run("rev-parse", "HEAD"))); got != head {
		t.Fatalf("source HEAD changed: %s", got)
	}
	if got := run("status", "--porcelain"); len(got) != 0 {
		t.Fatalf("source index changed: %s", got)
	}
}
