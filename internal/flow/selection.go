package flow

import (
	"blux/internal/core"
	"blux/internal/flowmodel"
	"blux/internal/workspace"
	"context"

	"slices"
)

// The source must still match the entire reviewed baseline. A clean Git source
// pins the commit; directory snapshots pin the tree, since Freeze creates a commit.
func (s Service) verifySource(ctx context.Context, d flowmodel.Delivery, recovering bool) error {
	m := workspace.Manager{DataDir: s.DataDir}
	source, err := m.Freeze(ctx, d.RepositoryPath)
	if err != nil {
		return err
	}
	old, err := m.Resolve(ctx, d.RepositoryPath, d.BaseCommit)
	if err != nil {
		return err
	}
	if !m.IsSnapshot(d.RepositoryPath, old.Root) {
		if source.Root == old.Root && (source.BaseCommit == d.BaseCommit || recovering && source.BaseCommit == d.AcceptanceCommit) {
			return nil
		}
		return &core.ExecutionError{Code: "source_changed", Message: "源项目已变更，请以当前基线重新生成并验证交付。", Err: core.ErrConflict}
	}
	current, err := git(ctx, source.Root, "rev-parse", source.BaseCommit+"^{tree}")
	if err != nil {
		return err
	}
	base, err := git(ctx, old.Root, "rev-parse", d.BaseCommit+"^{tree}")
	if err != nil {
		return err
	}
	if current == base {
		return nil
	}
	if recovering && d.AcceptanceCommit != "" {
		target, err := git(ctx, old.Root, "rev-parse", d.AcceptanceCommit+"^{tree}")
		if err != nil {
			return err
		}
		if current == target {
			return nil
		}
	}
	return &core.ExecutionError{Code: "source_changed", Message: "源目录已变更，请以当前基线重新生成并验证交付。", Err: core.ErrConflict}
}

func (s Service) selectPaths(ctx context.Context, d *flowmodel.Delivery, paths []string) error {
	if d.Status != "completed" && !(d.CandidateBase != "" && d.Status == "awaiting_rework") {
		return core.ErrInvalidState
	}
	if err := s.verifySource(ctx, *d, false); err != nil {
		return err
	}
	repo, err := (workspace.Manager{DataDir: s.DataDir}).Resolve(ctx, d.RepositoryPath, d.BaseCommit)
	if err != nil {
		return err
	}
	full := d.SourceHead
	if full == "" {
		full = d.Head
	}
	head, err := workspace.SelectedCommit(ctx, repo.Root, d.BaseCommit, full, paths)
	if err != nil {
		return err
	}
	old := d.Run
	d.History = append(d.History, old)
	d.SourceHead = full
	d.CandidateBase = d.BaseCommit
	d.SelectedPaths = slices.Clone(paths)
	d.Head = head
	d.Orchestration = "smart"
	if len(d.Agents) == 0 {
		d.Agents = []string{d.Adapter}
	}
	// Keep the frozen spec as review context. Later worker reports may refer
	// to excluded files and are available only in the historical run.
	spec := []string{}
	for _, p := range old.Phases {
		if p.Role == "planner" || p.Mode == "spec" {
			for _, u := range p.Attempt.Units {
				if u.SnapshotID != "" {
					spec = append(spec, u.SnapshotID)
				}
			}
		}
	}
	if len(spec) > 0 {
		d.InputSnapshotIDs = spec
	}
	d.Template = flowmodel.Template{ID: "selection", Version: 1, Name: "所选文件重验", Phases: flowmodel.Checks("auto", nil)}
	d.Run = flowmodel.NewRun(d.Template, old.Generation+1)
	d.AcceptanceBase, d.AcceptanceCommit, d.IntegrationPath = "", "", ""
	d.Worktree, d.Branch = "", ""
	d.Status, d.Reason, d.Summary = "running", "", ""
	return nil
}
