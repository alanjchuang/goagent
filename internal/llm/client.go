// Package llm 实现对 OpenAI 兼容 Chat Completions 接口的调用。
//
// 这是 Python 版 src/lib/smolagents/models/ 的简化 Go 实现。原版用 litellm
// 做多 provider 路由，这里先支持 OpenAI 兼容端点（绝大多数国内外网关都兼容），
// 后续可在 Client 内按 model_id 前缀扩展不同 provider 的适配。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alanjchuang/goagent/internal/config"
)

// Role 表示消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 是一条对话消息（OpenAI 格式）。
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // role=tool 时回填
	Name       string     `json:"name,omitempty"`
}

// ToolCall 表示一次模型发起的工具调用。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // 固定 "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall 是工具调用的函数名与参数（参数为 JSON 字符串）。
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolSchema 描述一个可供模型调用的工具（OpenAI tools 格式）。
type ToolSchema struct {
	Type     string             `json:"type"` // "function"
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition 是工具的函数签名定义。
type FunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// TokenUsage 记录一次请求的 token 消耗。
type TokenUsage struct {
	InputTokens  int `json:"prompt_tokens"`
	OutputTokens int `json:"completion_tokens"`
}

// Client 封装一个针对特定 model_type 的 LLM 调用客户端。
type Client struct {
	cfg        config.ModelConfig
	modelType  string
	httpClient *http.Client
	// CumulativeUsage 累计 token 用量（对应 Python 版 monitor_metrics）。
	CumulativeUsage TokenUsage
}

// NewClient 基于全局配置构造一个针对 modelType 的客户端。
func NewClient(modelType string) (*Client, error) {
	if config.C == nil {
		return nil, fmt.Errorf("配置未加载，请先调用 config.Load()")
	}
	mc, err := config.C.LLM.ForType(modelType)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(mc.Timeout) * time.Second
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	resolved := modelType
	if resolved == "" {
		resolved = config.C.LLM.DefaultModelType
	}
	return &Client{
		cfg:        mc,
		modelType:  resolved,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

// chatRequest 是发往 /chat/completions 的请求体。
type chatRequest struct {
	Model       string       `json:"model"`
	Messages    []Message    `json:"messages"`
	Tools       []ToolSchema `json:"tools,omitempty"`
	Temperature float64      `json:"temperature"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
}

// chatResponse 是 /chat/completions 的响应体。
type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage TokenUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// stripProviderPrefix 去掉 litellm 风格的 provider 前缀（如 "openai/gpt-5" -> "gpt-5"）。
// OpenAI 兼容端点只认裸 model 名。
func stripProviderPrefix(modelID string) string {
	if i := strings.Index(modelID, "/"); i >= 0 {
		return modelID[i+1:]
	}
	return modelID
}

// Complete 发起一次对话补全调用，返回 assistant 消息。带简单指数退避重试。
func (c *Client) Complete(ctx context.Context, messages []Message, tools []ToolSchema) (*Message, error) {
	reqBody := chatRequest{
		Model:       stripProviderPrefix(c.cfg.Model),
		Messages:    messages,
		Tools:       tools,
		Temperature: c.cfg.Temperature,
		MaxTokens:   c.cfg.MaxTokens,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	retries := c.cfg.NumRetries
	if retries <= 0 {
		retries = 3
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"

	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt*attempt) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		msg, usage, err := c.doRequest(ctx, url, payload)
		if err != nil {
			lastErr = err
			continue
		}
		c.CumulativeUsage.InputTokens += usage.InputTokens
		c.CumulativeUsage.OutputTokens += usage.OutputTokens
		return msg, nil
	}
	return nil, fmt.Errorf("LLM 调用失败（已重试 %d 次）: %w", retries, lastErr)
}

func (c *Client) doRequest(ctx context.Context, url string, payload []byte) (*Message, TokenUsage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, TokenUsage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, TokenUsage{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, TokenUsage{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, TokenUsage{}, fmt.Errorf("解析响应失败: %w; body=%s", err, string(body))
	}
	if cr.Error != nil {
		return nil, TokenUsage{}, fmt.Errorf("API 错误: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return nil, TokenUsage{}, fmt.Errorf("响应不含 choices: %s", string(body))
	}
	msg := cr.Choices[0].Message
	msg.Role = RoleAssistant
	return &msg, cr.Usage, nil
}

// ModelID 返回底层 model_id（用于日志）。
func (c *Client) ModelID() string { return c.cfg.Model }
