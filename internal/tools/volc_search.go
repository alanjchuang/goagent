package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alanjchuang/goagent/internal/config"
)

const (
	defaultVolcSearchModelType = "search"
	defaultVolcSearchAPIType   = "web"
	defaultVolcSearchCount     = 10
	defaultVolcSearchAPIURL    = "https://open.feedcoopapi.com/search_api/web_search"
)

// VolcWebSearch 使用独立搜索 API 做联网搜索，避免与方舟内置 web_search 工具冲突。
type VolcWebSearch struct{}

func (VolcWebSearch) Name() string { return "volc_web_search" }
func (VolcWebSearch) Description() string {
	return "使用独立搜索 API 联网检索并返回带来源链接的摘要；支持 web/image 搜索，image 搜索可把候选图片下载到 outputs/。用于获取实时信息、资料来源和授权图线索。"
}
func (VolcWebSearch) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "搜索关键词或问题。",
			},
			"queries": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "批量搜索关键词；提供时优先于 query。",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "每个 query 返回结果数，默认 10，最大 20。",
			},
			"time_range": map[string]any{
				"type":        "string",
				"description": "可选时间范围，透传给搜索 API。",
			},
			"search_type": map[string]any{
				"type":        "string",
				"description": "搜索类型：web 或 image。默认 web。",
			},
			"download_images": map[string]any{
				"type":        "boolean",
				"description": "search_type=image 时是否下载图片到本地 outputs/，默认 true。",
			},
			"output_dir": map[string]any{
				"type":        "string",
				"description": "图片下载目录，默认 outputs/images/search。",
			},
		},
	}
}

func (VolcWebSearch) Execute(args map[string]any) (string, error) {
	queries := stringSliceArg(args, "queries")
	if len(queries) == 0 {
		if q := strings.TrimSpace(strArg(args, "query")); q != "" {
			queries = []string{q}
		}
	}
	if len(queries) == 0 {
		return "", fmt.Errorf("缺少参数 query 或 queries")
	}

	cfg, hasSearchConfig := volcSearchConfig()
	apiKey := firstNonEmpty(
		os.Getenv("VOLC_SEARCH_API_KEY"),
		os.Getenv("SEARCH_API_KEY"),
		configValue(hasSearchConfig, cfg.APIKey),
	)
	if apiKey == "" {
		return "", fmt.Errorf("缺少搜索 API Key：请在 config/llm.yaml 的 model.search.api_key 中配置，或设置 VOLC_SEARCH_API_KEY / SEARCH_API_KEY")
	}
	apiURL := firstNonEmpty(os.Getenv("VOLC_SEARCH_API_URL"), configValue(hasSearchConfig, cfg.BaseURL), defaultVolcSearchAPIURL)
	count := intArgDefault(args, "count", defaultVolcSearchCount)
	if count <= 0 {
		count = defaultVolcSearchCount
	}
	if count > 20 {
		count = 20
	}
	timeRange := strings.TrimSpace(strArg(args, "time_range"))
	searchType := strings.TrimSpace(strings.ToLower(strArg(args, "search_type")))
	if searchType == "" {
		searchType = defaultVolcSearchAPIType
	}
	if searchType != "web" && searchType != "image" && searchType != "web_summary" {
		return "", fmt.Errorf("不支持的 search_type=%q，仅支持 web/image/web_summary", searchType)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	responses, err := runVolcSearchQueries(ctx, apiURL, apiKey, queries, count, timeRange, searchType)
	if err != nil {
		return "", err
	}
	if searchType == "image" && boolArgDefault(args, "download_images", true) {
		outputDir := strings.TrimSpace(strArg(args, "output_dir"))
		if outputDir == "" {
			outputDir = "outputs/images/search"
		}
		downloadVolcSearchImages(ctx, responses, outputDir)
	}
	return formatVolcSearchResults(responses, searchType)
}

func volcSearchConfig() (config.ModelConfig, bool) {
	if config.C == nil {
		return config.ModelConfig{}, false
	}
	modelType := firstNonEmpty(os.Getenv("VOLC_SEARCH_MODEL_TYPE"), defaultVolcSearchModelType)
	mc, err := config.C.LLM.ForType(modelType)
	if err != nil {
		return config.ModelConfig{}, false
	}
	return mc, true
}

func runVolcSearchQueries(ctx context.Context, apiURL, apiKey string, queries []string, count int, timeRange string, searchType string) ([]*SearchAPIResponse, error) {
	if len(queries) == 1 {
		resp, err := searchAPIKeyClient(ctx, apiURL, apiKey, queries[0], count, timeRange, searchType)
		if err != nil {
			return nil, err
		}
		return []*SearchAPIResponse{resp}, nil
	}

	results := make([]*SearchAPIResponse, len(queries))
	errs := make([]error, len(queries))
	wg := sync.WaitGroup{}
	for i, q := range queries {
		i, q := i, q
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := searchAPIKeyClient(ctx, apiURL, apiKey, q, count, timeRange, searchType)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = resp
		}()
	}
	wg.Wait()

	success := make([]*SearchAPIResponse, 0, len(results))
	for _, r := range results {
		if r != nil {
			success = append(success, r)
		}
	}
	if len(success) == 0 {
		return nil, fmt.Errorf("all search queries failed: %v", errs)
	}
	return success, nil
}

