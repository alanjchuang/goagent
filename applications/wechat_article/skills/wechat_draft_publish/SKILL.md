---
name: wechat_draft_publish
description: "微信公众号草稿创建技能：将本地文章 HTML、封面图和正文图片上传到目标公众号，替换正文图片为微信 URL，并创建草稿箱草稿。"
version: "1.0.0"
invocation-control:
  allow-model: true
---

# 微信公众号草稿创建技能

当用户要求“创建微信草稿”“上传到公众号草稿箱”“把文章发到微信公众号后台”“生成可预览草稿”时，使用本技能。

## 1. 目标公众号

默认目标公众号：**智能写码局**。

调用工具时优先使用 `config/llm.yaml` 中的 `model.wechat_official_account` 配置；也支持环境变量：

- `WECHAT_MP_ACCESS_TOKEN`
- `WECHAT_MP_APP_ID`
- `WECHAT_MP_APP_SECRET`

不要在输出中展示 AppSecret 或 access token。

## 2. 必须使用的工具

优先调用内置工具：`wechat_create_draft`。

工具会自动完成：

1. 如未提供 `thumb_media_id`，把 `cover_image_path` 上传为微信永久图片素材，获得封面 `thumb_media_id`。
2. 扫描 `content` / `content_file` 中的本地 `<img src="...">`。
3. 将正文中的本地图片通过微信 `media/uploadimg` 上传，获得微信图片 URL。
4. 替换正文 HTML 中的本地图片路径为微信图片 URL。
5. 调用 `draft/add` 新增草稿箱草稿。

## 3. 调用前检查

正式创建草稿前必须确认：

- `title` 已生成，建议不超过 32 个字。
- `digest` 已生成，建议不超过 128 个字。
- `content` 或 `content_file` 是微信公众号可用 HTML。
- 正文 HTML 必须直接以 `<section>`、`<p>` 等标签开始；不要包裹 `<![CDATA[ ... ]]>`，也不要输出 Markdown 代码围栏。
- 有封面：`cover_image_path` 或已上传好的 `thumb_media_id`。
- 正文图片如果是本地路径，应尽量为 jpg/png，且单图小于 1MB；否则微信上传可能失败。
- CTA 已按用户要求处理：默认以前期引流为主，资料包领取只是可选项。

如果缺少封面、HTML 或微信凭证，调用 `wechat_create_draft` 时设置 `dry_run=true`，输出缺失项，不要声称草稿已创建。

## 4. 推荐调用参数

```json
{
  "title": "文章标题",
  "author": "智能写码局",
  "digest": "文章摘要",
  "content_file": "outputs/wechat_article/<slug>/article.html",
  "cover_image_path": "outputs/images/generated/<cover>.jpeg",
  "content_source_url": "",
  "need_open_comment": 1,
  "only_fans_can_comment": 0,
  "dry_run": false
}
```

如果只是验证 payload：

```json
{
  "title": "文章标题",
  "content": "<p>正文</p>",
  "dry_run": true
}
```

## 5. 输出要求

工具调用后，最终回答必须包含：

- 草稿创建状态：`draft_created` / `draft_package_ready` / `blocked`。
- 微信草稿 `media_id`（如果已创建）。
- 封面上传结果：`thumb_media_id` 或失败原因。
- 正文图片替换清单：本地路径 → 微信 URL。
- 公众号后台人工预览提醒：创建草稿后仍需在 mp.weixin.qq.com 后台或手机预览确认。

## 6. 禁止事项

- 不要默认群发。
- 不要泄露 AppSecret、access token。
- 不要把本地图片路径直接留在最终微信 HTML 里。
- 不要在正文 content 中保留 `<![CDATA[`、`]]>`、```html 代码围栏等包装文本；微信草稿 API 接收的是 JSON 字符串字段，不需要 CDATA。
- 不要声称“已发布”，除非后续明确接入并调用群发/发布接口。
