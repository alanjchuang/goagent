// worker.go 实现 Worker-as-Tool：把一个 worker agent 包装成 supervisor 可调用的工具。
//
// 对应 Python 版的 Agent-as-Tool / agent_function_schema：worker 暴露为一个普通函数工具，
// supervisor 像调用工具一样调用 worker，工具内部启动 worker 的 agent 循环并返回字符串结果。
package agent

import (
	"context"
	"fmt"

	"github.com/alanjchuang/goagent/internal/config"
	"github.com/alanjchuang/goagent/internal/tools"
)

// workerTool 把一个 worker AgentConfig 适配成 tools.Tool。
type workerTool struct {
	cfg *config.AgentConfig
}

func (w *workerTool) Name() string { return w.cfg.Name }

func (w *workerTool) Description() string {
	return fmt.Sprintf("调用子 Agent %q 完成专项任务。该子 Agent 的职责: %s",
		w.cfg.Name, w.cfg.Description)
}

func (w *workerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "交给该子 Agent 处理的具体任务描述",
			},
		},
		"required": []string{"task"},
	}
}

// Execute 启动 worker 子 agent 并运行给定任务。
func (w *workerTool) Execute(args map[string]any) (string, error) {
	task := ""
	if v, ok := args["task"].(string); ok {
		task = v
	}
	if task == "" {
		task = w.cfg.Description
	}

	sub, err := New(w.cfg)
	if err != nil {
		return "", fmt.Errorf("创建子 Agent %q 失败: %w", w.cfg.Name, err)
	}
	fmt.Printf("\n╭── 进入子 Agent: %s ──\n", w.cfg.Name)
	result, err := sub.Run(context.Background(), task)
	fmt.Printf("╰── 子 Agent %s 完成 ──\n", w.cfg.Name)
	if err != nil {
		return fmt.Sprintf("子 Agent %q 执行出错: %v", w.cfg.Name, err), nil
	}
	return result, nil
}

var _ tools.Tool = (*workerTool)(nil)

// registerWorkers 加载 supervisor 配置中的 worker_agents，并把每个 worker 注册为工具。
func registerWorkers(reg *tools.Registry, supervisor *config.AgentConfig) error {
	for _, ref := range supervisor.WorkerAgents {
		if ref.Path == "" {
			continue
		}
		wcfg, err := config.LoadAgentConfig(ref.Path)
		if err != nil {
			return fmt.Errorf("加载 worker %q 失败: %w", ref.Path, err)
		}
		reg.Register(&workerTool{cfg: wcfg})
	}
	return nil
}
