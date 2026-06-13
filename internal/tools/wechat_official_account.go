package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/alanjchuang/goagent/internal/config"
)

const (
	defaultWeChatModelType = "wechat_official_account"
	defaultWeChatAPIBase   = "https://api.weixin.qq.com"
)

var wechatTokenCache = struct {
	sync.Mutex
	accessToken string
	expiresAt   time.Time
}{}

// WeChatCreateDraft 上传封面/正文图片并创建微信公众号草稿。
type WeChatCreateDraft struct{}

func (WeChatCreateDraft) Name() string { return "wechat_create_draft" }

func (WeChatCreateDraft) Description() string {
	return "调用微信公众号官方 API 上传封面和正文图片，并创建草稿箱图文草稿；默认只创建草稿，不群发。可 dry_run 预览 payload。"
}

func (WeChatCreateDraft) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{
				"type":        "string",
				"description": "公众号图文标题，建议不超过 32 个字。",
			},
			"author": map[string]any{
				"type":        "string",
				"description": "作者，建议不超过 16 个字。",
			},
			"digest": map[string]any{
				"type":        "string",
				"description": "摘要，建议不超过 128 个字。",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "公众号正文 HTML；正文 img 本地路径会自动上传到微信并替换为微信 URL。",
			},
			"content_file": map[string]any{
				"type":        "string",
				"description": "可选：从本地 HTML 文件读取 content，优先级低于 content。",
			},
			"content_source_url": map[string]any{
				"type":        "string",
				"description": "阅读原文链接，可为空。",
			},
			"cover_image_path": map[string]any{
				"type":        "string",
				"description": "封面图本地路径；未提供 thumb_media_id 时必填，工具会上传为永久素材。",
			},
			"thumb_media_id": map[string]any{
				"type":        "string",
				"description": "已上传的永久素材 media_id；提供后可跳过封面上传。",
			},
			"need_open_comment": map[string]any{
				"type":        "integer",
				"description": "是否打开评论：0 否，1 是；默认 0。",
			},
			"only_fans_can_comment": map[string]any{
				"type":        "integer",
				"description": "是否仅粉丝可评论：0 否，1 是；默认 0。",
			},
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "只生成草稿 payload 和检查项，不实际调用微信 API。默认 false。",
			},
			"access_token": map[string]any{
				"type":        "string",
				"description": "可选：直接传微信公众号 access_token；否则从环境变量或 appid/secret 获取。",
			},
			"app_id": map[string]any{
				"type":        "string",
				"description": "可选：公众号 AppID；默认读取 WECHAT_MP_APP_ID 或 config/llm.yaml 的 model.wechat_official_account.model。",
			},
			"app_secret": map[string]any{
				"type":        "string",
				"description": "可选：公众号 AppSecret；默认读取 WECHAT_MP_APP_SECRET 或 config/llm.yaml 的 model.wechat_official_account.api_key。",
			},
			"api_base": map[string]any{
				"type":        "string",
				"description": "可选：微信 API 根地址，默认 https://api.weixin.qq.com。",
			},
		},
		"required": []string{"title"},
	}
}

func (WeChatCreateDraft) Execute(args map[string]any) (string, error) {
	dryRun := boolArgDefault(args, "dry_run", false)
	article, err := buildWeChatDraftArticle(args)
	if err != nil {
		return "", err
	}

	cfg := resolveWeChatConfig(args)
	result := map[string]any{
		"dry_run": dryRun,
		"note":    "微信公众号草稿 API 只创建草稿，不会群发；草稿创建后仍建议在公众号后台手机预览和人工确认。",
		"limits": []string{
			"正文图片必须使用微信 uploadimg 返回的 URL，外部图片可能被过滤。",
			"封面 thumb_media_id 必须是永久素材 media_id。",
			"uploadimg 正文图片官方限制 jpg/png 且小于 1MB。",
		},
	}

	if dryRun {
		payload := wechatDraftPayload{Articles: []wechatDraftArticle{article}}
		result["draft_payload"] = payload
		result["missing_before_real_call"] = missingWeChatDraftInputs(article, cfg)
		out, _ := json.MarshalIndent(result, "", "  ")
		return string(out), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutFromWeChatConfig(cfg))
	defer cancel()

	accessToken, err := getWeChatAccessToken(ctx, cfg)
	if err != nil {
		return "", err
	}

	coverUpload := map[string]any{}
	if article.ThumbMediaID == "" {
		coverPath := strings.TrimSpace(strArg(args, "cover_image_path"))
		if coverPath == "" {
			return "", fmt.Errorf("缺少封面：请提供 thumb_media_id 或 cover_image_path")
		}
		uploaded, err := uploadWeChatPermanentImage(ctx, cfg.APIBase, accessToken, coverPath)
		if err != nil {
			return "", fmt.Errorf("上传封面永久素材失败: %w", err)
		}
		article.ThumbMediaID = uploaded.MediaID
		coverUpload["media_id"] = uploaded.MediaID
		coverUpload["url"] = uploaded.URL
		coverUpload["local_path"] = coverPath
	}

	content, replacements, err := replaceLocalImagesForWeChat(ctx, cfg.APIBase, accessToken, article.Content)
	if err != nil {
		return "", err
	}
	article.Content = content

	payload := wechatDraftPayload{Articles: []wechatDraftArticle{article}}
	draftResp, err := addWeChatDraft(ctx, cfg.APIBase, accessToken, payload)
	if err != nil {
		return "", err
	}

	result["status"] = "draft_created"
	result["media_id"] = draftResp.MediaID
	result["cover_upload"] = coverUpload
	result["body_image_replacements"] = replacements
	result["draft_payload"] = payload
	out, _ := json.MarshalIndent(result, "", "  ")
	return string(out), nil
}

