package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/alanjchuang/goagent/internal/config"
	"github.com/volcengine/volcengine-go-sdk/service/arkruntime"
	"github.com/volcengine/volcengine-go-sdk/service/arkruntime/model"
	"github.com/volcengine/volcengine-go-sdk/volcengine"
)

const defaultArkImageModel = "doubao-seedream-4-0-250828"

// ArkGenerateImages 调用火山方舟文生图接口，返回图片 URL。
type ArkGenerateImages struct{}

func (ArkGenerateImages) Name() string { return "ark_generate_images" }
func (ArkGenerateImages) Description() string {
	return "调用火山方舟图片生成 API 生成图片，返回图片 URL、尺寸、用量和错误信息。适合公众号封面、正文插图、金句图等。"
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
		},
		"required": []string{"prompt"},
	}
}

func (ArkGenerateImages) Execute(args map[string]any) (string, error) {
	prompt := strings.TrimSpace(strArg(args, "prompt"))
	if prompt == "" {
		return "", fmt.Errorf("缺少参数 prompt")
	}

	apiKey := strings.TrimSpace(os.Getenv("ARK_IMAGE_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("ARK_API_KEY"))
	}
	if apiKey == "" && config.C != nil {
		if mc, err := config.C.LLM.ForType(""); err == nil {
			apiKey = strings.TrimSpace(mc.APIKey)
		}
	}
	if apiKey == "" {
		return "", fmt.Errorf("缺少火山方舟 API Key：请设置 ARK_IMAGE_API_KEY 或 ARK_API_KEY，或在 config/llm.yaml 中配置默认模型 api_key")
	}

	imageModel := strings.TrimSpace(strArg(args, "model"))
	if imageModel == "" {
		imageModel = strings.TrimSpace(os.Getenv("ARK_IMAGE_MODEL"))
	}
	if imageModel == "" {
		imageModel = defaultArkImageModel
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
		Index int64  `json:"index"`
		Size  string `json:"size"`
		URL   string `json:"url"`
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
				result.Images = append(result.Images, imageResult{Index: recv.ImageIndex, Size: recv.Size, URL: *recv.Url})
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

// LicensedImageSearchStrategy 生成授权图库搜索策略，并可用 web_search 辅助检索来源线索。
type LicensedImageSearchStrategy struct{}

func (LicensedImageSearchStrategy) Name() string { return "licensed_image_search_strategy" }
func (LicensedImageSearchStrategy) Description() string {
	return "为公众号配图生成网上找授权图策略：推荐图库、搜索关键词、版权记录模板和风险提示；可选调用 web_search 获取候选来源线索。"
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
			"run_web_search": map[string]any{
				"type":        "boolean",
				"description": "是否调用 web_search 获取候选来源线索，默认 false。",
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
		Topic             string   `json:"topic"`
		Usage             string   `json:"usage"`
		Style             string   `json:"style"`
		PreferredSources  []string `json:"preferred_sources"`
		SearchQueries     []string `json:"search_queries"`
		RejectSources     []string `json:"reject_sources"`
		LicenseChecklist  []string `json:"license_checklist"`
		AttributionRecord []string `json:"attribution_record_fields"`
		RiskNotes         []string `json:"risk_notes"`
		WebSearchSummary  string   `json:"web_search_summary,omitempty"`
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

	if boolArgDefault(args, "run_web_search", false) {
		searchQuery := strings.Join(queries, " OR ")
		out, err := (WebSearch{}).Execute(map[string]any{"query": searchQuery, "max_keyword": float64(5)})
		if err != nil {
			result.WebSearchSummary = "web_search 调用失败: " + err.Error()
		} else {
			result.WebSearchSummary = out
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
