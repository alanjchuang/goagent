// runapp.go 提供一行式应用入口 RunApp，对应 AgentLoom 的 runner.run_app。
//
// 用法（代码嵌入）：
//
//	result, err := agent.RunApp("applications/demo/workflows/demo_agent.yaml", "")
//
// 它封装了配置加载、agent 构建、运行的完整流程，便于把 agent 嵌入到自己的
// Go 程序中（loom create 生成的脚手架即调用本函数）。
package agent

import (
	"context"
	"fmt"

	"github.com/alanjchuang/goagent/internal/config"
)

// RunApp 从 YAML 配置启动并运行一个 agent，返回最终答复。
// taskOverride 非空时覆盖 YAML 的 description 作为任务文本。
func RunApp(yamlPath, taskOverride string) (string, error) {
	if config.C == nil {
		if _, err := config.Load(); err != nil {
			return "", fmt.Errorf("加载配置失败: %w", err)
		}
	}
	ac, err := config.LoadAgentConfig(yamlPath)
	if err != nil {
		return "", err
	}
	task := ac.Description
	if taskOverride != "" {
		task = taskOverride
	}
	ag, err := New(ac)
	if err != nil {
		return "", fmt.Errorf("创建 agent 失败: %w", err)
	}
	defer ag.Close()
	return ag.Run(context.Background(), task)
}
