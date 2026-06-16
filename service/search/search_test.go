package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestQueryBuildsPythonCompatibleSearchPrompt(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s":
			if got := r.URL.Query().Get("wd"); got != "小智新闻" {
				t.Fatalf("wd = %q", got)
			}
			if got := r.URL.Query().Get("rn"); got != "2" {
				t.Fatalf("rn = %q", got)
			}
			_, _ = fmt.Fprintf(w, `<html><body>
<div class="result"><h3><a href="%s/page1">标题一</a></h3><div class="c-abstract">摘要一</div></div>
<div class="result"><h3><a href="%s/page2">标题二</a></h3><div class="c-abstract">摘要二</div></div>
</body></html>`, serverURL, serverURL)
		case "/page1":
			_, _ = w.Write([]byte(`<html><body><article><p>正文一</p><script>alert(1)</script></article></body></html>`))
		case "/page2":
			_, _ = w.Write([]byte(`<html><body><main><p>正文二</p></main></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	client := New(config.BaiduSearchConf{
		Enabled:          true,
		SearchURL:        server.URL + "/s",
		MaxResults:       10,
		MaxExtractPages:  3,
		MaxContentLength: 1000,
	})
	client.HTTPClient = server.Client()

	text, err := client.Query(context.Background(), "小智新闻", "zh_CN", 2, true, 2)
	if err != nil {
		t.Fatalf("query search: %v", err)
	}
	for _, want := range []string{
		"根据下列数据，用zh_CN回应用户的搜索查询请求",
		"百度搜索结果 - 关键词: 小智新闻",
		"1. 标题一",
		"   链接: " + server.URL + "/page1",
		"   摘要: 摘要一",
		"   内容: 正文一",
		"2. 标题二",
		"   内容: 正文二",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("search prompt missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "alert(1)") {
		t.Fatalf("search prompt should strip scripts: %s", text)
	}
}

func TestQueryDisabledMatchesPythonUnavailable(t *testing.T) {
	client := New(config.BaiduSearchConf{Enabled: false})
	text, err := client.Query(context.Background(), "小智", "zh_CN", 5, true, 3)
	if err != nil {
		t.Fatalf("query disabled: %v", err)
	}
	if text != "抱歉，百度搜索功能当前不可用。" {
		t.Fatalf("unexpected disabled response: %q", text)
	}
}

func TestQueryNoResultsMatchesPythonResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>empty</body></html>`))
	}))
	defer server.Close()

	client := New(config.BaiduSearchConf{Enabled: true, SearchURL: server.URL})
	client.HTTPClient = server.Client()

	text, err := client.Query(context.Background(), "不存在", "zh_CN", 5, false, 0)
	if err != nil {
		t.Fatalf("query no results: %v", err)
	}
	if text != "抱歉，没有找到关于'不存在'的搜索结果。请尝试使用不同的关键词。" {
		t.Fatalf("unexpected no results response: %q", text)
	}
}
