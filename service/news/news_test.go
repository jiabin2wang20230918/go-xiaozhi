package news

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestQueryFetchesRSSAndBuildsPythonPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<rss><channel><item>
<title>测试新闻</title>
<link>https://example.com/news</link>
<description>新闻摘要</description>
<pubDate>Mon, 15 Jun 2026 08:00:00 GMT</pubDate>
</item></channel></rss>`))
	}))
	defer server.Close()

	client := New(config.GetNewsConf{
		DefaultRSSURL: server.URL,
		CategoryURLs:  map[string]string{"finance": server.URL},
	})
	client.rand = nil

	text, err := client.Query(context.Background(), "财经", false, "zh_CN")
	if err != nil {
		t.Fatalf("query news: %v", err)
	}
	for _, want := range []string{
		"根据下列数据，用zh_CN回应用户的新闻查询请求",
		"新闻标题: 测试新闻",
		"发布时间: Mon, 15 Jun 2026 08:00:00 GMT",
		"新闻内容: 新闻摘要",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("news prompt missing %q: %s", want, text)
		}
	}
}

func TestQueryDetailRequiresPreviousNews(t *testing.T) {
	client := New(config.GetNewsConf{})
	text, err := client.Query(context.Background(), "", true, "zh_CN")
	if err != nil {
		t.Fatalf("query detail: %v", err)
	}
	if text != "抱歉，没有找到最近查询的新闻，请先获取一条新闻。" {
		t.Fatalf("unexpected detail response: %q", text)
	}
}

func TestQueryDetailFetchesLastNewsPage(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rss":
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<rss><channel><item>
<title>详情新闻</title>
<link>` + serverURL + `/detail</link>
<description>新闻摘要</description>
<pubDate>Mon, 15 Jun 2026 08:00:00 GMT</pubDate>
</item></channel></rss>`))
		case "/detail":
			_, _ = w.Write([]byte(`<html><head><style>.x{}</style><script>alert(1)</script></head><body><h1>详情新闻</h1><p>第一段详情&nbsp;内容。</p><p>第二段详情内容。</p></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	client := New(config.GetNewsConf{DefaultRSSURL: server.URL + "/rss"})
	client.HTTPClient = server.Client()
	client.rand = nil

	if _, err := client.Query(context.Background(), "", false, "zh_CN"); err != nil {
		t.Fatalf("query news: %v", err)
	}
	text, err := client.Query(context.Background(), "", true, "zh_CN")
	if err != nil {
		t.Fatalf("query detail: %v", err)
	}
	for _, want := range []string{
		"根据下列数据，用zh_CN回应用户的新闻详情查询请求",
		"新闻标题: 详情新闻",
		"第一段详情",
		"第二段详情内容。",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("detail prompt missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "alert(1)") {
		t.Fatalf("detail prompt should strip scripts: %s", text)
	}
}
