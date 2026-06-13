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
	"time"

	"github.com/alanjchuang/goagent/internal/agent"
	"github.com/alanjchuang/goagent/internal/checkpoint"
	"github.com/alanjchuang/goagent/internal/config"
	"github.com/alanjchuang/goagent/internal/logging"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "list-tasks":
		listTasksCmd()
	case "clean-tasks":
		cleanTasksCmd(os.Args[2:])
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
  loom run <workflow.yaml> [--task "任务文本"] [--log-to-file] [--resume <taskID>]
  loom list-tasks                列出可恢复的 checkpoint 任务
  loom clean-tasks [--all]       清理 checkpoint（默认清理 7 天前的）

示例:
  loom run applications/demo/workflows/demo_agent.yaml
  loom run applications/demo/workflows/demo_agent.yaml --task "列出 internal 目录"
  loom run applications/demo/workflows/demo_agent.yaml --resume task_123
`)
}

// logRootDir 返回 checkpoint/日志根目录。
func logRootDir() string {
	dir := ".logs"
	if config.C != nil && config.C.System.Logging.Dir != "" {
		dir = config.C.System.Logging.Dir
	}
	return dir
}

// listTasksCmd 实现 loom list-tasks。
func listTasksCmd() {
	if _, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	states, err := checkpoint.ListAll(logRootDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "列出任务失败: %v\n", err)
		os.Exit(1)
	}
	if len(states) == 0 {
		fmt.Println("没有可恢复的任务。")
		return
	}
	for _, s := range states {
		fmt.Printf("  [%s] %s  (%s)  %s\n  task: %s\n",
			s.Status, s.TaskID, s.AgentName, s.UpdatedAt.Format("2006-01-02 15:04:05"),
			truncate(s.Task, 60))
	}
}

// cleanTasksCmd 实现 loom clean-tasks。
func cleanTasksCmd(args []string) {
	fs := flag.NewFlagSet("clean-tasks", flag.ExitOnError)
	all := fs.Bool("all", false, "清理所有 checkpoint（默认仅清理 7 天前的）")
	_ = fs.Parse(args)

	if _, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	logDir := logRootDir()
	entries, _ := os.ReadDir(logDir)
	maxAge := 7 * 24 * time.Hour
	if *all {
		maxAge = 0
	}
	total := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mgr := checkpoint.NewManager(e.Name(), logDir)
		n, _ := mgr.CleanOlderThan(maxAge)
		total += n
	}
	fmt.Printf("已清理 %d 个 checkpoint。\n", total)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	taskOverride := fs.String("task", "", "覆盖 YAML 中的 description 作为任务文本")
	logToFile := fs.Bool("log-to-file", false, "把日志归档到 .logs/<agent>/<时间戳>/run.log")
	resumeID := fs.String("resume", "", "从指定 checkpoint task ID 恢复执行")

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

	// 2.1 初始化结构化日志。
	logLevel := config.C.System.Logging.Level
	if logLevel == "" {
		logLevel = "INFO"
	}
	logDir := config.C.System.Logging.Dir
	if logDir == "" {
		logDir = ".logs"
	}
	log := logging.Init(logging.Options{
		AgentName: ac.Name,
		Level:     logLevel,
		Dir:       logDir,
		ToFile:    *logToFile,
	})
	defer log.Close()

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

	// 4.1 启用 checkpoint：新建或从 --resume 恢复。
	ckptMgr := checkpoint.NewManager(ac.Name, logDir)
	var state *checkpoint.State
	if *resumeID != "" {
		state, err = ckptMgr.Load(*resumeID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "无法恢复任务 %s: %v\n", *resumeID, err)
			os.Exit(1)
		}
		task = state.Task // 恢复时沿用原任务文本
		fmt.Printf("恢复任务: %s (状态=%s)\n", state.TaskID, state.Status)
	} else {
		state = &checkpoint.State{
			TaskID:    checkpoint.NewTaskID(),
			AgentName: ac.Name,
			YAMLPath:  yamlPath,
			Task:      task,
			Status:    checkpoint.StatusRunning,
			CreatedAt: time.Now(),
		}
	}
	ag.EnableCheckpoint(ckptMgr, state)

	fmt.Printf("======================================================\n")
	fmt.Printf("应用: %s\n", ac.Name)
	fmt.Printf("模型类型: %s\n", ac.ModelType)
	fmt.Printf("任务: %s\n", task)
	fmt.Printf("Task ID: %s\n", state.TaskID)
	if rd := log.RunDir(); rd != "" {
		fmt.Printf("日志归档: %s\n", rd)
	}
	fmt.Printf("======================================================\n")
	log.Info("开始执行任务: %s", task)

	result, err := ag.Run(context.Background(), task)
	if err != nil {
		log.Error("执行失败: %v", err)
		fmt.Fprintf(os.Stderr, "\n执行失败: %v\n", err)
		os.Exit(1)
	}

	usage := ag.Usage()
	log.Info("执行完成。Token 用量: 输入=%d 输出=%d", usage.InputTokens, usage.OutputTokens)
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