type wechatToolConfig struct {
	APIBase     string
	AccessToken string
	AppID       string
	AppSecret   string
	TimeoutSec  int
}

type wechatDraftPayload struct {
	Articles []wechatDraftArticle `json:"articles"`
}

type wechatDraftArticle struct {
	ArticleType        string `json:"article_type,omitempty"`
	Title              string `json:"title"`
	Author             string `json:"author,omitempty"`
	Digest             string `json:"digest,omitempty"`
	Content            string `json:"content"`
	ContentSourceURL   string `json:"content_source_url,omitempty"`
	ThumbMediaID       string `json:"thumb_media_id,omitempty"`
	NeedOpenComment    int    `json:"need_open_comment,omitempty"`
	OnlyFansCanComment int    `json:"only_fans_can_comment,omitempty"`
}

type wechatAPIError struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

type wechatAccessTokenResponse struct {
	wechatAPIError
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type wechatUploadMediaResponse struct {
	wechatAPIError
	MediaID string `json:"media_id"`
	URL     string `json:"url"`
}

type wechatUploadImgResponse struct {
	wechatAPIError
	URL string `json:"url"`
}

type wechatDraftResponse struct {
	wechatAPIError
	MediaID string `json:"media_id"`
}

type wechatImageReplacement struct {
	LocalPath string `json:"local_path"`
	WeChatURL string `json:"wechat_url"`
}

func buildWeChatDraftArticle(args map[string]any) (wechatDraftArticle, error) {
	title := strings.TrimSpace(strArg(args, "title"))
	if title == "" {
		return wechatDraftArticle{}, fmt.Errorf("缺少参数 title")
	}
	content := strings.TrimSpace(strArg(args, "content"))
	if content == "" {
		contentFile := strings.TrimSpace(strArg(args, "content_file"))
		if contentFile != "" {
			if err := activePolicy.CheckPath(contentFile); err != nil {
				return wechatDraftArticle{}, err
			}
			data, err := os.ReadFile(contentFile)
			if err != nil {
				return wechatDraftArticle{}, err
			}
			content = string(data)
		}
	}
	if content == "" {
		return wechatDraftArticle{}, fmt.Errorf("缺少参数 content 或 content_file")
	}
	content = sanitizeWeChatHTMLContent(content)
	return wechatDraftArticle{
		ArticleType:        firstNonEmpty(strings.TrimSpace(strArg(args, "article_type")), "news"),
		Title:              title,
		Author:             strings.TrimSpace(strArg(args, "author")),
		Digest:             strings.TrimSpace(strArg(args, "digest")),
		Content:            content,
		ContentSourceURL:   strings.TrimSpace(strArg(args, "content_source_url")),
		ThumbMediaID:       strings.TrimSpace(strArg(args, "thumb_media_id")),
		NeedOpenComment:    intArgDefault(args, "need_open_comment", 0),
		OnlyFansCanComment: intArgDefault(args, "only_fans_can_comment", 0),
	}, nil
}

func sanitizeWeChatHTMLContent(content string) string {
	content = strings.TrimSpace(strings.TrimPrefix(content, "\ufeff"))
	content = stripMarkdownFence(content)

	// 微信 draft/add 的 content 是 JSON 字符串字段，不需要也不应该包 CDATA。
	// 有些模型会把 HTML 放进 <![CDATA[ ... ]]>，微信会把 CDATA 文本原样展示。
	cdataRe := regexp.MustCompile(`(?is)^\s*<\s*!\s*\[CDATA\[\s*([\s\S]*?)\s*\]\]>\s*$`)
	if m := cdataRe.FindStringSubmatch(content); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	cdataStartRe := regexp.MustCompile(`(?is)^\s*<\s*!\s*\[CDATA\[\s*`)
	if cdataStartRe.MatchString(content) {
		content = cdataStartRe.ReplaceAllString(content, "")
		content = regexp.MustCompile(`(?is)\s*\]\]>\s*$`).ReplaceAllString(content, "")
		return normalizeWeChatHTMLLists(strings.TrimSpace(content))
	}
	return normalizeWeChatHTMLLists(content)
}

func normalizeWeChatHTMLLists(content string) string {
	// 微信草稿箱对原生 ol/ul/li 的渲染不稳定，可能把多个列表合并成连续编号。
	// 发布前统一转成手写项目符号段落，避免「两件事」在草稿中变成 1、2、3、4、5。
	listTagRe := regexp.MustCompile(`(?is)</?\s*(?:ol|ul)\b[^>]*>`)
	content = listTagRe.ReplaceAllString(content, "")
	liOpenRe := regexp.MustCompile(`(?is)<\s*li\b[^>]*>`)
	content = liOpenRe.ReplaceAllString(content, `<p style="margin:0 0 8px;padding-left:1em;text-indent:-1em;">• `)
	liCloseRe := regexp.MustCompile(`(?is)</\s*li\s*>`)
	content = liCloseRe.ReplaceAllString(content, `</p>`)
	return content
}

func stripMarkdownFence(content string) string {
	fenceRe := regexp.MustCompile("(?is)^\\s*```(?:html|xml)?\\s*\\n([\\s\\S]*?)\\n```\\s*$")
	if m := fenceRe.FindStringSubmatch(content); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return content
}

func resolveWeChatConfig(args map[string]any) wechatToolConfig {
	mc, hasCfg := wechatModelConfig()
	return wechatToolConfig{
		APIBase: firstNonEmpty(
			strings.TrimSpace(strArg(args, "api_base")),
			os.Getenv("WECHAT_MP_API_BASE"),
			configValue(hasCfg, mc.BaseURL),
			defaultWeChatAPIBase,
		),
		AccessToken: firstNonEmpty(
			strings.TrimSpace(strArg(args, "access_token")),
			os.Getenv("WECHAT_MP_ACCESS_TOKEN"),
		),
		AppID: firstNonEmpty(
			strings.TrimSpace(strArg(args, "app_id")),
			os.Getenv("WECHAT_MP_APP_ID"),
			os.Getenv("WECHAT_MP_APPID"),
			configValue(hasCfg, mc.Model),
		),
		AppSecret: firstNonEmpty(
			strings.TrimSpace(strArg(args, "app_secret")),
			os.Getenv("WECHAT_MP_APP_SECRET"),
			os.Getenv("WECHAT_MP_APPSECRET"),
			configValue(hasCfg, mc.APIKey),
		),
		TimeoutSec: mc.Timeout,
	}
}

func wechatModelConfig() (config.ModelConfig, bool) {
	if config.C == nil {
		return config.ModelConfig{}, false
	}
	modelType := firstNonEmpty(os.Getenv("WECHAT_MP_MODEL_TYPE"), defaultWeChatModelType)
	mc, err := config.C.LLM.ForType(modelType)
	if err != nil {
		return config.ModelConfig{}, false
	}
	return mc, true
}

func timeoutFromWeChatConfig(cfg wechatToolConfig) time.Duration {
	if cfg.TimeoutSec <= 0 {
		return 60 * time.Second
	}
	return time.Duration(cfg.TimeoutSec) * time.Second
}

func missingWeChatDraftInputs(article wechatDraftArticle, cfg wechatToolConfig) []string {
	missing := []string{}
	if article.ThumbMediaID == "" {
		missing = append(missing, "thumb_media_id 或 cover_image_path")
	}
	if cfg.AccessToken == "" && (cfg.AppID == "" || cfg.AppSecret == "") {
		missing = append(missing, "WECHAT_MP_ACCESS_TOKEN 或 WECHAT_MP_APP_ID + WECHAT_MP_APP_SECRET")
	}
	return missing
}

func getWeChatAccessToken(ctx context.Context, cfg wechatToolConfig) (string, error) {
	if cfg.AccessToken != "" {
		return cfg.AccessToken, nil
	}
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return "", fmt.Errorf("缺少微信公众号凭证：请设置 WECHAT_MP_ACCESS_TOKEN，或设置 WECHAT_MP_APP_ID/WECHAT_MP_APP_SECRET，或在 config/llm.yaml 的 model.wechat_official_account 中配置 model=app_id、api_key=app_secret")
	}

	wechatTokenCache.Lock()
	if wechatTokenCache.accessToken != "" && time.Now().Before(wechatTokenCache.expiresAt.Add(-5*time.Minute)) {
		token := wechatTokenCache.accessToken
		wechatTokenCache.Unlock()
		return token, nil
	}
	wechatTokenCache.Unlock()

	endpoint := fmt.Sprintf("%s/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s", strings.TrimRight(cfg.APIBase, "/"), cfg.AppID, cfg.AppSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	var resp wechatAccessTokenResponse
	if err := doWeChatJSON(req, &resp); err != nil {
		return "", err
	}
	if err := resp.apiErr(); err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("微信未返回 access_token")
	}
	expiresIn := resp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 7200
	}
	wechatTokenCache.Lock()
	wechatTokenCache.accessToken = resp.AccessToken
	wechatTokenCache.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	wechatTokenCache.Unlock()
	return resp.AccessToken, nil
}

