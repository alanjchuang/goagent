// Package mcp 实现 MCP (Model Context Protocol) 客户端。
//
// 对应 AgentLoom src/mcp。读取 .mcp.json（Claude Code 兼容格式），通过 stdio
// 以 JSON-RPC 2.0 连接 MCP server，调用 initialize / tools/list / tools/call，
// 把 server 暴露的工具适配成 agent 可用的工具。
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ServerSpec 是 .mcp.json 中单个 server 的定义。
type ServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// configFile 是 .mcp.json 的顶层结构。
type configFile struct {
	MCPServers map[string]ServerSpec `json:"mcpServers"`
}

// LoadConfig 读取 .mcp.json，返回 server 名 → spec 映射。文件不存在返回空。
func LoadConfig(path string) (map[string]ServerSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cf configFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return cf.MCPServers, nil
}

// ---- JSON-RPC 2.0 报文 ----

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ToolDef 是 MCP server 暴露的一个工具定义。
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Client 是一个连接到单个 MCP server 的客户端。
type Client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	nextID int
	Tools  []ToolDef
}

// Connect 启动 server 进程并完成 initialize 握手 + tools/list。
func Connect(name string, spec ServerSpec, workDir string) (*Client, error) {
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 MCP server %q 失败: %w", name, err)
	}

	c := &Client{
		name:   name,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
	}

	// initialize 握手。
	if _, err := c.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "goagent", "version": "0.1.0"},
	}); err != nil {
		c.Close()
		return nil, fmt.Errorf("MCP server %q initialize 失败: %w", name, err)
	}
	// 发送 initialized 通知（无需响应）。
	c.notify("notifications/initialized", map[string]any{})

	// 拉取工具列表。
	res, err := c.call("tools/list", map[string]any{})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("MCP server %q tools/list 失败: %w", name, err)
	}
	var listResult struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(res, &listResult); err != nil {
		c.Close()
		return nil, fmt.Errorf("解析 MCP %q 工具列表失败: %w", name, err)
	}
	c.Tools = listResult.Tools
	return c, nil
}

// CallTool 调用 server 上的一个工具，返回文本结果。
func (c *Client) CallTool(toolName string, args map[string]any) (string, error) {
	res, err := c.call("tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}
	// MCP tools/call 结果结构: {"content": [{"type":"text","text":...}], "isError": bool}
	var callResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &callResult); err != nil {
		return string(res), nil
	}
	out := ""
	for _, c := range callResult.Content {
		out += c.Text + "\n"
	}
	return out, nil
}

// call 发送一个 JSON-RPC 请求并等待响应（带超时）。
func (c *Client) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	req := rpcRequest{JSONRPC: "2.0", ID: c.nextID, Method: method, Params: params}
	if err := c.writeMessage(req); err != nil {
		return nil, err
	}

	type result struct {
		resp *rpcResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := c.readMessage()
		ch <- result{resp, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if r.resp.Error != nil {
			return nil, fmt.Errorf("MCP 错误 %d: %s", r.resp.Error.Code, r.resp.Error.Message)
		}
		return r.resp.Result, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("MCP 调用 %s 超时", method)
	}
}

// notify 发送一个不需要响应的通知。
func (c *Client) notify(method string, params any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// writeMessage 以「换行分隔的 JSON」写一条报文。
func (c *Client) writeMessage(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

// readMessage 读取一条 JSON-RPC 响应（按行）。
func (c *Client) readMessage() (*rpcResponse, error) {
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			// 可能是 server 的非 JSON 输出行，跳过。
			continue
		}
		// 跳过没有 id 的通知。
		if resp.ID == 0 && resp.Result == nil && resp.Error == nil {
			continue
		}
		return &resp, nil
	}
}

// Close 关闭 stdin 并终止 server 进程。
func (c *Client) Close() {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// ResolveConfigPath 把 .mcp.json 路径相对 root 解析为绝对路径。
func ResolveConfigPath(path, root string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}
