// web_search.go 实现 web_search 工具，接入火山方舟 ARK 的 /responses 接口。
//
// 对应 AgentLoom applications/web_search。ARK 的 web_search 是模型侧内置工具：
// 我们向 /responses 发一个带 {"type":"web_search"} 的请求，模型自动联网检索并
// 在回复中给出综合答案。本工具复用 powerful 模型的 base_url/api_key/model。
//
// 注意：需要在火山引擎控制台为账号开通 web search 插件，否则返回 ToolNotOpen。
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alanjchuang/goagent/internal/config"
)

type WebSearch struct{}

// stripPrefix 去掉 litellm 风格的 provider 前缀（如 "openai/gpt-5" -> "gpt-5"）。
func stripPrefix(modelID string) string {
	if i := strings.Index(modelID, "/"); i >= 0 {
		return modelID[i+1:]
	}
	return modelID
}

func (WebSearch) Name() string { return "web_search" }
func (WebSearch) Description() string {
	return "联网搜索并返回综合答案。用于获取实时信息（新闻、最新资料等）。"
}
func (WebSearch) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":       map[string]any{"type": "string", "description": "搜索的问题或关键词"},
			"max_keyword": map[string]any{"type": "integer", "description": "最大检索关键词数，默认 3"},
		},
		"required": []string{"query"},
	}
}

// responsesRequest 对应 ARK /responses 的请求体。
type responsesRequest struct {
	Model  string           `json:"model"`
	Stream bool             `json:"stream"`
	Tools  []map[string]any `json:"tools"`
	Input  []responsesInput `json:"input"`
}

type responsesInput struct {
	Role    string                   `json:"role"`
	Content []map[string]interface{} `json:"content"`
}

func (WebSearch) Execute(args map[string]any) (string, error) {
	query := strArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("缺少参数 query")
	}
	if config.C == nil {
		return "", fmt.Errorf("配置未加载")
	}
	// 复用 powerful 模型的接入信息（base_url/api_key/model）。
	mc, err := config.C.LLM.ForType("")
	if err != nil {
		return "", fmt.Errorf("无法获取模型配置用于 web_search: %w", err)
	}

	maxKeyword := 3
	if v, ok := args["max_keyword"].(float64); ok && v > 0 {
		maxKeyword = int(v)
	}

	reqBody := responsesRequest{
		Model:  stripPrefix(mc.Model),
		Stream: false,
		Tools:  []map[string]any{{"type": "web_search", "max_keyword": maxKeyword}},
		Input: []responsesInput{{
			Role:    "user",
			Content: []map[string]interface{}{{"type": "input_text", "text": query}},
		}},
	}
	payload, _ := json.Marshal(reqBody)

	url := strings.TrimRight(mc.BaseURL, "/") + "/responses"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mc.APIKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("web_search 失败 HTTP %d: %s", resp.StatusCode, string(body))
	}
	return extractResponsesText(body), nil
}

// extractResponsesText 从 /responses 的 JSON 响应中提取文本输出。
// ARK responses 的结构为 {"output": [{"content": [{"type":"output_text","text":...}]}]}。
func extractResponsesText(body []byte) string {
	var parsed struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return string(body) // 解析不了就原样返回，便于调试
	}
	if parsed.Error != nil {
		return "web_search 错误: " + parsed.Error.Message
	}
	var sb strings.Builder
	for _, o := range parsed.Output {
		for _, c := range o.Content {
			if c.Text != "" {
				sb.WriteString(c.Text)
				sb.WriteString("\n")
			}
		}
	}
	if sb.Len() == 0 {
		return string(body)
	}
	return strings.TrimSpace(sb.String())
}
