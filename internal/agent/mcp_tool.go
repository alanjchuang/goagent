// mcp_tool.go 把 MCP server 暴露的工具适配成 agent 工具系统。
//
// 对应 AgentLoom src/mcp/tool_wrapper.py。每个 MCP 工具被包装成 mcpTool，
// 工具名加 "mcp__<server>__" 前缀以避免与内置工具冲突。
package agent

import (
	"github.com/alanjchuang/goagent/internal/config"
	"github.com/alanjchuang/goagent/internal/logging"
	"github.com/alanjchuang/goagent/internal/mcp"
	"github.com/alanjchuang/goagent/internal/tools"
)

// mcpTool 适配单个 MCP server 工具。
type mcpTool struct {
	client   *mcp.Client
	def      mcp.ToolDef
	toolName string // 加前缀后的对外工具名
}

func (m *mcpTool) Name() string        { return m.toolName }
func (m *mcpTool) Description() string { return m.def.Description }
func (m *mcpTool) Parameters() map[string]any {
	if m.def.InputSchema != nil {
		return m.def.InputSchema
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (m *mcpTool) Execute(args map[string]any) (string, error) {
	return m.client.CallTool(m.def.Name, args)
}

var _ tools.Tool = (*mcpTool)(nil)

// registerMCPTools 读取 .mcp.json，连接所有 server 并把其工具注册到 registry。
// 返回已连接的 client 列表，供调用方在结束时 Close。失败的 server 仅记录警告，不中断。
func registerMCPTools(reg *tools.Registry) []*mcp.Client {
	if config.C == nil || config.C.System.MCPServers == "" {
		return nil
	}
	cfgPath := mcp.ResolveConfigPath(config.C.System.MCPServers, config.C.AgentRoot)
	servers, err := mcp.LoadConfig(cfgPath)
	if err != nil {
		logging.Get().Warn("加载 MCP 配置失败: %v", err)
		return nil
	}
	var clients []*mcp.Client
	for name, spec := range servers {
		client, err := mcp.Connect(name, spec, config.C.AgentRoot)
		if err != nil {
			logging.Get().Warn("连接 MCP server %q 失败: %v", name, err)
			continue
		}
		for _, def := range client.Tools {
			toolName := "mcp__" + name + "__" + def.Name
			reg.Register(&mcpTool{client: client, def: def, toolName: toolName})
		}
		logging.Get().Info("MCP server %q 已连接，注册 %d 个工具", name, len(client.Tools))
		clients = append(clients, client)
	}
	return clients
}

// closeMCPClients 关闭所有 MCP client（agent 结束时调用）。
func closeMCPClients(clients []*mcp.Client) {
	for _, c := range clients {
		c.Close()
	}
}
