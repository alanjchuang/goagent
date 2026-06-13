# goagent

goagent is a Go implementation of the AgentLoom-style multi-agent framework. It provides a lightweight CLI and embeddable Go API for running YAML-defined agents with OpenAI-compatible LLM backends, built-in tools, worker agents, CodeAct execution, checkpoint resume, MCP/LSP integrations, a Web UI, sandbox execution, and optional Langfuse tracing.

goagent 是一个 AgentLoom 风格多 Agent 框架的 Go 实现。它提供轻量 CLI 与可嵌入 Go API，用 YAML 定义 Agent，并支持 OpenAI 兼容 LLM 后端、内置工具、Worker 子 Agent、CodeAct 执行、checkpoint 断点恢复、MCP/LSP 集成、Web UI、沙箱执行以及可选 Langfuse 追踪。

---

## 中文文档

### 1. 功能概览

- **YAML 驱动 Agent**：通过 `applications/<app>/workflows/*.yaml` 定义 Agent 名称、任务描述、模型类型、工作流和工具。
- **OpenAI 兼容 LLM**：支持火山方舟 ARK、OpenAI 或其他兼容 Chat Completions API 的服务。
- **Tool Calling Agent**：支持模型原生 tool calls，也支持从文本中解析 JSON/XML/正则形式的工具调用。
- **CodeAct Agent**：让模型编写并执行 `python`、`golang` 或 `bash` 代码块来完成任务。
- **Worker-as-Tool**：Supervisor Agent 可以把 Worker Agent 当成工具调用，并支持批量并发调度。
- **内置工具**：文件读写、目录浏览、grep/glob 搜索、shell、编辑文件、网页搜索、repo map、LSP 代码跳转等。
- **Skills**：支持 `SKILL.md` + YAML frontmatter，包含强制注入、按需加载、隐藏三种模式。
- **MCP**：读取 `.mcp.json`，通过 stdio JSON-RPC 连接 MCP server，并把 MCP tools 暴露给 Agent。
- **LSP**：通过语言服务器提供定义跳转、引用查找、文档符号等代码智能能力。
- **Checkpoint**：任务运行状态持久化，可列出和恢复中断任务。
- **Web UI**：基于 HTTP + SSE 实时展示 Agent 运行事件。
- **Docker 沙箱**：可把 shell 工具放入 Docker 容器中执行。
- **Langfuse 追踪**：通过环境变量启用 HTTP ingestion 上报。

### 2. 环境要求

- Go 1.21+
- 可访问的 OpenAI 兼容 Chat Completions API
- 可选：Docker（使用沙箱执行时需要）
- 可选：语言服务器（使用 LSP 工具时需要，例如 `gopls`）
- 可选：Node/Python/其他运行时（MCP server 或 CodeAct 代码执行可能需要）

### 3. 安装与构建

在仓库根目录执行：

```bash
go mod tidy
go build ./cmd/loom
```

构建后会在当前目录生成 `loom` 可执行文件。你也可以直接用 `go run`：

```bash
go run ./cmd/loom --help
```

如果希望全局使用：

```bash
go install ./cmd/loom
```

### 4. 配置 LLM

复制示例配置：

```bash
cp config/llm.example.yaml config/llm.yaml
```

编辑 `config/llm.yaml`：

```yaml
model:
  default_model_type: powerful

  powerful:
    base_url: "https://ark.cn-beijing.volces.com/api/v3"
    api_key: "你的 ARK API Key"
    model: "你的方舟 endpoint 或模型名"
    temperature: 0.2
    max_tokens: 8000
    timeout: 300
    num_retries: 3
    supports_native_tool_calls: "auto"

  fast:
    base_url: "https://ark.cn-beijing.volces.com/api/v3"
    api_key: "你的 ARK API Key"
    model: "你的方舟 endpoint 或模型名"
    temperature: 0.3
    max_tokens: 4000
    timeout: 120
    num_retries: 3
    supports_native_tool_calls: "auto"
```

说明：

- `base_url` 是 OpenAI 兼容接口地址，结尾不要包含 `/chat/completions`。
- `api_key` 建议只放在本地 `config/llm.yaml`，不要提交到 Git。
- `model_type` 会在 Agent YAML 中引用，例如 `model_type: "powerful"`。
- `supports_native_tool_calls` 可选值：
  - `auto`：自动探测模型是否支持原生 tool calls。
  - `true`：强制使用原生 tool calls。
  - `false`：不使用原生 tool calls，改用文本解析工具调用。

