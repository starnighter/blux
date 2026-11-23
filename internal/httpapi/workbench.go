package httpapi

import (
	"blux/internal/artifact"
	"blux/internal/core"
	"blux/internal/store"
	"blux/internal/workspace"
	"bytes"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

func (s *Server) registerWorkbenchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/snapshots/{snapshot}/revisions", s.reviseSnapshot)
	mux.HandleFunc("GET /api/v1/snapshots/{snapshot}/comments", s.snapshotComments)
	mux.HandleFunc("POST /api/v1/snapshots/{snapshot}/comments", s.createSnapshotComment)
}

func (s *Server) workbenchText(r *http.Request, v store.Snapshot, kind, name, side string) ([]byte, error) {
	if name == "" || !validWorkspacePath(name) || side != "new" && side != "old" {
		return nil, core.ErrInvalidState
	}
	const limit = 1 << 20
	var b []byte
	if kind == "file" {
		if v.Commit == "" {
			return nil, core.ErrNotFound
		}
		d, err := s.Store.GetTask(r.Context(), v.TaskID)
		if err != nil {
			return nil, err
		}
		repo, err := (workspace.Manager{DataDir: s.DataDir}).Resolve(r.Context(), d.Task.RepositoryPath, d.Task.BaseCommit)
		if err != nil {
			return nil, err
		}
		if side == "old" {
			v.Commit = d.Task.BaseCommit
		}
		data, _, cut, err := readWorkspaceFile(r.Context(), workspaceSource{detail: d, root: repo.Root, snapshot: &v}, name, limit)
		if err != nil {
			return nil, core.ErrNotFound
		}
		if cut {
			return nil, core.ErrInvalidState
		}
		b = data
	} else if kind == "artifact" && side == "new" {
		found := false
		for _, a := range v.Artifacts {
			if a.Path != name {
				continue
			}
			found = true
			if a.Size > limit {
				return nil, core.ErrInvalidState
			}
			f, err := os.Open((artifact.Service{DataDir: s.DataDir}).Blob(a.Hash))
			if err != nil {
				return nil, err
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, limit+1))
			if err != nil {
				return nil, err
			}
			b = data
			break
		}
		if !found {
			return nil, core.ErrNotFound
		}
	} else {
		return nil, core.ErrInvalidState
	}
	if len(b) > limit || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return nil, core.ErrInvalidState
	}
	return b, nil
}

func (s *Server) reviseSnapshot(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Kind    string  `json:"kind"`
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	if !readJSON(w, r, &p) {
		return
	}
	switch strings.ToLower(path.Ext(p.Path)) {
	case ".md", ".markdown", ".html", ".htm":
	default:
		writeError(w, 400, "unsupported_edit", "工作台支持编辑 Markdown 和 HTML。")
		return
	}
	if p.Content == nil || len(*p.Content) > 1<<20 || strings.ContainsRune(*p.Content, 0) {
		writeError(w, 400, "invalid_content", "只支持 1 MiB 以内的文本文件。")
		return
	}
	v, err := s.Store.Snapshot(r.Context(), r.PathValue("snapshot"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	if _, err = s.workbenchText(r, v, p.Kind, p.Path, "new"); err != nil {
		s.storeError(w, err)
		return
	}
	next, err := (artifact.Service{Store: s.Store, DataDir: s.DataDir}).Revise(r.Context(), v, p.Kind, p.Path, *p.Content)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, 201, next)
}

func (s *Server) snapshotComments(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Snapshot(r.Context(), r.PathValue("snapshot"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	comments, err := s.Store.LineComments(r.Context(), v.ID, r.URL.Query().Get("kind"), r.URL.Query().Get("path"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"comments": comments})
}

func (s *Server) createSnapshotComment(w http.ResponseWriter, r *http.Request) {
	var c store.LineComment
	if !readJSON(w, r, &c) {
		return
	}
	c.Body = strings.TrimSpace(c.Body)
	if len(c.Body) == 0 || utf8.RuneCountInString(c.Body) > 4000 || c.Line < 1 {
		writeError(w, 400, "invalid_comment", "请填写 1 至 4000 个字符的行评论。")
		return
	}
	v, err := s.Store.Snapshot(r.Context(), r.PathValue("snapshot"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	b, err := s.workbenchText(r, v, c.Kind, c.Path, c.Side)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if c.Line > 1+bytes.Count(b, []byte{'\n'}) {
		writeError(w, 400, "invalid_line", "此行不在所选版本中。")
		return
	}
	saved, err := s.Store.AddLineComment(r.Context(), v.ID, c)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, 201, saved)
}
