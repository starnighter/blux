package flowmodel

import (
	"blux/internal/core"
	"fmt"
	"slices"
	"strings"
)

type Ticket struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Prompt     string   `json:"prompt"`
	Acceptance string   `json:"acceptance"`
	DependsOn  []string `json:"depends_on"`
	Agent      string   `json:"agent,omitempty"`
}
type Plan struct {
	Tickets []Ticket `json:"tickets"`
}

func Planning(strategy string, agents []string) Template {
	prompt := fmt.Sprintf("按 %s 策略拆解目标。候选 Agent：%s。把可审阅的规格写入 Spec.md，另写 Plan.json。格式为 {\"tickets\":[{\"id\":\"唯一英文标识\",\"title\":\"标题\",\"prompt\":\"具体工作\",\"acceptance\":\"可检查的完成条件\",\"depends_on\":[],\"agent\":\"候选 Agent（可省略）\"}]}。引用其他工单的 id 定义依赖，最多 16 个工单。fan_out 或 discussion 应有两项以上可独立开始的分工；chain 按数组顺序串行。将这两个文件放到指定的产物目录，计划获确认前不实施。", strategy, strings.Join(agents, ", "))
	if strategy == "review" || strategy == "verify" {
		prompt += "此专项仅做审查或验证。列出需要检查的范围和验收点，不安排修改源码。"
	}
	return Template{ID: "smart", Version: 1, Name: "智能编排", Phases: []PhaseTemplate{{ID: "plan", Name: "规划", Mode: "spec", Role: "planner", Gate: true, Units: []UnitTemplate{{ID: "plan", Name: "制定计划", Prompt: prompt}}}}}
}

func (p Plan) Expand(strategy string, agents []string) ([]PhaseTemplate, error) {
	if len(p.Tickets) == 0 || len(p.Tickets) > 16 {
		return nil, fmt.Errorf("计划需要 1–16 个工单: %w", core.ErrInvalidState)
	}
	ids := map[string]bool{}
	for _, t := range p.Tickets {
		if !identifier.MatchString(t.ID) || ids[t.ID] || strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Prompt) == "" || strings.TrimSpace(t.Acceptance) == "" {
			return nil, fmt.Errorf("工单标识重复或缺少标题、说明、验收条件: %w", core.ErrInvalidState)
		}
		if t.Agent != "" && !slices.Contains(agents, t.Agent) {
			return nil, fmt.Errorf("工单 %s 引用了候选列表之外的 Agent: %w", t.ID, core.ErrInvalidState)
		}
		ids[t.ID] = true
	}
	emitted := map[string]bool{}
	out := []PhaseTemplate{}
	all := []string{"plan"}
	independent := 0
	for len(emitted) < len(p.Tickets) {
		before := len(emitted)
		for i, t := range p.Tickets {
			if emitted[t.ID] {
				continue
			}
			deps := slices.Clone(t.DependsOn)
			if strategy == "chain" && i > 0 && !slices.Contains(deps, p.Tickets[i-1].ID) {
				deps = append(deps, p.Tickets[i-1].ID)
			}
			ready := true
			for _, dep := range deps {
				if !ids[dep] {
					return nil, fmt.Errorf("工单 %s 引用了不存在的工单 %s: %w", t.ID, dep, core.ErrInvalidState)
				}
				if !emitted[dep] {
					ready = false
				}
			}
			if !ready {
				continue
			}
			if len(deps) == 0 {
				independent++
			}
			parents := []string{"plan"}
			for _, dep := range deps {
				parents = append(parents, "t-"+dep)
			}
			role, mode, prompt := "worker", "development", t.Prompt+"\n验收条件："+t.Acceptance
			if strategy == "discussion" {
				role, mode = "discussant", "explore"
				prompt += "\n独立论证，把观点、依据和疑点写入产物 Discussion.md。"
			}
			ph := phase("t-"+t.ID, t.Title, role, mode, parents, prompt)
			ph.Agent = t.Agent
			out = append(out, ph)
			all = append(all, ph.ID)
			emitted[t.ID] = true
		}
		if len(emitted) == before {
			return nil, fmt.Errorf("工单依赖含环，无法执行: %w", core.ErrInvalidState)
		}
	}
	if (strategy == "fan_out" || strategy == "discussion") && independent < 2 {
		return nil, fmt.Errorf("所选策略需要至少两项可以并行开始的工单: %w", core.ErrInvalidState)
	}
	if strategy == "discussion" {
		cross := []string{}
		for i, t := range p.Tickets {
			peer := p.Tickets[(i+1)%len(p.Tickets)]
			node := phase("cross-"+t.ID, "交叉检查 · "+t.Title, "critic", "explore", []string{"plan", "t-" + t.ID, "t-" + peer.ID}, "比较本方与对方的讨论快照，列出分歧、可核实依据和结论，把交叉审阅写入 CrossCheck.md。")
			node.Agent = t.Agent
			out = append(out, node)
			cross = append(cross, node.ID)
		}
		all = append(all, cross...)
	}
	if strategy == "review" || strategy == "verify" {
		return Checks(strategy, []string{"plan"}), nil
	}
	return append(out, Checks("auto", all)...), nil
}

func Checks(strategy string, all []string) []PhaseTemplate {
	review := phase("review", "独立审查", "reviewer", "explore", all, "依据冻结规格逐项审查方案、实际差异及上游产物。写 Review.md（列出问题、证据及关联工单），另写 Review.json，含 passed 布尔值、feedback 文本和可选 ticket_id。")
	verify := phase("verify", "独立验证", "verifier", "explore", all, "独立执行验收检查；记录命令、结果、限制和证据到 Verification.md。另写 Verification.json，含 passed 布尔值、feedback 文本和可选 ticket_id。")
	checks := []PhaseTemplate{review, verify}
	if strategy == "review" {
		checks = []PhaseTemplate{review}
	}
	if strategy == "verify" {
		checks = []PhaseTemplate{verify}
	}
	deps := slices.Clone(all)
	for _, p := range checks {
		deps = append(deps, p.ID)
	}
	summary := phase("summary", "结果汇总", "summary", "explore", deps, "把目标、各工单完成情况、产物、评审与验证证据、分歧和限制汇总至 Summary.md。")
	summary.Gate = true
	return append(checks, summary)
}

func phase(id, name, role, mode string, deps []string, prompt string) PhaseTemplate {
	return PhaseTemplate{ID: id, Name: name, Role: role, Mode: mode, DependsOn: deps, Units: []UnitTemplate{{ID: "execute", Name: name, Prompt: prompt}}}
}
