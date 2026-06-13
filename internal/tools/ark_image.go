package tools

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alanjchuang/goagent/internal/config"
	"github.com/volcengine/volcengine-go-sdk/service/arkruntime"
	"github.com/volcengine/volcengine-go-sdk/service/arkruntime/model"
	"github.com/volcengine/volcengine-go-sdk/volcengine"
)

const defaultArkImageModel = "doubao-seedream-4-0-250828"
const defaultArkImageModelType = "image"

// ArkGenerateImages 调用火山方舟文生图接口，返回图片 URL，并默认下载到 outputs/。
type ArkGenerateImages struct{}

func (ArkGenerateImages) Name() string { return "ark_generate_images" }
func (ArkGenerateImages) Description() string {
	return "调用火山方舟图片生成 API 生成图片，返回图片 URL、本地文件路径、尺寸、用量和错误信息。适合公众号封面、正文插图、金句图等。"
}
func (ArkGenerateImages) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "图片生成提示词。建议包含用途、主体、风格、比例/版式、文字留白、公众号封面/插图等要求。",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "火山方舟生图模型，默认读取 ARK_IMAGE_MODEL，未设置则使用 doubao-seedream-4-0-250828。",
			},
			"size": map[string]any{
				"type":        "string",
				"description": "图片尺寸/清晰度，例如 2K、1K、adaptive。默认 2K。",
			},
			"max_images": map[string]any{
				"type":        "integer",
				"description": "连续生图最大张数，默认 1，最大建议 4。",
			},
			"watermark": map[string]any{
				"type":        "boolean",
				"description": "是否添加水印，默认 true。",
			},
			"sequential_image_generation": map[string]any{
				"type":        "string",
				"description": "连续生图模式：auto 或 disabled，默认 auto。",
			},
			"download_images": map[string]any{
				"type":        "boolean",
				"description": "是否下载生成图片到本地 outputs/，默认 true。",
			},
			"output_dir": map[string]any{
				"type":        "string",
				"description": "图片下载目录，默认 outputs/images/generated。",
			},
		},
		"required": []string{"prompt"},
	}
}