也可以用 OpenAI 或其他兼容服务：

```yaml
powerful:
  base_url: "https://api.openai.com/v1"
  api_key: "sk-..."
  model: "openai/gpt-4o"
```

### 5. CLI 使用

#### 查看帮助

```bash
go run ./cmd/loom --help
# 或构建后
./loom --help
```

#### 运行 Agent

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml
```

覆盖 YAML 中的默认任务描述：

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --task "列出 internal 目录，并总结核心模块"
```

记录运行日志到 `.logs/<agent>/<timestamp>/run.log`：

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --log-to-file
```

运行时同时启动 Web UI：

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --ui 8080
```

然后打开：

```text
http://localhost:8080
```

Web UI 是面向新手的操作界面，包含四个页签：

- `和模型对话`：直接和 `config/llm.yaml` 中配置的模型聊天，不需要写命令。
- `执行 Applications`：自动列出 `applications/**/workflows/*.yaml`，选择应用、填写任务并运行。
- `实时事件`：通过 SSE 实时展示 Agent step、tool_call、tool_result、final_answer 等事件。
- `本地日志`：查看 `.logs/<agent>/<timestamp>/run.log`。该页签需要运行 Agent 时加 `--log-to-file` 才会有归档日志。

#### 运行 Supervisor + Worker

```bash
go run ./cmd/loom run applications/demo/workflows/supervisor_agent.yaml --task "分析这个项目的目录结构和关键模块"
```

`supervisor_agent.yaml` 会把 `worker_agents` 中声明的 Worker 包装为工具，Supervisor 可以直接调用子 Agent 完成局部任务。

#### 运行 CodeAct Agent

```bash
go run ./cmd/loom run applications/demo/workflows/codeact_agent.yaml --task "用 Go 或 Python 计算 1 到 100 的平方和"
```

CodeAct 模式会要求模型输出代码块并执行，支持：

```text
```python
print("hello")
```

```golang
package main
func main() {}
```

```bash
ls -la
```
```

#### 生成独立 Go 入口

```bash
go run ./cmd/loom create applications/demo/workflows/demo_agent.yaml
```

默认生成到：

```text
applications/demo/workflows/demo_agent_app/main.go
```

指定输出路径：

```bash
go run ./cmd/loom create applications/demo/workflows/demo_agent.yaml -o ./demo_app/main.go
```

运行生成的入口：

```bash
go run ./demo_app/main.go "检查 README 和 config 目录"
```

#### 列出和恢复 checkpoint 任务

列出可恢复任务：

```bash
go run ./cmd/loom list-tasks
```

恢复指定任务：

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --resume task_xxx
```

清理 7 天前的 checkpoint：

```bash
go run ./cmd/loom clean-tasks
```

清理所有 checkpoint：

```bash
go run ./cmd/loom clean-tasks --all
```

#### 单独启动 Web UI

```bash
go run ./cmd/loom ui --port 8080
```

打开 `http://localhost:8080` 后，新手可以直接：

1. 在 `和模型对话` 页签输入问题并发送。
2. 在 `执行 Applications` 页签选择一个应用，填写任务并点击运行。
3. 切到 `实时事件` 查看 Agent 执行过程。
4. 切到 `本地日志` 查看已经归档到 `.logs` 的历史日志。

注意：单独启动 UI 时，实时事件只会显示当前 UI 进程内的历史/新增事件；如果想边运行边看事件，可以用 `loom run ... --ui 8080`。

### 6. 编写 Agent YAML

最小示例：

```yaml
name: "my_agent"
description: |
  请检查当前项目并总结核心文件。

model_type: "powerful"
tool_call_type: "tool_call"

workflow: |
  1. 使用 browse_directory 查看目录。
  2. 使用 read_file 阅读关键文件。
  3. 用 final_answer 给出总结。

tools:
  - name: "browse_directory"
  - name: "read_file"
  - name: "grep_search"

worker_agents: []
```

常用字段：

