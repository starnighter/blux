package artifact

import (
	"blux/internal/core"
	"blux/internal/store"
	"blux/internal/workspace"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Build a new immutable tree using a private index. The agent's checkout and
// source project do not participate in preparing a human revision.
func revisionCommit(ctx context.Context, root, parent, path, content string) (string, error) {
	row, err := gitBytes(ctx, root, "ls-tree", "-z", parent, "--", ":(literal)"+path)
	if err != nil {
		return "", err
	}
	meta, _, _ := strings.Cut(string(row), "\t")
	fields := strings.Fields(meta)
	if len(fields) != 3 || fields[0] != "100644" && fields[0] != "100755" {
		return "", core.ErrInvalidState
	}
	dir, err := os.MkdirTemp("", "blux-workbench-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	run := func(input string, args ...string) (string, error) {
		prefix := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Blux", "-c", "user.email=blux@localhost", "-C", root}
		cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "index"), "GIT_TERMINAL_PROMPT=0")
		cmd.Stdin = strings.NewReader(input)
		out, e := cmd.Output()
		return strings.TrimSpace(string(out)), e
	}
	if _, err = run("", "read-tree", parent); err != nil {
		return "", err
	}
	blob, err := run(content, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	if _, err = run(fields[0]+" "+blob+"\t"+path+"\x00", "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	tree, err := run("", "write-tree")
	if err != nil {
		return "", err
	}
	return run("", "commit-tree", tree, "-p", parent, "-m", "Edit "+path+" in Blux Workbench")
}

func (s Service) Revise(ctx context.Context, parent store.Snapshot, kind, name, content string) (store.Snapshot, error) {
	task, err := s.Store.GetTask(ctx, parent.TaskID)
	if err != nil {
		return store.Snapshot{}, err
	}
	v := store.Snapshot{ID: core.NewID("snapshot"), ParentSnapshotID: parent.ID, TaskID: parent.TaskID, TurnID: parent.TurnID, Generation: parent.Generation, Commit: parent.Commit, Diff: parent.Diff, CreatedAt: core.Now(), Artifacts: []store.ArtifactVersion{}}
	projectPath := ""
	if kind == "file" {
		projectPath = name
	}
	found := kind == "file"
	for _, a := range parent.Artifacts {
		_, projectName, prefixed := strings.Cut(a.Path, "/")
		projectFile := prefixed && a.ID == projectArtifactID(parent.ID, projectName)
		if kind == "artifact" && a.Path == name {
			found = true
			if projectFile {
				projectPath = projectName
			}
			if !projectFile {
				a.Hash, err = s.saveBlob([]byte(content))
				if err != nil {
					return v, err
				}
				a.Size = int64(len(content))
			}
		}
		if projectFile {
			continue
		}
		a.ID = core.NewID("artifact")
		v.Artifacts = append(v.Artifacts, a)
	}
	if !found {
		return v, core.ErrNotFound
	}
	if parent.Commit != "" {
		repo, err := (workspace.Manager{DataDir: s.DataDir}).Resolve(ctx, task.Task.RepositoryPath, task.Task.BaseCommit)
		if err != nil {
			return v, err
		}
		if projectPath != "" {
			v.Commit, err = revisionCommit(ctx, repo.Root, parent.Commit, projectPath, content)
			if err != nil {
				return v, err
			}
			diff, err := gitBytes(ctx, repo.Root, "diff", "--no-ext-diff", "--no-textconv", "--binary", task.Task.BaseCommit, v.Commit)
			if err != nil {
				return v, err
			}
			v.Diff = string(bytes.TrimRight(diff, "\n"))
		}
		files, err := s.projectArtifacts(ctx, repo.Root, task.Task.BaseCommit, v)
		if err != nil {
			return v, err
		}
		v.Artifacts = append(v.Artifacts, files...)
		if projectPath != "" {
			ref := "refs/blux/revisions/" + v.ID
			if _, err = git(ctx, repo.Root, "update-ref", ref, v.Commit); err != nil {
				return v, err
			}
			if err = s.Store.SaveRevision(ctx, v); err != nil {
				_, _ = git(context.WithoutCancel(ctx), repo.Root, "update-ref", "-d", ref, v.Commit)
				return v, err
			}
			return s.Store.Snapshot(ctx, v.ID)
		}
	}
	if projectPath != "" {
		return v, core.ErrInvalidState
	}
	if err = s.Store.SaveRevision(ctx, v); err != nil {
		return v, err
	}
	return s.Store.Snapshot(ctx, v.ID)
}
