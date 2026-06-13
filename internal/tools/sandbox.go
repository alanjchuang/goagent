// sandbox.go 实现 Docker 沙箱执行。
//
// 对应 AgentLoom utils/sandbox。当配置 execution_env=docker 时，shell 命令与
// code_act 代码在 docker 容器内执行（通过 docker CLI，不依赖 docker SDK）。
// 工作目录会挂载进容器，便于代码访问项目文件。
package tools

import (
	"os/exec"
)

// SandboxConfig 描述沙箱执行配置。
type SandboxConfig struct {
	Enabled bool   // 是否启用 docker 沙箱
	Image   string // 容器镜像，如 "python:3.12-slim"
	WorkDir string // 宿主机工作目录，挂载到容器 /workspace
}

// activeSandbox 是当前生效的沙箱配置；默认禁用（本地执行）。
var activeSandbox SandboxConfig

// SetSandbox 设置全局沙箱配置。
func SetSandbox(cfg SandboxConfig) { activeSandbox = cfg }

// SandboxEnabled 返回是否启用了 docker 沙箱。
func SandboxEnabled() bool { return activeSandbox.Enabled && activeSandbox.Image != "" }

// dockerWrap 把一条 shell 命令包装成在 docker 容器内执行的命令。
// 挂载 WorkDir 到 /workspace 并以其为工作目录。
func dockerWrap(command string) *exec.Cmd {
	workdir := activeSandbox.WorkDir
	if workdir == "" {
		workdir = "."
	}
	args := []string{
		"run", "--rm", "-i",
		"-v", workdir + ":/workspace",
		"-w", "/workspace",
		activeSandbox.Image,
		"sh", "-c", command,
	}
	return exec.Command("docker", args...)
}

// newShellCmd 根据是否启用沙箱，返回执行 command 的 *exec.Cmd。
// 这是 shell_tool 与 code_act 的统一入口。
func newShellCmd(command string) *exec.Cmd {
	if SandboxEnabled() {
		return dockerWrap(command)
	}
	return exec.Command("sh", "-c", command) //ignore_security_alert RCE
}

// NewShellCmd 是 newShellCmd 的导出版本，供 agent 包(code_act)复用沙箱路由。
func NewShellCmd(command string) *exec.Cmd { return newShellCmd(command) }
