package flow

import (
	"blux/internal/agent"
	"blux/internal/artifact"
	"blux/internal/core"
	"blux/internal/flowmodel"
	"blux/internal/store"
	"blux/internal/workspace"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// Advance only durable assignments. A worker restart can replay CreateTask using
// exactly the same frozen base, upstream versions, adapter and dispatch key.
func (s Service) advanceSmart(ctx context.Context, d flowmodel.Delivery) error {
	before := flowmodel.Clone(d)
	if d.Status == "blocked" {
		t, err := s.Store.GetTask(ctx, d.BlockedTask)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if t.Task.Status == "failed" {
			return nil
		}
		d.Status, d.Reason, d.BlockedTask = "running", "", ""
	}
	for i := range d.Run.Phases {
		p := &d.Run.Phases[i]
		if p.Status == "accepted" || p.Status == "awaiting_review" {
			continue
		}
		ready := true
		for _, prior := range d.Run.Phases {
			if slices.Contains(p.DependsOn, prior.ID) && prior.Status != "accepted" {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		u := &p.Attempt.Units[0]
		if u.Adapter == "" {
			p.Status = "running"
			if err := s.freezeAssignment(ctx, &d, p); err != nil {
				if !errors.Is(err, core.ErrInvalidState) {
					return err
				}
				d.Status, d.Reason = "blocked", err.Error()
				break
			}
			p.Status = "running"
			continue
		}
		if u.TaskID == "" {
			if err := s.dispatch(ctx, &d, p); err != nil {
				return err
			}
		}
		deleted, err := s.Store.IsTaskDeleted(ctx, u.TaskID)
		if err != nil {
			return err
		}
		if deleted {
			d.Status, d.Reason = "blocked", p.Name+"的工单已删除，请人工处理。"
			break
		}
		task, err := s.Store.GetTask(ctx, u.TaskID)
		if err != nil {
			return err
		}
		u.Status = task.Task.Status
		u.Adapter = task.Task.Adapter
		if u.Status == "failed" {
			redispatched, err := s.redispatch(ctx, &d, p)
			if err != nil {
				return err
			}
			if !redispatched {
				break
			}
			continue
		}
		if u.Status != "completed" {
			continue
		}
		v, err := s.Store.CapturedTurnSnapshot(ctx, task.Task.CurrentTurnID)
		if errors.Is(err, core.ErrNotFound) {
			d.Status, d.Reason = "blocked", p.Name+"缺少本次执行的原始产物快照。"
			break
		}
		if err != nil {
			return err
		}
		u.SnapshotID = v.ID
		p.Checkpoint = u.BaseCommit
		if p.Role == "worker" && v.Commit != "" {
			p.Checkpoint = v.Commit
		}
		if p.Role != "worker" && v.Diff != "" {
			d.Status, d.Reason = "blocked", p.Name+"修改了候选代码，请重做此阶段。"
			break
		}
		target, feedback, err := s.inspectOutput(&d, p, v)
		if err != nil {
			if !errors.Is(err, core.ErrInvalidState) {
				return err
			}
			d.Status, d.Reason = "blocked", err.Error()
			break
		}
		if feedback != "" {
			if d.Reworks >= 3 || d.CandidateBase != "" || d.Strategy == "review" || d.Strategy == "verify" {
				d.Status, d.Reason = "awaiting_rework", feedback
				break
			}
			d.Reworks++
			resetSmart(&d, target, feedback)
			break
		}
		u.Integrated = true
		p.Status = "accepted"
		if p.Gate {
			p.Status = "awaiting_review"
		}
	}
	if d.Status == "running" {
		complete := true
		for _, p := range d.Run.Phases {
			if p.Status != "accepted" {
				complete = false
				break
			}
		}
		if complete {
			d.Status = "completed"
		}
	}
	if reflect.DeepEqual(before, d) {
		return nil
	}
	_, err := s.Store.SaveDelivery(ctx, d, before.Revision, "", "", "")
	if err == nil && before.Run.Generation != d.Run.Generation {
		s.stopInvalidated(ctx, before.Run, d.Run)
	}
	return err
}

func (s Service) dispatch(ctx context.Context, d *flowmodel.Delivery, p *flowmodel.Phase) error {
	u := &p.Attempt.Units[0]
	parents := []string{}
	for _, prior := range d.Run.Phases {
		if slices.Contains(p.DependsOn, prior.ID) {
			parents = append(parents, prior.Attempt.Units[0].TaskID)
		}
	}
	goal, criteria := u.Prompt, u.Prompt
	if d.Plan != nil {
		for _, t := range d.Plan.Tickets {
			if p.ID == "t-"+t.ID {
				goal, criteria = t.Prompt, t.Acceptance
				break
			}
		}
	}
	prompt := d.Goal + "\n\n" + u.Prompt + "\n冻结基线：" + u.BaseCommit + "\n从 .agent/inputs.json 和 .agent/inputs/ 读取已冻结的上游产物。"
	if p.Role != "planner" && d.CandidateBase == "" {
		for _, approved := range d.Run.Phases {
			if approved.Role == "planner" && approved.Status == "accepted" {
				prompt += fmt.Sprintf("\n用户已在 Blux 确认此计划（已批准的计划快照 %s），现在派发工单 %s。规格中“待确认”的措辞记录的是草案生成时的状态。按已确认的范围直接执行并交付结果，无需再次征求确认。", approved.Attempt.Units[0].SnapshotID, p.ID)
				break
			}
		}
	}
	if p.Role == "reviewer" || p.Role == "verifier" {
		prompt += fmt.Sprintf("\n在当前项目工作树中用 git diff %s %s -- 审查本次候选与项目基线之间的精确差异。", d.BaseCommit, u.BaseCommit)
		for _, up := range d.Run.Phases {
			if (up.Role != "worker" && up.Role != "discussant") || d.CandidateBase != "" {
				continue
			}
			w := up.Attempt.Units[0]
			prompt += fmt.Sprintf("\n工单 %s（%s），task %s，snapshot %s，差异 %s..%s。", strings.TrimPrefix(up.ID, "t-"), up.Name, w.TaskID, w.SnapshotID, w.BaseCommit, up.Checkpoint)
		}
		for _, id := range d.InputSnapshotIDs {
			v, err := s.Store.Snapshot(ctx, id)
			if err != nil {
				return err
			}
			if v.Commit == "" || v.Diff == "" {
				continue
			}
			t, err := s.Store.GetTask(ctx, v.TaskID)
			if err != nil {
				return err
			}
			repo, err := (workspace.Manager{DataDir: s.DataDir}).Resolve(ctx, d.RepositoryPath, t.Task.BaseCommit)
			if err != nil {
				return err
			}
			prompt += fmt.Sprintf("\n钉住的快照 %s，工单 %s：在 Git 仓库 %q 中执行 git diff %s %s -- 查看此版本的准确差异。", id, v.TaskID, repo.Root, t.Task.BaseCommit, v.Commit)
		}
	}
	if p.Role != "worker" {
		prompt += "\n此角色仅向指定产物目录写结果，保持项目源代码不变。"
	}
	if d.CandidateBase != "" {
		prompt += "\n用户已选定本轮重验的文件，当前工单 " + p.ID + " 直接执行独立重验或汇总，无需再次确认选区；尚未授权接收到源项目。本轮文件：" + strings.Join(d.SelectedPaths, "、") + "。以当前工作树中的候选版本为准。"
	}
	if p.Attempt.Feedback != "" {
		prompt += "\n上轮反馈：" + p.Attempt.Feedback
	}
	task, _, err := s.Store.CreateTask(ctx, core.CreateTaskInput{Title: p.Name, Prompt: prompt, Adapter: u.Adapter, RepositoryPath: d.RepositoryPath, BaseCommit: u.BaseCommit, Mode: p.Mode, ContextIDs: d.ContextIDs, InputArtifacts: u.InputArtifacts, DeliveryID: d.ID, FlowGeneration: d.Run.Generation, CollaborationID: d.CollaborationID, TicketDependencies: parents, TicketGoal: goal, AcceptanceCriteria: criteria, IdempotencyKey: "flow:" + u.ID})
	if err != nil {
		return err
	}
	u.TaskID, u.Status = task.Task.ID, "queued"
	return nil
}

func (s Service) chooseAgent(ctx context.Context, d *flowmodel.Delivery, preferred string, excluded []string) (string, error) {
	health, err := s.Store.EngineHealth(ctx)
	if err != nil {
		return "", err
	}
	tasks, err := s.Store.ListTasks(ctx)
	if err != nil {
		return "", err
	}
	load := map[string]int{}
	for _, t := range tasks {
		if t.Status == "running" || t.Status == "queued" {
			load[t.Adapter]++
		}
	}
	for _, p := range d.Run.Phases {
		for _, u := range p.Attempt.Units {
			if u.Adapter != "" && u.TaskID == "" {
				load[u.Adapter]++
			}
		}
	}
	agents := slices.Clone(d.Agents)
	score := func(id string) int {
		n := load[id]
		if id == preferred {
			n -= 100
		}
		if health[id].Status == "degraded" {
			n += 1000
		}
		return n
	}
	sort.SliceStable(agents, func(i, j int) bool { return score(agents[i]) < score(agents[j]) })
	probe := s.Probe
	if probe == nil {
		probe = func(ctx context.Context, id string) error {
			return agent.Probe(ctx, id, os.Getenv("BLUX_"+strings.ToUpper(id)+"_BIN"))
		}
	}
	for _, id := range agents {
		if slices.Contains(excluded, id) || health[id].Status == "unavailable" {
			continue
		}
		if err := probe(ctx, id); err == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("候选列表没有可用的本地 Agent: %w", core.ErrInvalidState)
}

func (s Service) redispatch(ctx context.Context, d *flowmodel.Delivery, p *flowmodel.Phase) (bool, error) {
	u := &p.Attempt.Units[0]
	ticket, err := s.Store.Ticket(ctx, u.TaskID)
	if err != nil {
		return false, err
	}
	u.Status = ticket.Status
	if ticket.Status == "paused" || ticket.Status == "pausing" || ticket.Status == "cancelling" {
		return true, nil
	}
	last := ticket.Attempts[len(ticket.Attempts)-1]
	if !last.SafeToRetry {
		d.BlockedTask = u.TaskID
		d.Status, d.Reason = "blocked", p.Name+"执行中止，需人工处理："+last.ErrorCode
		return false, nil
	}
	tried := make([]string, 0, len(ticket.Attempts))
	for _, a := range ticket.Attempts {
		tried = append(tried, a.Adapter)
	}
	id, err := s.chooseAgent(ctx, d, "", tried)
	if errors.Is(err, core.ErrInvalidState) {
		d.BlockedTask = u.TaskID
		d.Status, d.Reason = "blocked", p.Name+"已用尽可改派的 Agent："+last.ErrorCode
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = s.Store.ActOnTicket(ctx, u.TaskID, "flow-reassign:"+last.ID, store.TicketAction{Action: "retry", Adapter: id, Automatic: true, ExpectedAttemptID: last.ID})
	if err != nil {
		return false, err
	}
	u.PreviousTasks = append(u.PreviousTasks, flowmodel.Execution{TaskID: u.TaskID, Adapter: last.Adapter, Reason: last.ErrorCode})
	u.Status, u.Adapter = "queued", id
	return true, nil
}

func (s Service) freezeAssignment(ctx context.Context, d *flowmodel.Delivery, p *flowmodel.Phase) error {
	u := &p.Attempt.Units[0]
	u.InputArtifacts = []string{}
	commits := []string{}
	inputIDs := map[string]bool{}
	if p.Role == "planner" || len(p.DependsOn) == 0 || d.Strategy == "review" || d.Strategy == "verify" {
		for _, id := range d.InputSnapshotIDs {
			v, err := s.Store.Snapshot(ctx, id)
			if err != nil {
				return err
			}
			for _, a := range v.Artifacts {
				inputIDs[a.ID] = true
			}
		}
	}
	for _, up := range d.Run.Phases {
		if !slices.Contains(p.DependsOn, up.ID) {
			continue
		}
		if up.Checkpoint != d.BaseCommit && !slices.Contains(commits, up.Checkpoint) {
			commits = append(commits, up.Checkpoint)
		}
		if d.CandidateBase != "" && up.Role != "planner" && up.Role != "reviewer" && up.Role != "verifier" {
			continue
		}
		v, err := s.Store.Snapshot(ctx, up.Attempt.Units[0].SnapshotID)
		if err != nil {
			return err
		}
		for _, a := range v.Artifacts {
			inputIDs[a.ID] = true
		}
	}
	for id := range inputIDs {
		u.InputArtifacts = append(u.InputArtifacts, id)
	}
	sort.Strings(u.InputArtifacts)
	repo, err := (workspace.Manager{DataDir: s.DataDir}).Resolve(ctx, d.RepositoryPath, d.BaseCommit)
	if err != nil {
		return err
	}
	if d.InputHead == "" && d.CandidateBase == "" {
		external := []string{}
		for _, id := range d.InputSnapshotIDs {
			v, err := s.Store.Snapshot(ctx, id)
			if err != nil {
				return err
			}
			if v.Diff == "" || v.Commit == "" {
				continue
			}
			t, err := s.Store.GetTask(ctx, v.TaskID)
			if err != nil {
				return err
			}
			baseTree, err := git(ctx, repo.Root, "rev-parse", d.BaseCommit+"^{tree}")
			if err != nil {
				return err
			}
			original, err := git(ctx, repo.Root, "rev-parse", t.Task.BaseCommit+"^{tree}")
			if err != nil || original != baseTree {
				return fmt.Errorf("快照 %s 的代码基线与本项目冻结版本不同，请重新选择版本: %w", id, core.ErrInvalidState)
			}
			commit := v.Commit
			if t.Task.BaseCommit != d.BaseCommit {
				tree, err := git(ctx, repo.Root, "rev-parse", commit+"^{tree}")
				if err != nil {
					return err
				}
				commit, err = git(ctx, repo.Root, "commit-tree", tree, "-p", d.BaseCommit, "-m", "Pinned input "+id)
				if err != nil {
					return err
				}
			}
			if !slices.Contains(external, commit) {
				external = append(external, commit)
			}
		}
		d.InputHead, err = mergeInputs(ctx, repo.Root, d.BaseCommit, external, "pinned-inputs")
		if err != nil {
			return err
		}
	}
	u.BaseCommit = d.InputHead
	switch {
	case d.CandidateBase != "" || p.Role == "summary":
		u.BaseCommit = d.Head
	default:
		matched := false
		if p.Role == "verifier" {
			for _, up := range d.Run.Phases {
				if up.Role == "reviewer" && up.Attempt.Units[0].BaseCommit != "" {
					u.BaseCommit = up.Attempt.Units[0].BaseCommit
					matched = true
					break
				}
			}
		}
		if !matched {
			u.BaseCommit, err = mergeInputs(ctx, repo.Root, u.BaseCommit, commits, p.ID)
			if err != nil {
				return err
			}
		}
		if p.Role == "reviewer" || p.Role == "verifier" {
			d.Head = u.BaseCommit
			if _, err := git(ctx, repo.Root, "update-ref", "refs/blux/deliveries/"+d.ID, d.Head); err != nil {
				return err
			}
		}
	}
	preferred := p.Agent
	if preferred == "" && p.Role == "planner" {
		preferred = d.Adapter
	}
	selected, err := s.chooseAgent(ctx, d, preferred, nil)
	if err != nil {
		return err
	}
	u.Adapter = selected
	return nil
}

func mergeInputs(ctx context.Context, repo, base string, commits []string, label string) (string, error) {
	for _, c := range commits {
		if c == base {
			continue
		}
		if _, e := git(ctx, repo, "merge-base", "--is-ancestor", base, c); e == nil {
			base = c
			continue
		}
		if _, e := git(ctx, repo, "merge-base", "--is-ancestor", c, base); e == nil {
			continue
		}
		tree, e := git(ctx, repo, "merge-tree", "--write-tree", base, c)
		if e != nil {
			return "", fmt.Errorf("%s 的上游代码存在合并冲突，请检查对应工单或所选版本: %w", label, core.ErrInvalidState)
		}
		base, e = git(ctx, repo, "commit-tree", tree, "-p", base, "-p", c, "-m", "Integrate dependencies for "+label)
		if e != nil {
			return "", e
		}
	}
	return base, nil
}

func (s Service) inspectOutput(d *flowmodel.Delivery, p *flowmodel.Phase, v store.Snapshot) (int, string, error) {
	read := func(name string) ([]byte, error) {
		for _, a := range v.Artifacts {
			if a.Path == name {
				b, err := os.ReadFile((artifact.Service{DataDir: s.DataDir}).Blob(a.Hash))
				if err != nil {
					return nil, err
				}
				if len(strings.TrimSpace(string(b))) == 0 {
					break
				}
				return b, nil
			}
		}
		return nil, fmt.Errorf("%s 缺少有效产物 %s: %w", p.Name, name, core.ErrInvalidState)
	}
	switch p.Role {
	case "planner":
		if _, err := read("Spec.md"); err != nil {
			return 0, "", err
		}
		b, err := read("Plan.json")
		if err != nil {
			return 0, "", err
		}
		var plan flowmodel.Plan
		if json.Unmarshal(b, &plan) != nil {
			return 0, "", fmt.Errorf("Plan.json 无法解析: %w", core.ErrInvalidState)
		}
		nodes, err := plan.Expand(d.Strategy, d.Agents)
		if err != nil {
			return 0, "", err
		}
		if d.Strategy == "chain" {
			for i := 1; i < len(plan.Tickets); i++ {
				prev := plan.Tickets[i-1].ID
				if !slices.Contains(plan.Tickets[i].DependsOn, prev) {
					plan.Tickets[i].DependsOn = append(plan.Tickets[i].DependsOn, prev)
				}
			}
		}
		d.Plan = &plan
		d.Template.Phases = append(d.Template.Phases[:1], nodes...)
	case "reviewer", "verifier":
		name := "Review"
		if p.Role == "verifier" {
			name = "Verification"
		}
		if _, err := read(name + ".md"); err != nil {
			return 0, "", err
		}
		b, err := read(name + ".json")
		if err != nil {
			return 0, "", err
		}
		var verdict struct {
			Passed   *bool  `json:"passed"`
			Feedback string `json:"feedback"`
			TicketID string `json:"ticket_id"`
		}
		if json.Unmarshal(b, &verdict) != nil || verdict.Passed == nil || (!*verdict.Passed && strings.TrimSpace(verdict.Feedback) == "") {
			return 0, "", fmt.Errorf("%s.json 缺少明确结论或返工依据: %w", name, core.ErrInvalidState)
		}
		if !*verdict.Passed {
			if d.CandidateBase != "" || d.Strategy == "review" || d.Strategy == "verify" {
				for i, node := range d.Run.Phases {
					if node.ID == p.ID {
						return i, verdict.Feedback, nil
					}
				}
			}
			target := -1
			for i, node := range d.Run.Phases {
				if node.Role != "worker" && node.Role != "discussant" {
					continue
				}
				if (target < 0 && verdict.TicketID == "") || node.ID == "t-"+verdict.TicketID || node.ID == verdict.TicketID || node.Attempt.Units[0].TaskID == verdict.TicketID {
					target = i
				}
			}
			if target < 0 {
				return 0, "", fmt.Errorf("审查结果引用了未识别的工单: %w", core.ErrInvalidState)
			}
			return target, verdict.Feedback, nil
		}
	case "critic", "discussant", "summary":
		n := map[string]string{"critic": "CrossCheck.md", "discussant": "Discussion.md", "summary": "Summary.md"}
		b, err := read(n[p.Role])
		if err != nil {
			return 0, "", err
		}
		if p.Role == "summary" {
			d.Summary = string(b)
		}
	}
	return 0, "", nil
}

func resetSmart(d *flowmodel.Delivery, target int, feedback string) {
	old := d.Run
	d.History = append(d.History, old)
	if old.Phases[target].Role == "planner" {
		d.Template = flowmodel.Planning(d.Strategy, d.Agents)
		d.Plan = nil
	}
	next := flowmodel.NewRun(d.Template, old.Generation+1)
	changed := map[string]bool{old.Phases[target].ID: true}
	for i, p := range next.Phases {
		for _, id := range p.DependsOn {
			if changed[id] {
				changed[p.ID] = true
			}
		}
		if !changed[p.ID] {
			next.Phases[i] = old.Phases[i]
			continue
		}
		next.Phases[i].Attempt.Number = old.Phases[i].Attempt.Number + 1
	}
	next.Phases[target].Attempt.Feedback = feedback
	d.Run = next
	d.Status = "running"
	d.Reason = ""
	d.Summary = ""
	d.BlockedTask = ""
	if old.Phases[target].Role == "planner" || old.Phases[target].Role == "worker" || old.Phases[target].Role == "discussant" {
		d.Head = d.BaseCommit
		d.SelectedPaths = nil
		d.CandidateBase = ""
		d.SourceHead = ""
	}
	d.AcceptanceCommit = ""
	d.AcceptanceBase = ""
	d.IntegrationPath = ""
}