func uploadWeChatPermanentImage(ctx context.Context, apiBase, accessToken, path string) (wechatUploadMediaResponse, error) {
	endpoint := fmt.Sprintf("%s/cgi-bin/material/add_material?access_token=%s&type=image", strings.TrimRight(apiBase, "/"), accessToken)
	var out wechatUploadMediaResponse
	if err := uploadWeChatFile(ctx, endpoint, path, &out); err != nil {
		return out, err
	}
	if err := out.apiErr(); err != nil {
		return out, err
	}
	if out.MediaID == "" {
		return out, fmt.Errorf("微信永久素材上传未返回 media_id")
	}
	return out, nil
}

func uploadWeChatContentImage(ctx context.Context, apiBase, accessToken, path string) (wechatUploadImgResponse, error) {
	endpoint := fmt.Sprintf("%s/cgi-bin/media/uploadimg?access_token=%s", strings.TrimRight(apiBase, "/"), accessToken)
	var out wechatUploadImgResponse
	if err := uploadWeChatFile(ctx, endpoint, path, &out); err != nil {
		return out, err
	}
	if err := out.apiErr(); err != nil {
		return out, err
	}
	if out.URL == "" {
		return out, fmt.Errorf("微信正文图片上传未返回 url")
	}
	return out, nil
}