func (ArkGenerateImages) Execute(args map[string]any) (string, error) {
	prompt := strings.TrimSpace(strArg(args, "prompt"))
	if prompt == "" {
		return "", fmt.Errorf("缺少参数 prompt")
	}

	mc, hasImageConfig := arkImageModelConfig()
	apiKey := firstNonEmpty(
		os.Getenv("ARK_IMAGE_API_KEY"),
		configValue(hasImageConfig, mc.APIKey),
		os.Getenv("ARK_API_KEY"),
	)
	if apiKey == "" {
		return "", fmt.Errorf("缺少火山方舟图片 API Key：请在 config/llm.yaml 的 model.image.api_key 中配置，或设置 ARK_IMAGE_API_KEY / ARK_API_KEY")
	}

	imageModel := strings.TrimSpace(strArg(args, "model"))
	if imageModel == "" {
		imageModel = firstNonEmpty(os.Getenv("ARK_IMAGE_MODEL"), configValue(hasImageConfig, mc.Model), defaultArkImageModel)
	}

	size := strings.TrimSpace(strArg(args, "size"))
	if size == "" {
		size = "2K"
	}
	maxImages := intArgDefault(args, "max_images", 1)
	if maxImages < 1 {
		maxImages = 1
	}
	if maxImages > 4 {
		maxImages = 4
	}
	watermark := boolArgDefault(args, "watermark", true)
	sequential := strings.TrimSpace(strArg(args, "sequential_image_generation"))
	if sequential == "" {
		sequential = model.SequentialImageGenerationAuto
	}
	seq := model.SequentialImageGeneration(sequential)
	downloadImages := boolArgDefault(args, "download_images", true)
	outputDir := strings.TrimSpace(strArg(args, "output_dir"))
	if outputDir == "" {
		outputDir = "outputs/images/generated"
	}

	client := arkruntime.NewClientWithApiKey(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	req := model.GenerateImagesRequest{
		Model:                     imageModel,
		Prompt:                    prompt,
		Size:                      volcengine.String(size),
		ResponseFormat:            volcengine.String(model.GenerateImagesResponseFormatURL),
		Watermark:                 volcengine.Bool(watermark),
		SequentialImageGeneration: &seq,
		SequentialImageGenerationOptions: &model.SequentialImageGenerationOptions{
			MaxImages: &maxImages,
		},
	}

	stream, err := client.GenerateImagesStreaming(ctx, req)
	if err != nil {
		return "", fmt.Errorf("调用 GenerateImagesStreaming 失败: %w", err)
	}
	defer stream.Close()

	type imageResult struct {
		Index       int64  `json:"index"`
		Size        string `json:"size"`
		URL         string `json:"url"`
		LocalPath   string `json:"local_path,omitempty"`
		DownloadErr string `json:"download_error,omitempty"`
	}
	type partialError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	result := struct {
		Model     string         `json:"model"`
		Prompt    string         `json:"prompt"`
		Size      string         `json:"size"`
		Images    []imageResult  `json:"images"`
		Errors    []partialError `json:"errors,omitempty"`
		Usage     any            `json:"usage,omitempty"`
		Generated int            `json:"generated"`
		Note      string         `json:"note"`
	}{
		Model:  imageModel,
		Prompt: prompt,
		Size:   size,
		Note:   "请下载图片并记录生成时间、模型、prompt 和用途；用于公众号前建议人工复核水印、版权和内容合规。",
	}

	for {
		recv, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("流式生成图片失败: %w", err)
		}

		switch recv.Type {
		case model.ImageGenerationStreamEventPartialFailed:
			if recv.Error != nil {
				result.Errors = append(result.Errors, partialError{Code: recv.Error.Code, Message: recv.Error.Message})
				if strings.EqualFold(recv.Error.Code, "InternalServiceError") {
					result.Generated = len(result.Images)
					out, _ := json.MarshalIndent(result, "", "  ")
					return string(out), nil
				}
			}
		case model.ImageGenerationStreamEventPartialSucceeded:
			if recv.Error == nil && recv.Url != nil {
				img := imageResult{Index: recv.ImageIndex, Size: recv.Size, URL: *recv.Url}
				if downloadImages {
					localPath, err := downloadImageToOutput(ctx, img.URL, outputDir, fmt.Sprintf("generated_%02d", recv.ImageIndex+1))
					if err != nil {
						img.DownloadErr = err.Error()
					} else {
						img.LocalPath = localPath
					}
				}
				result.Images = append(result.Images, img)
			}
		case model.ImageGenerationStreamEventCompleted:
			if recv.Error != nil {
				result.Errors = append(result.Errors, partialError{Code: recv.Error.Code, Message: recv.Error.Message})
			}
			if recv.Usage != nil {
				result.Usage = recv.Usage
			}
		}
	}

	result.Generated = len(result.Images)
	out, _ := json.MarshalIndent(result, "", "  ")
	return string(out), nil
}

func arkImageModelConfig() (config.ModelConfig, bool) {
	if config.C == nil {
		return config.ModelConfig{}, false
	}
	modelType := firstNonEmpty(os.Getenv("ARK_IMAGE_MODEL_TYPE"), defaultArkImageModelType)
	mc, err := config.C.LLM.ForType(modelType)
	if err != nil {
		return config.ModelConfig{}, false
	}
	return mc, true
}

