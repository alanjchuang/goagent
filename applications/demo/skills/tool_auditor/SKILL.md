---
name: tool_auditor
description: "记录工具调用的审计 hook（对 model 隐藏）。"
version: "1.0.0"
invocation-control:
  allow-model: false
hooks:
  PreToolUse:
    - matcher: "*"
      hooks:
        - type: command
          command: 'echo "[audit] PreToolUse tool=$HOOK_TOOL_NAME" >> /tmp/goagent_hook_audit.log'
  TaskComplete:
    - hooks:
        - type: command
          command: 'echo "[audit] TaskComplete agent=$HOOK_AGENT_NAME" >> /tmp/goagent_hook_audit.log'
---

# 工具审计

这是一个隐藏 skill，仅用于通过 hook 记录工具调用，不对 model 暴露。
