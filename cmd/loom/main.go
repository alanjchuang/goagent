// Command loom 是 goagent 的命令行入口。
//
// 用法:
//
//	loom run <workflow.yaml>            运行一个 agent
//	loom run <workflow.yaml> --task "自定义任务"
//
// 对应 Python 版的 `loom run`（src/__main__.py + src/runner.py）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/alanjchuang/goagent/internal/agent"
	"github.com/alanjchuang/goagent/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`goagent (loom) - 轻量多 Agent 框架 (Go 版)

用法:
  loom run <workflow.yaml> [--task "任务文本"]

示例:
  loom run applications/demo/workflows/demo_agent.yaml
  loom run applications/demo/workflows/demo_agent.yaml --task "列出 internal 目录"
`)
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	taskOverride := fs.String("task", "", "覆盖 YAML 中的 description 作为任务文本")

	// Go 的 flag 包遇到第一个位置参数就停止解析后续 flag。
	// 这里把 flag 与位置参数重排（flag 在前），以支持 `run <yaml> --task ...` 的写法。
	flags, positional := splitArgs(args)
	_ = fs.Parse(append(flags, positional...))

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "错误: 需要提供 workflow yaml 路径")
		os.Exit(1)
	}
	yamlPath := fs.Arg(0)

	// 1. 加载全局配置（发现 agent_root, 读取 system.yaml / llm.yaml）。
	if _, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 2. 加载 agent 配置。
	ac, err := config.LoadAgentConfig(yamlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// 3. 决定任务文本。
	task := ac.Description
	if *taskOverride != "" {
		task = *taskOverride
	}

	// 4. 构建并运行 agent。
	ag, err := agent.New(ac)
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建 agent 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("======================================================\n")
	fmt.Printf("应用: %s\n", ac.Name)
	fmt.Printf("模型类型: %s\n", ac.ModelType)
	fmt.Printf("任务: %s\n", task)
	fmt.Printf("======================================================\n")

	result, err := ag.Run(context.Background(), task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n执行失败: %v\n", err)
		os.Exit(1)
	}

	usage := ag.Usage()
	fmt.Printf("\n======================================================\n")
	fmt.Printf("执行完成。Token 用量: 输入=%d 输出=%d\n", usage.InputTokens, usage.OutputTokens)
	fmt.Printf("======================================================\n")
	fmt.Println(result)
}

// splitArgs 把参数分成 flag 参数（以 - 开头及其值）与位置参数两组。
// 用于绕开 Go flag 包"位置参数后停止解析 flag"的限制。
// 仅支持本程序用到的 `--flag value` 与 `--flag=value` 形式。
func splitArgs(args []string) (flags, positional []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			// 形如 `--task value`（无 =）时，把下一个参数当作其值。
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	return flags, positional
}