| 字段 | 说明 |
| --- | --- |
| `name` | Agent 名称，必填 |
| `description` | 默认任务描述，必填；`--task` 可覆盖 |
| `model_type` | 对应 `config/llm.yaml` 中的模型类型 |
| `tool_call_type` | `tool_call` 或 `code_act` |
| `workflow` | 注入给 Agent 的执行流程说明，必填 |
| `tools` | 启用的工具列表 |
| `worker_agents` | 子 Agent YAML 路径列表 |
| `skills` | Skill 目录列表 |
| `execution_env` | 执行环境配置，支持 `local` 或 `docker` |

### 7. 内置工具

在 Agent YAML 的 `tools` 中声明即可启用：

| 工具名 | 用途 |
| --- | --- |
| `read_file` | 读取文件内容 |
| `write_file` | 写入文件 |
| `browse_directory` | 浏览目录 |
| `get_file_outline` | 获取文件概要/预览 |
| `shell_tool` | 执行 shell 命令，受安全策略限制 |
| `grep_search` | 正则搜索文件内容 |
| `glob_search` | 按 glob 查找文件 |
| `edit_file` | 对文件做局部编辑 |
| `web_search` | 调用兼容 LLM 侧能力的网页搜索工具 |
| `repo_map` | 生成仓库符号地图 |
| `lsp_find_definition` | LSP 定义跳转 |
| `lsp_find_references` | LSP 引用查找 |
| `lsp_get_document_symbols` | LSP 文档符号 |

### 8. Worker-as-Tool

Supervisor YAML：

```yaml
worker_agents:
  - path: "applications/demo/workflows/worker_agents/file_reader_worker.yaml"
```

框架会读取 Worker YAML，并把 Worker Agent 包装成 Supervisor 可调用的工具。适合拆分复杂任务，例如：

- Supervisor 负责规划、分派和最终汇总。
- Worker 负责读取单个文件、分析单个模块或执行局部检查。

### 9. Skills

Agent YAML 中声明：

```yaml
skills:
  - path: "applications/demo/skills"
    platform: "Claude"
```

Skill 目录结构：

```text
applications/demo/skills/
  my_skill/
    SKILL.md
```

`SKILL.md` 示例：

```markdown
---
name: my_skill
description: 项目分析指南
version: 0.1.0
invocation-control:
  allow-model: force-inject
---

当你分析项目时，请先阅读 README，再检查配置文件和入口文件。
```

`allow-model` 支持：

- `force-inject`：启动时强制注入系统提示词。
- `true`：Agent 可通过 `list_skills` / `load_skill` 按需加载。
- `false`：对模型隐藏。

### 10. MCP 配置

在项目根目录创建 `.mcp.json`：

```json
{
  "mcpServers": {
    "demo": {
      "command": "python3",
      "args": ["./scripts/fake_mcp_server.py"],
      "env": {}
    }
  }
}
```

`config/system.yaml` 可通过 `mcp_servers` 指定配置文件路径；默认逻辑兼容 Claude Code 风格 `.mcp.json`。MCP server 通过 stdio JSON-RPC 暴露的工具会被注册进 Agent 工具集合。

### 11. LSP 工具

启用 LSP 工具：

```yaml
tools:
  - name: "lsp_find_definition"
  - name: "lsp_find_references"
  - name: "lsp_get_document_symbols"
```

使用前请确保对应语言服务器可用。例如 Go 项目：

```bash
go install golang.org/x/tools/gopls@latest
```

### 12. Docker 沙箱

Agent YAML 可声明执行环境：

```yaml
execution_env:
  type: "docker"
  image: "golang:1.21"
  workdir: "/workspace"
```

当 `shell_tool` 执行命令时，会通过 Docker CLI 在容器内运行。适合隔离高风险命令或为任务提供固定运行环境。

### 13. Langfuse 追踪

设置环境变量后自动启用：

```bash
export LANGFUSE_PUBLIC_KEY="pk-lf-..."
export LANGFUSE_SECRET_KEY="sk-lf-..."
export LANGFUSE_HOST="https://cloud.langfuse.com" # 可选
```

运行 Agent 时，框架会订阅事件总线，并将 trace/event 批量上报到 Langfuse ingestion API。

### 14. 嵌入 Go 程序

外部 Go 代码可以直接导入公开包：

```go
package main

import (
    "fmt"
    "log"

    "github.com/alanjchuang/goagent/pkg/loom"
)

func main() {
    result, err := loom.RunApp("applications/demo/workflows/demo_agent.yaml", "总结 internal 目录")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result)
}
```