type SearchAPIRequest struct {
	Query       string          `json:"Query"`
	SearchType  string          `json:"SearchType"`
	Count       int             `json:"Count"`
	Filter      SearchAPIFilter `json:"Filter"`
	NeedSummary bool            `json:"NeedSummary"`
	TimeRange   string          `json:"TimeRange,omitempty"`
}

type SearchAPIFilter struct {
	NeedContent bool   `json:"NeedContent"`
	NeedURL     bool   `json:"NeedUrl"`
	Sites       string `json:"Sites,omitempty"`
}

type SearchAPIResponse struct {
	ResponseMetadata SearchAPIResponseMetadata `json:"ResponseMetadata"`
	Result           SearchAPIResult           `json:"Result"`
}

type SearchAPIResponseMetadata struct {
	RequestID string `json:"RequestId"`
	Action    string `json:"Action"`
	Version   string `json:"Version"`
	Service   string `json:"Service"`
	Region    string `json:"Region"`
}

type SearchAPIResult struct {
	ResultCount  int64                `json:"ResultCount"`
	WebResults   []SearchAPIWebItem   `json:"WebResults"`
	ImageResults []SearchAPIImageItem `json:"ImageResults"`
	Usage        map[string]any       `json:"Usage"`
	Context      map[string]any       `json:"SearchContext"`
	TimeCost     int                  `json:"TimeCost"`
	LogID        string               `json:"LogID"`
	Rag          *string              `json:"Rag"`
}

type SearchAPIWebItem struct {
	ID          string  `json:"Id"`
	SortID      int     `json:"SortId"`
	Title       string  `json:"Title"`
	SiteName    string  `json:"SiteName"`
	URL         string  `json:"Url"`
	Snippet     string  `json:"Snippet"`
	Summary     string  `json:"Summary"`
	Content     string  `json:"Content"`
	PublishTime string  `json:"PublishTime"`
	LogoURL     string  `json:"LogoUrl"`
	RankScore   float64 `json:"RankScore"`
}

type SearchAPIImageItem struct {
	ID          string         `json:"Id"`
	SortID      int            `json:"SortId"`
	Title       string         `json:"Title"`
	SiteName    string         `json:"SiteName"`
	URL         string         `json:"Url"`
	Snippet     string         `json:"Snippet"`
	PublishTime string         `json:"PublishTime"`
	Image       SearchAPIImage `json:"Image"`
	LocalPath   string         `json:"-"`
	DownloadErr string         `json:"-"`
}

type SearchAPIImage struct {
	URL    string `json:"Url"`
	Width  int    `json:"Width"`
	Height int    `json:"Height"`
	Shape  string `json:"Shape"`
}

