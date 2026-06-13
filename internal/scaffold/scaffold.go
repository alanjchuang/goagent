// Package scaffold 实现 loom create：为一个 agent YAML 生成 Go 入口示例代码。
//
// 对应 AgentLoom src/scaffold。生成的文件演示如何用 agent.RunApp 把一个
// agent 嵌入到独立的 Go 程序中运行。
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// scriptTemplate 是生成的入口程序模板。%s 依次为：yaml 路径、yaml 路径。
const scriptTemplate = `// 由 loom create 生成的 agent 入口示例。
// 运行: go run %s
package main

import (
	"fmt"
	"os"

	"github.com/alanjchuang/goagent/pkg/loom"
)

func main() {
	// 可选：用命令行第一个参数覆盖任务文本。
	task := ""
	if len(os.Args) > 1 {
		task = os.Args[1]
	}

	result, err := loom.RunApp(%q, task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "执行失败: %%v\n", err)
		os.Exit(1)
	}
	fmt.Println(result)
}
`

// Create 为 yamlPath 生成一个 Go 入口文件。
// outputPath 为空时默认生成到 applications/<category>/<name>_app/main.go。
// 返回生成文件的路径。
func Create(yamlPath, outputPath string) (string, error) {
	if _, err := os.Stat(yamlPath); err != nil {
		// 尝试相对路径也无妨，交给后续运行时解析；这里仅在绝对不存在时提示。
		if !filepath.IsAbs(yamlPath) {
			// 允许相对路径，不在此处报错。
		} else {
			return "", fmt.Errorf("YAML 文件不存在: %s", yamlPath)
		}
	}

	if outputPath == "" {
		// 默认输出目录：基于 yaml 文件名。
		base := strings.TrimSuffix(filepath.Base(yamlPath), filepath.Ext(yamlPath))
		dir := filepath.Join(filepath.Dir(yamlPath), base+"_app")
		outputPath = filepath.Join(dir, "main.go")
	}

	if _, err := os.Stat(outputPath); err == nil {
		return "", fmt.Errorf("目标文件已存在，避免覆盖: %s", outputPath)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return "", err
	}
	content := fmt.Sprintf(scriptTemplate, outputPath, yamlPath)
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return outputPath, nil
}
