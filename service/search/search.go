package search

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"

var (
	resultStartRE = regexp.MustCompile(`(?is)<div[^>]+class=["'][^"']*(?:result|c-container)[^"']*["'][^>]*>`)
	titleLinkRE   = regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	abstractRE    = regexp.MustCompile(`(?is)<(?:span|div)[^>]+class=["'][^"']*(?:content-right|c-abstract|c-span-last|abstract)[^"']*["'][^>]*>(.*?)</(?:span|div)>`)
	metaRE        = regexp.MustCompile(`(?is)<meta[^>]+name=["']description["'][^>]+content=["']([^"']*)["'][^>]*>`)
	scriptStyleRE = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</\s*(script|style|noscript)\s*>`)
	tagRE         = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRE       = regexp.MustCompile(`[ \t\r\f\v]+`)
)

type Client struct {
	Config     config.BaiduSearchConf
	HTTPClient *http.Client
}

type Result struct {
	Title       string
	Link        string
	Description string
	Content     string
}

func New(conf config.BaiduSearchConf) *Client {
	conf.Normalize()
	return &Client{Config: conf}
}

func (c *Client) Query(ctx context.Context, query string, lang string, numResults int, extractContent bool, maxExtractPages int) (string, error) {
	conf := c.Config
	conf.Normalize()
	if !conf.Enabled {
		return "抱歉，百度搜索功能当前不可用。", nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "抱歉，没有找到关于''的搜索结果。请尝试使用不同的关键词。", nil
	}
	if strings.TrimSpace(lang) == "" {
		lang = "zh_CN"
	}
	numResults = clamp(numResults, 1, minInt(conf.MaxResults, 10), 5)
	maxExtractPages = clamp(maxExtractPages, 1, minInt(conf.MaxExtractPages, 5), conf.MaxExtractPages)
	results, err := c.fetchResults(ctx, conf, query, numResults)
	if err != nil {
		return fmt.Sprintf("抱歉，执行百度搜索时发生错误: %s。请稍后再试。", err), nil
	}
	if len(results) == 0 {
		return fmt.Sprintf("抱歉，没有找到关于'%s'的搜索结果。请尝试使用不同的关键词。", query), nil
	}
	if extractContent {
		extracted := 0
		for i := range results {
			if extracted >= maxExtractPages {
				break
			}
			if results[i].Link == "" || results[i].Link == "#" {
				continue
			}
			results[i].Content = c.extractContent(ctx, results[i].Link, conf.MaxContentLength)
			extracted++
		}
	}
	return buildReport(lang, query, results, extractContent), nil
}

func (c *Client) fetchResults(ctx context.Context, conf config.BaiduSearchConf, query string, numResults int) ([]Result, error) {
	requestURL, err := url.Parse(conf.SearchURL)
	if err != nil {
		return nil, err
	}
	values := requestURL.Query()
	values.Set("wd", query)
	values.Set("rn", strconv.Itoa(numResults))
	requestURL.RawQuery = values.Encode()
	data, err := c.get(ctx, requestURL.String())
	if err != nil {
		return nil, err
	}
	return parseResults(string(data), numResults), nil
}

func parseResults(data string, limit int) []Result {
	starts := resultStartRE.FindAllStringIndex(data, -1)
	results := make([]Result, 0, limit)
	seen := map[string]struct{}{}
	for i, start := range starts {
		end := len(data)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		block := data[start[0]:end]
		linkMatch := titleLinkRE.FindStringSubmatch(block)
		if len(linkMatch) < 3 {
			continue
		}
		link := html.UnescapeString(strings.TrimSpace(linkMatch[1]))
		title := cleanHTML(linkMatch[2])
		if title == "" {
			title = "无标题"
		}
		if _, ok := seen[link]; ok {
			continue
		}
		seen[link] = struct{}{}
		description := "无描述"
		if abstractMatch := abstractRE.FindStringSubmatch(block); len(abstractMatch) >= 2 {
			description = fallback(cleanHTML(abstractMatch[1]), "无描述")
		}
		results = append(results, Result{
			Title:       title,
			Link:        fallback(link, "#"),
			Description: description,
		})
		if len(results) >= limit {
			break
		}
	}
	return results
}

func (c *Client) extractContent(ctx context.Context, requestURL string, maxLength int) string {
	data, err := c.get(ctx, requestURL)
	if err != nil {
		return "提取网页内容失败: " + err.Error()
	}
	content := extractMainText(string(data))
	if content == "" {
		return "无法提取网页内容"
	}
	runes := []rune(content)
	if maxLength > 0 && len(runes) > maxLength {
		return string(runes[:maxLength]) + "... (内容已截取)"
	}
	return content
}

func extractMainText(data string) string {
	for _, pattern := range []string{
		`(?is)<article[^>]*>(.*?)</article>`,
		`(?is)<main[^>]*>(.*?)</main>`,
		`(?is)<div[^>]+class=["'][^"']*(?:article-content|post-content|entry-content|main-content|content)[^"']*["'][^>]*>(.*?)</div>`,
		`(?is)<body[^>]*>(.*?)</body>`,
	} {
		re := regexp.MustCompile(pattern)
		if match := re.FindStringSubmatch(data); len(match) >= 2 {
			if text := cleanHTML(match[1]); text != "" {
				return text
			}
		}
	}
	if match := metaRE.FindStringSubmatch(data); len(match) >= 2 {
		return fallback(cleanHTML(match[1]), "")
	}
	return cleanHTML(data)
}

func (c *Client) get(ctx context.Context, requestURL string) ([]byte, error) {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status=%d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func buildReport(lang string, query string, results []Result, extractContent bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "根据下列数据，用%s回应用户的搜索查询请求：\n\n", lang)
	fmt.Fprintf(&b, "百度搜索结果 - 关键词: %s\n\n", query)
	for i, result := range results {
		fmt.Fprintf(&b, "%d. %s\n", i+1, fallback(result.Title, "无标题"))
		fmt.Fprintf(&b, "   链接: %s\n", fallback(result.Link, "#"))
		fmt.Fprintf(&b, "   摘要: %s\n", fallback(result.Description, "无描述"))
		if extractContent && strings.TrimSpace(result.Content) != "" {
			fmt.Fprintf(&b, "   内容: %s\n", result.Content)
		}
		b.WriteString("\n")
	}
	b.WriteString("(请以自然、流畅的方式向用户播报这些搜索结果，可以适当总结内容，直接读出最重要的信息即可，不需要额外多余的内容。如果用户需要了解某个具体结果的详细信息，可以告知用户点击对应链接查看详情)")
	return b.String()
}

func cleanHTML(data string) string {
	data = scriptStyleRE.ReplaceAllString(data, "")
	for _, marker := range []string{"</p>", "</div>", "</br>", "<br>", "<br/>", "<br />", "</h1>", "</h2>", "</h3>", "</li>"} {
		data = strings.ReplaceAll(data, marker, marker+"\n")
	}
	data = tagRE.ReplaceAllString(data, "\n")
	data = html.UnescapeString(data)
	lines := strings.Split(data, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = spaceRE.ReplaceAllString(strings.TrimSpace(line), " ")
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}

func clamp(value int, minValue int, maxValue int, defaultValue int) int {
	if value == 0 {
		value = defaultValue
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func fallback(value string, fallbackValue string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallbackValue
	}
	return value
}