func searchAPIKeyClient(ctx context.Context, apiURL, apiKey, query string, count int, timeRange string, searchType string) (*SearchAPIResponse, error) {
	if apiURL == "" || apiKey == "" || query == "" {
		return nil, fmt.Errorf("invalid search request params")
	}
	if searchType == "" {
		searchType = defaultVolcSearchAPIType
	}
	request := SearchAPIRequest{
		Query:      query,
		SearchType: searchType,
		Count:      count,
		Filter: SearchAPIFilter{
			NeedContent: false,
			NeedURL:     true,
		},
		NeedSummary: searchType != "image",
		TimeRange:   timeRange,
	}
	reqBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal request failed: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("search api status=%d, body=%s", resp.StatusCode, string(body))
	}
	var result SearchAPIResponse
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response failed: %w", err)
	}
	return &result, nil
}

func formatVolcSearchResults(responses []*SearchAPIResponse, searchType string) (string, error) {
	var b strings.Builder
	b.WriteString(searchTimeNow())

	idx := 0
	for _, resp := range responses {
		if resp == nil {
			continue
		}
		for _, item := range resp.Result.WebResults {
			text := firstNonEmpty(item.Summary, item.Snippet, item.Content)
			if text == "" {
				continue
			}
			idx++
			b.WriteString(fmt.Sprintf("摘要superscript:%d:\n", idx))
			b.WriteString(fmt.Sprintf("本文标题：%s\n本文内容：%s\n本文链接：%s\n网页发布时间：%s\n",
				item.Title, text, item.URL, item.PublishTime))
		}
		for _, item := range resp.Result.ImageResults {
			imageURL := firstNonEmpty(item.Image.URL, item.URL)
			if imageURL == "" {
				continue
			}
			idx++
			b.WriteString(fmt.Sprintf("图片结果superscript:%d:\n", idx))
			b.WriteString(fmt.Sprintf("图片标题：%s\n来源站点：%s\n来源页面：%s\n图片链接：%s\n尺寸：%dx%d\n形状：%s\n网页发布时间：%s\n",
				item.Title, item.SiteName, item.URL, imageURL, item.Image.Width, item.Image.Height, item.Image.Shape, item.PublishTime))
			if item.Snippet != "" {
				b.WriteString(fmt.Sprintf("摘要：%s\n", item.Snippet))
			}
			if item.LocalPath != "" {
				b.WriteString(fmt.Sprintf("本地文件：%s\n", item.LocalPath))
			}
			if item.DownloadErr != "" {
				b.WriteString(fmt.Sprintf("下载错误：%s\n", item.DownloadErr))
			}
		}
	}
	if idx == 0 {
		return "", fmt.Errorf("empty %s search content", searchType)
	}
	return b.String(), nil
}

func downloadVolcSearchImages(ctx context.Context, responses []*SearchAPIResponse, outputDir string) {
	idx := 0
	for _, resp := range responses {
		if resp == nil {
			continue
		}
		for i := range resp.Result.ImageResults {
			imageURL := firstNonEmpty(resp.Result.ImageResults[i].Image.URL, resp.Result.ImageResults[i].URL)
			if imageURL == "" {
				continue
			}
			idx++
			localPath, err := downloadImageToOutput(ctx, imageURL, outputDir, fmt.Sprintf("search_%02d", idx))
			if err != nil {
				resp.Result.ImageResults[i].DownloadErr = err.Error()
				continue
			}
			resp.Result.ImageResults[i].LocalPath = localPath
		}
	}
}

func searchTimeNow() string {
	now := time.Now()
	week := []string{"日", "一", "二", "三", "四", "五", "六"}[now.Weekday()]
	return fmt.Sprintf("搜索时刻：%s星期%s\n", now.Format("2006年1月2日15时4分"), week)
}

func stringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return compactStrings(x)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := strings.TrimSpace(fmt.Sprintf("%v", item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{strings.TrimSpace(x)}
	default:
		return nil
	}
}

func compactStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
