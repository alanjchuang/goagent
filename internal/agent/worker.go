// worker.go 实现 Worker-as-Tool：把一个 worker agent 包装成 supervisor 可调用的工具。
//
// 对应 Python 版的 Agent-as-Tool / agent_function_schema：worker 暴露为一个普通函数工具，
// supervisor 像调用工具一样调用 worker，工具内部启动 worker 的 agent 循环并返回字符串结果。
package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/alanjchuang/goagent/internal/config"
	"github.com/alanjchuang/goagent/internal/logging"
	"github.com/alanjchuang/goagent/internal/tools"
)

// maxWorkerConcurrency 是 batch 模式下的默认并发上限。
const maxWorkerConcurrency = 4

// workerTool 把一个 worker AgentConfig 适配成 tools.Tool。
type workerTool struct {
	cfg *config.AgentConfig
}

func (w *workerTool) Name() string { return w.cfg.Name }

func (w *workerTool) Description() string {
	return fmt.Sprintf("调用子 Agent %q 完成专项任务。该子 Agent 的职责: %s。"+
		"可传单个 task，或传 tasks 数组进行批量并发处理。",
		w.cfg.Name, w.cfg.Description)
}

func (w *workerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "交给该子 Agent 处理的单个任务描述",
			},
			"tasks": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "批量任务列表；提供时会并发执行并汇总结果（优先于 task）",
			},
		},
	}
}

// Execute 启动 worker 子 agent。支持单任务(task)与批量并发(tasks)两种模式。
func (w *workerTool) Execute(args map[string]any) (string, error) {
	// 批量模式：tasks 数组存在且非空。
	if raw, ok := args["tasks"]; ok {
		if taskList := toStringSlice(raw); len(taskList) > 0 {
			return w.runBatch(taskList), nil
		}
	}

	// 单任务模式。
	task := ""
	if v, ok := args["task"].(string); ok {
		task = v
	}
	if task == "" {
		task = w.cfg.Description
	}
	return w.runOne(task), nil
}

// runOne 运行单个任务。
func (w *workerTool) runOne(task string) string {
	sub, err := New(w.cfg)
	if err != nil {
		return fmt.Sprintf("创建子 Agent %q 失败: %v", w.cfg.Name, err)
	}
	fmt.Printf("\n╭── 进入子 Agent: %s ──\n", w.cfg.Name)
	result, err := sub.Run(context.Background(), task)
	fmt.Printf("╰── 子 Agent %s 完成 ──\n", w.cfg.Name)
	if err != nil {
		return fmt.Sprintf("子 Agent %q 执行出错: %v", w.cfg.Name, err)
	}
	return result
}

// runBatch 并发执行多个任务并按顺序汇总结果（对应 tool.batch(tasks)）。
func (w *workerTool) runBatch(taskList []string) string {
	logging.Get().Info("子 Agent %s 批量执行 %d 个任务（并发上限 %d）",
		w.cfg.Name, len(taskList), maxWorkerConcurrency)

	results := make([]string, len(taskList))
	sem := make(chan struct{}, maxWorkerConcurrency)
	var wg sync.WaitGroup

	for i, t := range taskList {
		wg.Add(1)
		go func(idx int, task string) {
			defer wg.Done()
			sem <- struct{}{}        // 获取并发名额
			defer func() { <-sem }() // 释放
			results[idx] = w.runOne(task)
		}(i, t)
	}
	wg.Wait()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("子 Agent %s 批量完成 %d 个任务:\n", w.cfg.Name, len(taskList)))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("\n=== 任务 #%d ===\n%s\n", i+1, r))
	}
	return sb.String()
}

// toStringSlice 把 any 转成 []string（容错处理 yaml/json 解出的 []any）。
func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
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
