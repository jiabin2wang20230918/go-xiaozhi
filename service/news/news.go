package news

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

const maxNewsDetailBytes = 4 << 20

var (
	scriptStyleRE = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</\s*(script|style|noscript)\s*>`)
	tagRE         = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRE       = regexp.MustCompile(`[ \t\r\f\v]+`)
	blankLineRE   = regexp.MustCompile(`\n{3,}`)
)

type Client struct {
	Config     config.GetNewsConf
	HTTPClient *http.Client
	rand       *rand.Rand
	last       *Item
}

type Item struct {
	Title       string
	Link        string
	Description string
	PubDate     string
}

func New(conf config.GetNewsConf) *Client {
	conf.Normalize()
	return &Client{
		Config: conf,
		rand:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (c *Client) Query(ctx context.Context, category string, detail bool, lang string) (string, error) {
	if strings.TrimSpace(lang) == "" {
		lang = "zh_CN"
	}
	if detail {
		if c.last == nil || strings.TrimSpace(c.last.Link) == "" || c.last.Link == "#" {
			return "抱歉，没有找到最近查询的新闻，请先获取一条新闻。", nil
		}
		content, err := c.fetchDetail(ctx, c.last.Link)
		if err != nil || strings.TrimSpace(content) == "" {
			return "抱歉，获取新闻详情时发生错误，请稍后再试。", nil
		}
		return fmt.Sprintf(
			"根据下列数据，用%s回应用户的新闻详情查询请求：\n\n新闻标题: %s\n新闻内容: %s\n\n(请以自然、流畅的方式详细介绍这条新闻，直接播报即可，不需要额外多余的内容。)",
			lang, c.last.Title, content,
		), nil
	}

	items, err := c.fetchRSS(ctx, c.rssURL(category))
	if err != nil {
		return "抱歉，获取新闻时发生错误，请稍后再试。", nil
	}
	if len(items) == 0 {
		return "抱歉，未能获取到新闻信息，请稍后再试。", nil
	}
	selected := items[0]
	if c.rand != nil && len(items) > 1 {
		selected = items[c.rand.Intn(len(items))]
	}
	c.last = &selected
	return fmt.Sprintf(
		"根据下列数据，用%s回应用户的新闻查询请求：\n\n新闻标题: %s\n发布时间: %s\n新闻内容: %s\n(请以自然、流畅的方式向用户播报这条新闻，可以适当总结内容，直接读出新闻即可，不需要额外多余的内容。如果用户询问更多详情，告知用户可以说'请详细介绍这条新闻'获取更多内容)",
		lang, selected.Title, selected.PubDate, selected.Description,
	), nil
}

func (c *Client) rssURL(category string) string {
	conf := c.Config
	conf.Normalize()
	key := mapCategory(category)
	if key != "" && conf.CategoryURLs != nil {
		if url := strings.TrimSpace(conf.CategoryURLs[key]); url != "" {
			return url
		}
	}
	return conf.DefaultRSSURL
}

func mapCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "社会", "社会新闻":
		return "society"
	case "国际", "国际新闻":
		return "world"
	case "财经", "财经新闻", "金融", "经济":
		return "finance"
	default:
		return strings.TrimSpace(category)
	}
}

func (c *Client) fetchRSS(ctx context.Context, url string) ([]Item, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rss request failed: status=%d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return parseRSS(data)
}

func (c *Client) fetchDetail(ctx context.Context, url string) (string, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("detail request failed: status=%d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNewsDetailBytes))
	if err != nil {
		return "", err
	}
	return extractHTMLText(string(data)), nil
}

func parseRSS(data []byte) ([]Item, error) {
	var feed struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(data, &feed); err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(feed.Items))
	for _, item := range feed.Items {
		items = append(items, Item{
			Title:       fallback(item.Title, "无标题"),
			Link:        fallback(item.Link, "#"),
			Description: fallback(item.Description, "无描述"),
			PubDate:     fallback(item.PubDate, "未知时间"),
		})
	}
	return items, nil
}

func extractHTMLText(data string) string {
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
	text := strings.Join(cleaned, "\n")
	text = blankLineRE.ReplaceAllString(text, "\n\n")
	if len([]rune(text)) > 4000 {
		runes := []rune(text)
		text = string(runes[:4000])
	}
	return text
}

func fallback(value string, fallbackValue string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallbackValue
	}
	return value
}