### 15. 目录结构

```text
cmd/loom/                  CLI 入口
config/                    system.yaml / llm.yaml
applications/demo/          示例 workflows 和 skills
internal/agent/             Agent 主循环、Worker、CodeAct、RunApp
internal/config/            配置加载
internal/llm/               OpenAI 兼容 LLM 客户端
internal/tools/             内置工具与安全策略
internal/mcp/               MCP stdio JSON-RPC 客户端
internal/lsp/               LSP JSON-RPC 客户端
internal/skills/            SKILL.md 加载和注入
internal/checkpoint/        checkpoint 持久化
internal/ui/                Web UI + SSE
internal/tracing/           Langfuse HTTP 上报
pkg/loom/                   对外公开 API
```

### 16. 常见问题

#### `--task` 放在 YAML 路径后面能生效吗？

可以。CLI 已兼容以下写法：

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --task "你的任务"
```

#### ARK 返回 `ToolNotOpen` 怎么办？

这通常表示当前方舟账号或 endpoint 未开通对应插件能力，例如 web search。普通对话、文件工具、CodeAct 等仍可使用；如需 `web_search`，请在 ARK 控制台开通对应能力或从 Agent YAML 中移除该工具。

#### 为什么不要提交 `config/llm.yaml`？

该文件通常包含真实 API Key。请只提交 `config/llm.example.yaml`，本地自行维护 `config/llm.yaml`。

---

## English Documentation

### 1. Features

- **YAML-driven agents**: Define agent name, task description, model type, workflow, tools, workers, skills, and execution environment in YAML.
- **OpenAI-compatible LLM backend**: Works with Volcengine ARK, OpenAI, and other Chat Completions compatible providers.
- **Tool-calling agents**: Supports native tool calls and fallback text parsing with JSON/XML/regex strategies.
- **CodeAct agents**: The model can write and execute `python`, `golang`, or `bash` code blocks.
- **Worker-as-Tool**: Supervisor agents can call worker agents as tools, including batched concurrent execution.
- **Built-in tools**: File I/O, directory browsing, grep/glob search, shell execution, file editing, web search, repo map, and LSP tools.
- **Skills**: Load `SKILL.md` files with YAML frontmatter and support force-inject, on-demand, and hidden modes.
- **MCP integration**: Reads `.mcp.json`, connects to MCP servers over stdio JSON-RPC, and exposes MCP tools to agents.
- **LSP integration**: Provides definition lookup, reference lookup, and document symbols via language servers.
- **Checkpointing**: Persist task state and resume interrupted runs.
- **Web UI**: HTTP + SSE dashboard for real-time agent events.
- **Docker sandbox**: Run shell commands inside Docker containers.
- **Langfuse tracing**: Optional HTTP ingestion tracing via environment variables.

### 2. Requirements

- Go 1.21+
- An OpenAI-compatible Chat Completions API endpoint
- Optional: Docker for sandbox execution
- Optional: Language servers for LSP tools, such as `gopls`
- Optional: Node/Python/other runtimes for MCP servers or CodeAct execution

### 3. Build and Install

From the repository root:

```bash
go mod tidy
go build ./cmd/loom
```

This creates a `loom` binary in the current directory. You can also run it directly:

```bash
go run ./cmd/loom --help
```

Install it globally:

```bash
go install ./cmd/loom
```

### 4. Configure the LLM

Copy the example configuration:

```bash
cp config/llm.example.yaml config/llm.yaml
```

Edit `config/llm.yaml`:

```yaml
model:
  default_model_type: powerful

  powerful:
    base_url: "https://ark.cn-beijing.volces.com/api/v3"
    api_key: "your ARK API key"
    model: "your ARK endpoint or model name"
    temperature: 0.2
    max_tokens: 8000
    timeout: 300
    num_retries: 3
    supports_native_tool_calls: "auto"

  fast:
    base_url: "https://ark.cn-beijing.volces.com/api/v3"
    api_key: "your ARK API key"
    model: "your ARK endpoint or model name"
    temperature: 0.3
    max_tokens: 4000
    timeout: 120
    num_retries: 3
    supports_native_tool_calls: "auto"
