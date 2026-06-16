package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatLLMRespondStreamsContent(t *testing.T) {
	var gotAuth string
	var gotPath string
	var gotRequest openAIChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	llm := &OpenAIChatLLM{
		BaseURL: server.URL,
		APIKey:  "secret",
		Model:   "test-model",
	}
	stream, err := llm.Respond(context.Background(), "s1", []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	var chunks []string
	for chunk := range stream {
		chunks = append(chunks, chunk)
	}
	if strings.Join(chunks, "") != "你好" {
		t.Fatalf("unexpected chunks: %v", chunks)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("unexpected auth: %q", gotAuth)
	}
	if !gotRequest.Stream || gotRequest.Model != "test-model" {
		t.Fatalf("unexpected request: %+v", gotRequest)
	}
	if len(gotRequest.Messages) != 2 || gotRequest.Messages[1].Content != "hi" {
		t.Fatalf("unexpected messages: %+v", gotRequest.Messages)
	}
}

func TestChatCompletionsURLKeepsPythonCompatibleBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "v1 base",
			in:   "https://api.example.com/v1",
			want: "https://api.example.com/v1/chat/completions",
		},
		{
			name: "v1 base trailing slash",
			in:   "https://api.example.com/v1/",
			want: "https://api.example.com/v1/chat/completions",
		},
		{
			name: "complete endpoint",
			in:   "https://api.example.com/v1/chat/completions",
			want: "https://api.example.com/v1/chat/completions",
		},
		{
			name: "python legacy url without v1",
			in:   "https://api.deepseek.com",
			want: "https://api.deepseek.com/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chatCompletionsURL(tt.in); got != tt.want {
				t.Fatalf("chatCompletionsURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestReadOpenAIStreamIgnoresDoneAndEmptyChoices(t *testing.T) {
	input := strings.NewReader(": ping\n\ndata: {\"choices\":[]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\ndata: [DONE]\n\n")
	var chunks []string
	err := readOpenAIStream(input, func(event LLMEvent) error {
		chunks = append(chunks, event.Content)
		return nil
	})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "A" {
		t.Fatalf("unexpected chunks: %v", chunks)
	}
}

func TestReadOpenAIStreamHandlesLongDataLine(t *testing.T) {
	content := strings.Repeat("长", 70*1024)
	payload, err := json.Marshal(openAIChatChunk{
		Choices: []struct {
			Delta struct {
				Content   string                  `json:"content"`
				ToolCalls []openAIMessageToolCall `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Delta: struct {
					Content   string                  `json:"content"`
					ToolCalls []openAIMessageToolCall `json:"tool_calls"`
				}{Content: content},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	input := strings.NewReader("data: " + string(payload) + "\n\ndata: [DONE]\n\n")

	var got string
	err = readOpenAIStream(input, func(event LLMEvent) error {
		got += event.Content
		return nil
	})
	if err != nil {
		t.Fatalf("read long stream: %v", err)
	}
	if got != content {
		t.Fatalf("long content length got %d want %d", len([]rune(got)), len([]rune(content)))
	}
}

func TestReadOpenAIStreamFiltersThinkTagsLikePython(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"回答前"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"<think>隐藏"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"推理</think>回答后"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n"))
	var got string
	err := readOpenAIStream(input, func(event LLMEvent) error {
		got += event.Content
		return nil
	})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if got != "回答前回答后" {
		t.Fatalf("filtered content got %q", got)
	}
}

func TestReadOpenAIStreamFiltersThinkTagsAcrossChunks(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"可见<thi"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"nk>隐藏"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"</thi"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"nk>继续"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n"))
	var got string
	err := readOpenAIStream(input, func(event LLMEvent) error {
		got += event.Content
		return nil
	})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if got != "可见继续" {
		t.Fatalf("filtered cross-chunk content got %q", got)
	}
}

func TestOpenAIChatLLMRespondWithToolsSendsToolSchemaAndParsesToolCall(t *testing.T) {
	var gotRequest openAIChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lamp_setpower\",\"arguments\":\"{\\\"value\\\":\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"false}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	llm := &OpenAIChatLLM{BaseURL: server.URL, Model: "test-model"}
	stream, err := llm.RespondWithTools(context.Background(), "s1", []Message{{Role: "user", Content: "关灯"}}, []Tool{{
		Name:        "lamp_setpower",
		Description: "台灯 - 设置开关",
		Parameters: ToolParameters{
			Type: "object",
			Properties: map[string]ToolProperty{
				"value": {Type: "boolean", Description: "目标状态"},
			},
			Required: []string{"value"},
		},
	}})
	if err != nil {
		t.Fatalf("respond with tools: %v", err)
	}
	var events []LLMEvent
	for event := range stream {
		events = append(events, event)
	}
	if len(gotRequest.Tools) != 1 || gotRequest.Tools[0].Function.Name != "lamp_setpower" {
		t.Fatalf("tool schema not sent: %+v", gotRequest.Tools)
	}
	if len(events) != 1 || events[0].ToolCall == nil {
		t.Fatalf("expected tool call event, got %+v", events)
	}
	call := events[0].ToolCall
	if call.ID != "call_1" || call.Name != "lamp_setpower" || call.Arguments != "{\"value\":false}" {
		t.Fatalf("unexpected tool call: %+v", call)
	}
}
