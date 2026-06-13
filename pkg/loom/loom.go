// Package loom 是 goagent 的公开 API 包，供外部 Go 程序嵌入运行 agent。
//
// 与 internal/* 不同，本包可被仓库外的代码导入。loom create 生成的入口代码
// 即引用本包。
package loom

import "github.com/alanjchuang/goagent/internal/agent"

// RunApp 从 YAML 配置启动并运行一个 agent，返回最终答复。
// taskOverride 非空时覆盖 YAML 的 description 作为任务文本。
//
// 示例:
//
//	result, err := loom.RunApp("applications/demo/workflows/demo_agent.yaml", "")
func RunApp(yamlPath, taskOverride string) (string, error) {
	return agent.RunApp(yamlPath, taskOverride)
}