```

Notes:

- `base_url` should not include `/chat/completions`.
- Keep real API keys only in local `config/llm.yaml`; do not commit it.
- Agent YAML files reference these model profiles via `model_type`.
- `supports_native_tool_calls` can be `auto`, `true`, or `false`.

OpenAI-compatible example:

```yaml
powerful:
  base_url: "https://api.openai.com/v1"
  api_key: "sk-..."
  model: "openai/gpt-4o"
```

### 5. CLI Usage

#### Help

```bash
go run ./cmd/loom --help
# or after building
./loom --help
```

#### Run an agent

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml
```

Override the default task from YAML:

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --task "List the internal directory and summarize the core modules"
```

Write logs to `.logs/<agent>/<timestamp>/run.log`:

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --log-to-file
```

Start the Web UI together with the run:

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --ui 8080
```

Open:

```text
http://localhost:8080
```

The Web UI is beginner-friendly and has four tabs:

- `和模型对话` / chat with model: chat directly with the model configured in `config/llm.yaml`.
- `执行 Applications` / run applications: list `applications/**/workflows/*.yaml`, choose one, enter a task, and run it.
- `实时事件` / live events: real-time agent events such as step, tool_call, tool_result, and final_answer over SSE.
- `本地日志` / local logs: archived `.logs/<agent>/<timestamp>/run.log` files. Run the agent with `--log-to-file` to generate these files.

#### Run Supervisor + Worker

```bash
go run ./cmd/loom run applications/demo/workflows/supervisor_agent.yaml --task "Analyze this project's structure and key modules"
```

The supervisor loads worker agents from `worker_agents` and exposes them as callable tools.

#### Run a CodeAct agent

```bash
go run ./cmd/loom run applications/demo/workflows/codeact_agent.yaml --task "Use Go or Python to calculate the sum of squares from 1 to 100"
```

CodeAct supports `python`, `golang`, and `bash` fenced code blocks.

#### Generate a standalone Go entrypoint

```bash
go run ./cmd/loom create applications/demo/workflows/demo_agent.yaml
```

Default output:

```text
applications/demo/workflows/demo_agent_app/main.go
```

Specify an output path:

```bash
go run ./cmd/loom create applications/demo/workflows/demo_agent.yaml -o ./demo_app/main.go
```

Run the generated app:

```bash
go run ./demo_app/main.go "Inspect README and config"
```

#### Checkpoints

List resumable tasks:

```bash
go run ./cmd/loom list-tasks
```

Resume a task:

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --resume task_xxx
```

Clean old checkpoints:

```bash
go run ./cmd/loom clean-tasks
```

Clean all checkpoints:

```bash
go run ./cmd/loom clean-tasks --all
```

#### Start only the Web UI

```bash
go run ./cmd/loom ui --port 8080
```

Open `http://localhost:8080`, then a beginner can:

1. Ask questions in the `和模型对话` tab.
2. Choose and run an application in the `执行 Applications` tab.
3. Inspect agent execution in the `实时事件` tab.
4. Read archived logs from `.logs` in the `本地日志` tab.

When started standalone, live events are limited to events in the current UI process. To watch events while running an agent, use `loom run ... --ui 8080`.

### 6. Agent YAML Guide

Minimal example:

```yaml
name: "my_agent"
description: |
  Inspect the current project and summarize core files.

model_type: "powerful"
tool_call_type: "tool_call"

workflow: |
  1. Use browse_directory to inspect the directory.
  2. Use read_file to read key files.
  3. Return the summary with final_answer.

tools:
  - name: "browse_directory"
  - name: "read_file"
  - name: "grep_search"

worker_agents: []
```

Common fields:

| Field | Description |
| --- | --- |
| `name` | Required agent name |
| `description` | Required default task; can be overridden by `--task` |
| `model_type` | Model profile in `config/llm.yaml` |
| `tool_call_type` | `tool_call` or `code_act` |
| `workflow` | Required workflow instructions |
| `tools` | Enabled tool list |
| `worker_agents` | Worker agent YAML paths |
| `skills` | Skill directory paths |
| `execution_env` | Execution environment: `local` or `docker` |

### 7. Built-in Tools

Enable tools in YAML with `tools`:

| Tool | Purpose |
| --- | --- |
| `read_file` | Read files |
| `write_file` | Write files |
| `browse_directory` | Browse directories |
| `get_file_outline` | Show file outline/preview |
| `shell_tool` | Run shell commands with safety checks |
| `grep_search` | Search file contents with regex |
| `glob_search` | Find files with glob patterns |
| `edit_file` | Patch files locally |
| `web_search` | Use provider-side web search capability |
| `repo_map` | Generate a repository symbol map |
| `lsp_find_definition` | Find definitions via LSP |
| `lsp_find_references` | Find references via LSP |
| `lsp_get_document_symbols` | Get document symbols via LSP |

### 8. Worker-as-Tool

Supervisor YAML:

```yaml
worker_agents:
  - path: "applications/demo/workflows/worker_agents/file_reader_worker.yaml"
```

The framework loads each worker YAML and wraps the worker agent as a callable tool. This is useful for complex task decomposition:

- The supervisor plans, dispatches, and aggregates results.
- Workers inspect individual files, modules, or subtasks.

### 9. Skills

Declare skill directories in agent YAML:

```yaml
skills:
  - path: "applications/demo/skills"
    platform: "Claude"
```

Directory layout:

```text
applications/demo/skills/
  my_skill/
    SKILL.md
```

`SKILL.md` example:

```markdown
---
name: my_skill
description: Project analysis guide
version: 0.1.0
invocation-control:
  allow-model: force-inject
---

When analyzing a project, read README first, then inspect config and entrypoint files.
```

`allow-model` values:

- `force-inject`: inject content into the system prompt at startup.
- `true`: visible to the agent and loadable on demand.
- `false`: hidden from the model.

### 10. MCP

Create `.mcp.json` in the repository root:

```json
{
  "mcpServers": {
    "demo": {
      "command": "python3",
      "args": ["./scripts/fake_mcp_server.py"],
      "env": {}
    }
  }
}
```

The framework reads MCP server definitions, connects over stdio JSON-RPC, lists tools, and registers them as agent tools.

### 11. LSP Tools

Enable LSP tools:

```yaml
tools:
  - name: "lsp_find_definition"
  - name: "lsp_find_references"
  - name: "lsp_get_document_symbols"
```

Install a language server first. For Go:

```bash
go install golang.org/x/tools/gopls@latest
```

### 12. Docker Sandbox

Agent YAML can declare a Docker execution environment:

```yaml
execution_env:
  type: "docker"
  image: "golang:1.21"
  workdir: "/workspace"
```

When `shell_tool` runs commands, the framework executes them through Docker CLI inside the configured container image.

### 13. Langfuse Tracing

Set environment variables to enable tracing:

```bash
export LANGFUSE_PUBLIC_KEY="pk-lf-..."
export LANGFUSE_SECRET_KEY="sk-lf-..."
export LANGFUSE_HOST="https://cloud.langfuse.com" # optional
```

During agent runs, goagent subscribes to the internal event bus and sends trace/event batches to the Langfuse ingestion API.

### 14. Embed in Go

Use the public package:

```go
package main

import (
    "fmt"
    "log"

    "github.com/alanjchuang/goagent/pkg/loom"
)

func main() {
    result, err := loom.RunApp("applications/demo/workflows/demo_agent.yaml", "Summarize the internal directory")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result)
}
```

### 15. Project Layout

```text
cmd/loom/                  CLI entrypoint
config/                    system.yaml / llm.yaml
applications/demo/          example workflows and skills
internal/agent/             agent loop, workers, CodeAct, RunApp
internal/config/            configuration loading
internal/llm/               OpenAI-compatible LLM client
internal/tools/             built-in tools and security policy
internal/mcp/               MCP stdio JSON-RPC client
internal/lsp/               LSP JSON-RPC client
internal/skills/            SKILL.md loading and injection
internal/checkpoint/        checkpoint persistence
internal/ui/                Web UI + SSE
internal/tracing/           Langfuse HTTP ingestion
pkg/loom/                   public API
```

### 16. FAQ

#### Can `--task` be placed after the YAML path?

Yes:

```bash
go run ./cmd/loom run applications/demo/workflows/demo_agent.yaml --task "your task"
```

#### What does ARK `ToolNotOpen` mean?

It usually means the ARK account or endpoint has not enabled the requested provider-side tool capability, such as web search. Regular chat, file tools, and CodeAct still work. Enable the capability in ARK or remove `web_search` from the agent YAML.

#### Why should `config/llm.yaml` not be committed?

It usually contains real API keys. Commit `config/llm.example.yaml` only and keep `config/llm.yaml` local.