func configValue(ok bool, value string) string {
	if !ok {
		return ""
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func downloadImageToOutput(ctx context.Context, imageURL, outputDir, filenamePrefix string) (string, error) {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return "", fmt.Errorf("empty image url")
	}
	if outputDir == "" {
		outputDir = "outputs/images"
	}
	if err := activePolicy.CheckPath(outputDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", fmt.Errorf("create image download request failed: %w", err)
	}
	req.Header.Set("User-Agent", "goagent-image-downloader/1.0")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download image failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("download image status=%d", resp.StatusCode)
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if contentType != "" && !strings.HasPrefix(contentType, "image/") && !strings.Contains(contentType, "octet-stream") {
		return "", fmt.Errorf("downloaded content is not image: %s", contentType)
	}

	ext := imageExtFromURLOrContentType(imageURL, contentType)
	if filenamePrefix == "" {
		filenamePrefix = "image"
	}
	h := sha1.Sum([]byte(imageURL))
	filename := fmt.Sprintf("%s_%s%s", sanitizeFilename(filenamePrefix), hex.EncodeToString(h[:])[:10], ext)
	path := filepath.Join(outputDir, filename)
	if err := activePolicy.CheckPath(path); err != nil {
		return "", err
	}
	out, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("save image failed: %w", err)
	}
	return path, nil
}

func imageExtFromURLOrContentType(rawURL, contentType string) string {
	if u, err := url.Parse(rawURL); err == nil {
		if ext := strings.ToLower(filepath.Ext(u.Path)); ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".gif" {
			return ext
		}
	}
	switch {
	case strings.Contains(contentType, "jpeg") || strings.Contains(contentType, "jpg"):
		return ".jpg"
	case strings.Contains(contentType, "png"):
		return ".png"
	case strings.Contains(contentType, "webp"):
		return ".webp"
	case strings.Contains(contentType, "gif"):
		return ".gif"
	default:
		return ".jpg"
	}
}

func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "image"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "image"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

// LicensedImageSearchStrategy 生成授权图库搜索策略，并可用 volc_web_search 辅助检索来源线索。
type LicensedImageSearchStrategy struct{}

func (LicensedImageSearchStrategy) Name() string { return "licensed_image_search_strategy" }
func (LicensedImageSearchStrategy) Description() string {
	return "为公众号配图生成网上找授权图策略：推荐图库、搜索关键词、版权记录模板和风险提示；可选调用 volc_web_search 获取候选来源线索。"
}
func (LicensedImageSearchStrategy) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"topic": map[string]any{
				"type":        "string",
				"description": "文章主题或图片主题。",
			},
			"usage": map[string]any{
				"type":        "string",
				"description": "图片用途，例如公众号封面、正文配图、产品截图、新闻事件图、金句图。",
			},
			"style": map[string]any{
				"type":        "string",
				"description": "期望风格，例如科技感、极简、真实办公场景、插画。",
			},
			"run_search": map[string]any{
				"type":        "boolean",
				"description": "是否调用 volc_web_search 获取候选网页来源线索，默认 false。",
			},
			"run_image_search": map[string]any{
				"type":        "boolean",
				"description": "是否调用 volc_web_search 的 image 搜索并下载候选图到 outputs/images/search，默认 false。",
			},
		},
		"required": []string{"topic"},
	}
}