func uploadWeChatFile(ctx context.Context, endpoint, path string, out any) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("empty file path")
	}
	if err := activePolicy.CheckPath(path); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("media", filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return doWeChatJSON(req, out)
}

func replaceLocalImagesForWeChat(ctx context.Context, apiBase, accessToken, content string) (string, []wechatImageReplacement, error) {
	re := regexp.MustCompile(`(?i)<img\b[^>]*\bsrc=["']([^"']+)["'][^>]*>`)
	matches := re.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return content, nil, nil
	}
	replacements := []wechatImageReplacement{}
	seen := map[string]string{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		src := strings.TrimSpace(match[1])
		if src == "" || strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "data:") || strings.HasPrefix(src, "{{") {
			continue
		}
		if mapped, ok := seen[src]; ok {
			content = strings.ReplaceAll(content, src, mapped)
			continue
		}
		localPath := strings.TrimPrefix(src, "file://")
		uploaded, err := uploadWeChatContentImage(ctx, apiBase, accessToken, localPath)
		if err != nil {
			return content, replacements, fmt.Errorf("上传正文图片 %s 失败: %w", src, err)
		}
		seen[src] = uploaded.URL
		content = strings.ReplaceAll(content, src, uploaded.URL)
		replacements = append(replacements, wechatImageReplacement{LocalPath: localPath, WeChatURL: uploaded.URL})
	}
	return content, replacements, nil
}

func addWeChatDraft(ctx context.Context, apiBase, accessToken string, payload wechatDraftPayload) (wechatDraftResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return wechatDraftResponse{}, err
	}
	endpoint := fmt.Sprintf("%s/cgi-bin/draft/add?access_token=%s", strings.TrimRight(apiBase, "/"), accessToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return wechatDraftResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	var out wechatDraftResponse
	if err := doWeChatJSON(req, &out); err != nil {
		return out, err
	}
	if err := out.apiErr(); err != nil {
		return out, err
	}
	if out.MediaID == "" {
		return out, fmt.Errorf("微信新增草稿未返回 media_id")
	}
	return out, nil
}

func doWeChatJSON(req *http.Request, out any) error {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("wechat api status=%d, body=%s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode wechat response failed: %w; body=%s", err, string(body))
	}
	return nil
}

func (e wechatAPIError) apiErr() error {
	if e.ErrCode == 0 {
		return nil
	}
	if e.ErrCode == 40164 {
		return fmt.Errorf("wechat api errcode=%d errmsg=%s；当前机器出口 IP 不在公众号后台 IP 白名单内，请在 mp.weixin.qq.com 的开发配置中加入 errmsg 里提示的 IP 后重试", e.ErrCode, e.ErrMsg)
	}
	return fmt.Errorf("wechat api errcode=%d errmsg=%s", e.ErrCode, e.ErrMsg)
}
