package flow

import (
	"blux/internal/core"
	"blux/internal/flowmodel"
	"blux/internal/workspace"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func (s Service) Accept(ctx context.Context, id, key string, generation int64) (d flowmodel.Delivery, err error) {
	if key == "" {
		return d, core.ErrInvalidState
	}
	unlock, e := s.lock(id)
	if e != nil {
		return d, core.ErrConflict
	}
	defer unlock()
	lock, e := platformAcceptLock(s.DataDir)
	if e != nil {
		return d, core.ErrConflict
	}
	defer lock()
	h := hash(struct{ Generation int64 }{generation})
	if old, found, e := s.Store.FlowReceipt(ctx, id+":accept", key, h); found || e != nil {
		return old, e
	}
	d, err = s.Store.Delivery(ctx, id)
	if err != nil {
		return
	}
	if d.Run.Generation != generation {
		return d, core.ErrStaleGeneration
	}
	if d.Status != "completed" || (d.Orchestration != "smart" && d.Adapter != "codex") {
		return d, core.ErrInvalidState
	}
	if d.Orchestration == "smart" {
		if err = s.verifySource(ctx, d, d.AcceptanceCommit != ""); err != nil {
			return d, err
		}
	}
	expected := d.Revision
	save := func() (flowmodel.Delivery, error) {
		return s.Store.SaveDelivery(ctx, d, expected, id+":accept", key, h)
	}
	m := workspace.Manager{DataDir: s.DataDir}
	repo, e := m.Resolve(ctx, d.RepositoryPath, d.BaseCommit)
	if e != nil {
		return d, e
	}
	if m.IsSnapshot(d.RepositoryPath, repo.Root) {
		recovering := d.AcceptanceCommit == d.Head
		d.AcceptanceCommit = d.Head
		d.AcceptanceBase = d.BaseCommit
		d, err = s.Store.SaveDelivery(ctx, d, expected, "", "", "")
		if err != nil {
			return d, err
		}
		expected = d.Revision
		if d.Orchestration == "smart" {
			if e = s.verifySource(ctx, d, recovering); e != nil {
				return d, e
			}
		}
		if e = m.ApplySnapshot(ctx, d.RepositoryPath, d.BaseCommit, d.Head, recovering); e != nil {
			d.Reason = e.Error()
		} else {
			d.Status = "accepted"
			d.Reason = ""
		}
		return save()
	}
	if d.AcceptanceCommit != "" {
		head, e := git(ctx, d.RepositoryPath, "rev-parse", "HEAD")
		if e == nil && head == d.AcceptanceCommit {
			d.Status = "accepted"
			return save()
		}
	}
	status, e := git(ctx, d.RepositoryPath, "status", "--porcelain=v1", "--untracked-files=all")
	if e != nil {
		return d, e
	}
	if status != "" {
		d.Reason = "源项目存在未提交变更，请先处理。"
		return save()
	}
	base, e := git(ctx, d.RepositoryPath, "rev-parse", "HEAD")
	if e != nil {
		return d, e
	}
	if d.Orchestration == "smart" && base != d.BaseCommit {
		return d, core.ErrConflict
	}
	path := filepath.Join(s.DataDir, "integrations", fmt.Sprintf("%s-g%d", id, generation))
	d.IntegrationPath = path
	if _, e = os.Stat(path); os.IsNotExist(e) {
		if _, e = git(ctx, d.RepositoryPath, "worktree", "add", "--detach", path, base); e != nil {
			return d, e
		}
		if _, e = git(ctx, path, "merge", "--no-ff", "-m", "Accept Blux delivery "+id, d.Head); e != nil {
			d.Reason = "接收合并冲突，请在整合区解决并提交：" + path
			return save()
		}
	} else {
		status, e = git(ctx, path, "status", "--porcelain=v1")
		if e != nil || status != "" {
			d.Reason = "整合区仍有未解决变更：" + path
			return save()
		}
		if _, e = git(ctx, path, "merge-base", "--is-ancestor", d.Head, "HEAD"); e != nil {
			d.Reason = "整合区未包含当前交付版本。"
			return save()
		}
	}
	d.AcceptanceCommit, err = git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return
	}
	if d.Orchestration == "smart" {
		actual, e := git(ctx, path, "rev-parse", "HEAD^{tree}")
		if e != nil {
			return d, e
		}
		reviewed, e := git(ctx, path, "rev-parse", d.Head+"^{tree}")
		if e != nil {
			return d, e
		}
		if actual != reviewed {
			return d, fmt.Errorf("整合区与已验证候选不一致: %w", core.ErrConflict)
		}
	}
	d.AcceptanceBase = base
	d.Reason = ""
	d, err = s.Store.SaveDelivery(ctx, d, expected, "", "", "")
	if err != nil {
		return
	}
	expected = d.Revision
	head, e := git(ctx, d.RepositoryPath, "rev-parse", "HEAD")
	status, se := git(ctx, d.RepositoryPath, "status", "--porcelain=v1", "--untracked-files=all")
	if e != nil || se != nil || head != base || status != "" {
		d.Reason = "整合期间源项目发生变化，未接收。"
		return save()
	}
	if _, e = git(ctx, d.RepositoryPath, "merge", "--ff-only", d.AcceptanceCommit); e != nil {
		d.Reason = "源项目无法快进，请检查整合区。"
		return save()
	}
	d.Status = "accepted"
	return save()
}
