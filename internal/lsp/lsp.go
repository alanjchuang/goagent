// Package lsp 实现一个最小的 LSP (Language Server Protocol) 客户端。
//
// 对应 AgentLoom src/services/lsp。通过 stdio 以 LSP 帧格式(Content-Length 头 +
// JSON-RPC body)连接语言服务器(如 gopls / pyright)，支持 initialize、
// textDocument/definition、references、documentSymbol。
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client 是一个连接到单个 LSP server 的客户端。
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	nextID int
	root   string
}

// serverForExt 根据文件后缀返回启动 LSP server 的命令。
func serverForExt(ext string) []string {
	switch ext {
	case ".go":
		return []string{"gopls"}
	case ".py":
		return []string{"pyright-langserver", "--stdio"}
	case ".ts", ".tsx", ".js", ".jsx":
		return []string{"typescript-language-server", "--stdio"}
	default:
		return nil
	}
}

// Connect 启动适配该文件类型的 LSP server 并完成 initialize。
// root 为项目根目录。
func Connect(ext, root string) (*Client, error) {
	argv := serverForExt(ext)
	if argv == nil {
		return nil, fmt.Errorf("没有适配 %s 的 LSP server", ext)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 LSP server %q 失败(请确认已安装): %w", argv[0], err)
	}
	c := &Client{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), root: root}

	rootURI := "file://" + root
	if _, err := c.call("initialize", map[string]any{
		"processId":    nil,
		"rootUri":      rootURI,
		"capabilities": map[string]any{},
	}); err != nil {
		c.Close()
		return nil, fmt.Errorf("LSP initialize 失败: %w", err)
	}
	c.notify("initialized", map[string]any{})
	return c, nil
}

// Definition 查询符号定义位置。line/character 从 0 开始。
func (c *Client) Definition(file string, line, character int) (string, error) {
	return c.locationQuery("textDocument/definition", file, line, character)
}

// References 查询符号引用位置。
func (c *Client) References(file string, line, character int) (string, error) {
	c.didOpen(file)
	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI(file)},
		"position":     map[string]any{"line": line, "character": character},
		"context":      map[string]any{"includeDeclaration": true},
	}
	res, err := c.call("textDocument/references", params)
	if err != nil {
		return "", err
	}
	return formatLocations(res), nil
}

// DocumentSymbols 列出文件内的符号。
func (c *Client) DocumentSymbols(file string) (string, error) {
	c.didOpen(file)
	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI(file)},
	}
	res, err := c.call("textDocument/documentSymbol", params)
	if err != nil {
		return "", err
	}
	var syms []struct {
		Name string `json:"name"`
		Kind int    `json:"kind"`
	}
	if err := json.Unmarshal(res, &syms); err != nil {
		return string(res), nil
	}
	if len(syms) == 0 {
		return "未找到符号。", nil
	}
	var sb strings.Builder
	for _, s := range syms {
		sb.WriteString(fmt.Sprintf("- %s (kind=%d)\n", s.Name, s.Kind))
	}
	return sb.String(), nil
}

func (c *Client) locationQuery(method, file string, line, character int) (string, error) {
	c.didOpen(file)
	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI(file)},
		"position":     map[string]any{"line": line, "character": character},
	}
	res, err := c.call(method, params)
	if err != nil {
		return "", err
	}
	return formatLocations(res), nil
}

// didOpen 通知 server 打开文件（读取内容发送），保证后续查询可用。
func (c *Client) didOpen(file string) {
	data, err := readFile(file)
	if err != nil {
		return
	}
	c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": fileURI(file), "languageId": langID(file), "version": 1, "text": data,
		},
	})
}

// formatLocations 把 Location[] 或单个 Location 格式化为可读文本。
func formatLocations(res json.RawMessage) string {
	type loc struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct{ Line, Character int } `json:"start"`
		} `json:"range"`
	}
	var list []loc
	if err := json.Unmarshal(res, &list); err != nil {
		var single loc
		if err2 := json.Unmarshal(res, &single); err2 == nil && single.URI != "" {
			list = []loc{single}
		}
	}
	if len(list) == 0 {
		return "未找到结果。"
	}
	var sb strings.Builder
	for _, l := range list {
		p := strings.TrimPrefix(l.URI, "file://")
		if dec, err := url.PathUnescape(p); err == nil {
			p = dec
		}
		sb.WriteString(fmt.Sprintf("%s:%d:%d\n", p, l.Range.Start.Line+1, l.Range.Start.Character+1))
	}
	return sb.String()
}

// ---- JSON-RPC over LSP framing ----

func (c *Client) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.writeMessage(req); err != nil {
		return nil, err
	}

	type result struct {
		raw json.RawMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		for {
			raw, err := c.readMessage()
			if err != nil {
				ch <- result{nil, err}
				return
			}
			var resp struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(raw, &resp) != nil {
				continue
			}
			if resp.ID == nil || *resp.ID != id {
				continue // 服务器通知或其它响应，跳过
			}
			if resp.Error != nil {
				ch <- result{nil, fmt.Errorf("LSP 错误: %s", resp.Error.Message)}
				return
			}
			ch <- result{resp.Result, nil}
			return
		}
	}()

	select {
	case r := <-ch:
		return r.raw, r.err
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("LSP 调用 %s 超时", method)
	}
}

func (c *Client) notify(method string, params any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// writeMessage 以 LSP 帧格式(Content-Length 头 + body)写一条消息。
func (c *Client) writeMessage(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

// readMessage 读取一条 LSP 帧。
func (c *Client) readMessage() (json.RawMessage, error) {
	contentLength := 0
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // header 结束
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			v := strings.TrimSpace(line[len("content-length:"):])
			contentLength, _ = strconv.Atoi(v)
		}
	}
	if contentLength == 0 {
		return nil, fmt.Errorf("无 Content-Length")
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(c.stdout, buf); err != nil {
		return nil, err
	}
	return json.RawMessage(buf), nil
}

// Close 终止 LSP server 进程。
func (c *Client) Close() {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// ---- 辅助 ----

func fileURI(file string) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		abs = file
	}
	return "file://" + abs
}

func langID(file string) string {
	switch filepath.Ext(file) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	default:
		return "plaintext"
	}
}

func readFile(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
