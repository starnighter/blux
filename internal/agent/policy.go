package agent

import (
	"blux/internal/core"
	"context"
)

// Probe checks the local CLI and protocol without starting a model turn.
func Probe(ctx context.Context, id, binary string) error {
	switch id {
	case "codex":
		_, err := ProbeCodex(ctx, binary)
		return err
	case "claude":
		_, err := ProbeClaude(ctx, binary)
		return err
	case "gemini", "deepseek":
		_, err := (Headless{ID: id, Binary: binary}).Probe(ctx)
		return err
	}
	return core.ErrInvalidState
}

type Policy struct {
	Sandbox  string `json:"sandbox"`
	Approval string `json:"approval"`
	Network  string `json:"network"`
	Files    string `json:"files"`
	Tools    string `json:"tools"`
	Evidence string `json:"evidence"`
}

func ExecutionPolicy(id string) Policy {
	switch id {
	case "codex":
		return Policy{"原生 workspace-write", "never，超出沙箱的审批请求会被拒绝", "开启", "工作树、产物及运行目录可写", "使用 CLI 的工作区工具", "启动和续聊时校验 CLI 回传的沙箱策略"}
	case "claude":
		return Policy{"原生沙箱，启动失败时终止", "dontAsk，关闭交互提示", "允许所有域名", "工作树、产物及运行目录可写", "限定读取、编辑、写入、Bash、搜索与网页工具", "按启动参数配置，校验 CLI 回传的权限模式"}
	case "deepseek":
		return Policy{"原生 workspace-write", "never", "由 CLI 控制", "工作区及指定产物、运行目录", "由 CLI 控制", "按启动参数配置"}
	case "gemini":
		return Policy{"macOS 原生 sandbox-exec", "yolo，沙箱内自动执行", "使用 permissive-open 配置", "工作目录及额外上下文目录", "由 CLI 控制", "按启动参数配置"}
	}
	return Policy{}
}
