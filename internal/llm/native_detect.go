// native_detect.go 实现 native tool_call 的三态检测。
//
// 对应 Python 版 litellm_model.py 的 supports_native_tool_calls 三态逻辑：
//   - "true":  始终带 tool schema，使用原生 tool_calls
//   - "false": 始终不带 schema，由调用方从文本解析工具调用
//   - "auto":  首次请求带 schema 探测；若模型返回了原生 tool_calls 则锁定为
//              原生模式，否则切换到文本解析模式（后续不再带 schema）
package llm

// nativeState 表示当前对原生 tool_call 支持的判定。
type nativeState int

const (
	nativeUnknown nativeState = iota // auto 模式下尚未探测
	nativeYes                        // 使用原生 tool_calls
	nativeNo                         // 使用文本解析
)

// UseNativeToolCalls 返回本次请求是否应携带 tool schema（即是否走原生 tool_call）。
func (c *Client) UseNativeToolCalls() bool {
	switch c.cfg.SupportsNativeToolCalls {
	case "true":
		return true
	case "false":
		return false
	default: // "auto" 或空：按探测状态决定
		switch c.nativeDetect {
		case nativeNo:
			return false
		default:
			// unknown（首次探测）或 yes：都带 schema
			return true
		}
	}
}

// UpdateNativeDetection 在 auto 模式下根据响应更新检测状态。
// 只在状态尚未确定（nativeUnknown）时生效，确定后不再翻转。
func (c *Client) UpdateNativeDetection(resp *Message) {
	if c.cfg.SupportsNativeToolCalls != "auto" && c.cfg.SupportsNativeToolCalls != "" {
		return
	}
	if c.nativeDetect != nativeUnknown {
		return
	}
	if resp != nil && len(resp.ToolCalls) > 0 {
		c.nativeDetect = nativeYes
	} else {
		c.nativeDetect = nativeNo
	}
}

// NativeMode 返回当前判定的可读字符串（用于日志）。
func (c *Client) NativeMode() string {
	switch c.cfg.SupportsNativeToolCalls {
	case "true":
		return "native(forced)"
	case "false":
		return "text-parse(forced)"
	}
	switch c.nativeDetect {
	case nativeYes:
		return "native(detected)"
	case nativeNo:
		return "text-parse(detected)"
	default:
		return "auto(probing)"
	}
}
