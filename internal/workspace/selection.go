// File selections are constructed in a private Git index against the reviewed base.
package workspace

import (
	"blux/internal/core"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type ChangedFile struct {
	Path         string `json:"path"`
	Status       string `json:"status"`
	PreviousPath string `json:"previous_path,omitempty"`
	mode         string
	object       string
}

func ChangedFiles(ctx context.Context, root, base, head string) ([]ChangedFile, error) {
	raw, err := exec.CommandContext(ctx, "git", "-C", root, "diff-tree", "--no-commit-id", "--raw", "-r", "--no-abbrev", "-z", "--find-renames", base, head, "--").Output()
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(raw, []byte{0})
	changes := []ChangedFile{}
	for i := 0; i < len(fields)-1; {
		meta := strings.Fields(string(fields[i]))
		change := ChangedFile{Path: string(fields[i+1]), Status: meta[4][:1], mode: meta[1], object: meta[3]}
		i += 2
		if change.Status == "R" {
			change.PreviousPath = change.Path
			change.Path = string(fields[i])
			i++
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func SelectedCommit(ctx context.Context, root, base, head string, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", core.ErrInvalidState
	}
	files, err := ChangedFiles(ctx, root, base, head)
	if err != nil {
		return "", err
	}
	selected := make(map[string]bool, len(paths))
	for _, path := range paths {
		selected[path] = true
	}
	var removes, updates bytes.Buffer
	allowed := map[string]bool{}
	for _, file := range files {
		if !selected[file.Path] {
			continue
		}
		delete(selected, file.Path)
		allowed[file.Path] = true
		deleted := file.PreviousPath
		if file.Status == "D" {
			deleted = file.Path
		}
		if deleted != "" {
			allowed[deleted] = true
			fmt.Fprintf(&removes, "0 %s\t%s%c", strings.Repeat("0", len(file.object)), deleted, 0)
		}
		if file.Status != "D" {
			fmt.Fprintf(&updates, "%s %s\t%s%c", file.mode, file.object, file.Path, 0)
		}
	}
	if len(selected) != 0 {
		return "", core.ErrInvalidState
	}
	dir, err := os.MkdirTemp("", "blux-selection-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	date, err := git(ctx, root, "show", "-s", "--format=%aI", head)
	if err != nil {
		return "", err
	}
	run := func(input []byte, args ...string) (string, error) {
		prefix := []string{"-C", root, "-c", "core.hooksPath=" + os.DevNull, "-c", "user.name=Blux", "-c", "user.email=blux@localhost", "-c", "commit.gpgsign=false"}
		cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "index"), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	if _, err = run(nil, "read-tree", base); err != nil {
		return "", err
	}
	if _, err = run(append(removes.Bytes(), updates.Bytes()...), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	tree, err := run(nil, "write-tree")
	if err != nil {
		return "", err
	}
	// A file replacing a directory must not implicitly remove unselected files.
	modified, err := exec.CommandContext(ctx, "git", "-C", root, "diff-tree", "--no-commit-id", "--name-only", "-r", "--no-renames", "-z", base, tree, "--").Output()
	if err != nil {
		return "", err
	}
	for _, name := range bytes.Split(bytes.TrimSuffix(modified, []byte{0}), []byte{0}) {
		if !allowed[string(name)] {
			return "", core.ErrInvalidState
		}
		delete(allowed, string(name))
	}
	if len(allowed) != 0 {
		return "", core.ErrInvalidState
	}
	commit, err := run(nil, "commit-tree", tree, "-p", base, "-m", "Blux file selection from "+head)
	if err != nil {
		return "", err
	}
	_, err = run(nil, "update-ref", "refs/blux/selections/"+commit, commit)
	return commit, err
}