func (LicensedImageSearchStrategy) Execute(args map[string]any) (string, error) {
	topic := strings.TrimSpace(strArg(args, "topic"))
	if topic == "" {
		return "", fmt.Errorf("缺少参数 topic")
	}
	usage := strings.TrimSpace(strArg(args, "usage"))
	if usage == "" {
		usage = "公众号配图"
	}
	style := strings.TrimSpace(strArg(args, "style"))
	if style == "" {
		style = "清晰、可商用、适合移动端阅读"
	}

	queries := []string{
		fmt.Sprintf("%s %s site:unsplash.com", topic, style),
		fmt.Sprintf("%s %s site:pexels.com", topic, style),
		fmt.Sprintf("%s %s site:pixabay.com", topic, style),
		fmt.Sprintf("%s %s site:commons.wikimedia.org", topic, style),
		fmt.Sprintf("%s official press kit images", topic),
	}

	type strategy struct {
		Topic              string   `json:"topic"`
		Usage              string   `json:"usage"`
		Style              string   `json:"style"`
		PreferredSources   []string `json:"preferred_sources"`
		SearchQueries      []string `json:"search_queries"`
		RejectSources      []string `json:"reject_sources"`
		LicenseChecklist   []string `json:"license_checklist"`
		AttributionRecord  []string `json:"attribution_record_fields"`
		RiskNotes          []string `json:"risk_notes"`
		SearchSummary      string   `json:"search_summary,omitempty"`
		ImageSearchSummary string   `json:"image_search_summary,omitempty"`
	}

	result := strategy{
		Topic: topic,
		Usage: usage,
		Style: style,
		PreferredSources: []string{
			"Unsplash：适合场景图/氛围图，发布前仍需确认当前图片授权条款。",
			"Pexels：适合人物、办公、生活方式图片，发布前确认授权条款。",
			"Pixabay：适合插画、矢量图、通用素材，发布前确认授权条款。",
			"Wikimedia Commons：适合百科/历史/公开资料图，必须核对具体 license 和署名要求。",
			"品牌/产品官方 press kit 或媒体资源页：适合产品截图、Logo、发布会图。",
			"自行 AI 生成：适合封面、概念图、金句图，需记录模型、prompt、生成时间。",
		},
		SearchQueries: queries,
		RejectSources: []string{
			"不要直接复用公众号、小红书、知乎、微博、新闻站、个人博客中的图片。",
			"不要使用带明显摄影师/机构版权水印且无授权说明的图片。",
			"不要裁掉来源、水印或版权声明后使用。",
		},
		LicenseChecklist: []string{
			"确认图片页面明确允许商业使用或符合当前公众号用途。",
			"确认是否需要署名、是否禁止改图、是否禁止用于广告/推广。",
			"保存图片详情页 URL，而不是只保存 CDN 图片 URL。",
			"涉及人物肖像、商标、医疗/金融/教育场景时增加人工复核。",
			"发布前保留截图或记录：授权条款、作者、下载时间、使用位置。",
		},
		AttributionRecord: []string{"image_id", "usage", "source_page_url", "image_url", "author", "license", "download_time", "modification", "article_slug"},
		RiskNotes: []string{
			"图库授权条款可能变化，最终以下载时页面说明为准。",
			"Wikimedia Commons 的不同图片 license 不同，不能只因为来自 Commons 就默认可商用。",
			"产品截图和 Logo 即使来自官网，也可能受商标/品牌使用规范约束。",
		},
	}

	if boolArgDefault(args, "run_search", boolArgDefault(args, "run_web_search", false)) {
		searchQuery := strings.Join(queries, " OR ")
		out, err := (VolcWebSearch{}).Execute(map[string]any{"query": searchQuery, "count": float64(5)})
		if err != nil {
			result.SearchSummary = "volc_web_search 调用失败: " + err.Error()
		} else {
			result.SearchSummary = out
		}
	}
	if boolArgDefault(args, "run_image_search", false) {
		imageQuery := fmt.Sprintf("%s %s %s", topic, usage, style)
		out, err := (VolcWebSearch{}).Execute(map[string]any{
			"query":           imageQuery,
			"count":           float64(5),
			"search_type":     "image",
			"download_images": true,
			"output_dir":      "outputs/images/search",
		})
		if err != nil {
			result.ImageSearchSummary = "volc_web_search image 调用失败: " + err.Error()
		} else {
			result.ImageSearchSummary = out
		}
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	return string(out), nil
}

func intArgDefault(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i)
		}
	case string:
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i
		}
	}
	return def
}

func boolArgDefault(args map[string]any, key string, def bool) bool {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		s := strings.TrimSpace(strings.ToLower(b))
		if s == "true" || s == "1" || s == "yes" || s == "y" {
			return true
		}
		if s == "false" || s == "0" || s == "no" || s == "n" {
			return false
		}
	}
	return def
}
