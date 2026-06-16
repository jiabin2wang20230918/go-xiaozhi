package voice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

type OpenAIChatLLM struct {
	BaseURL    string
	APIKey     string
	Model      string
	MaxTokens  int
	HTTPClient *http.Client
}

func NewLLM(conf config.LLMConf) LLM {
	switch conf.Type {
	case "openai":
		return &OpenAIChatLLM{
			BaseURL:   conf.BaseURL,
			APIKey:    conf.APIKey,
			Model:     conf.Model,
			MaxTokens: conf.MaxTokens,
		}
	default:
		return EchoLLM{WelcomeMessage: conf.Echo.WelcomeMessage, EchoTranscripts: conf.Echo.EchoTranscripts}
	}
}

func (l *OpenAIChatLLM) Respond(ctx context.Context, sessionID string, history []Message) (<-chan string, error) {
	events, err := l.respond(ctx, sessionID, history, nil)
	if err != nil {
		return nil, err
	}
	out := make(chan string)
	go func() {
		defer close(out)
		for event := range events {
			if event.Content == "" {
				continue
			}
			select {
			case out <- event.Content:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (l *OpenAIChatLLM) RespondWithTools(ctx context.Context, sessionID string, history []Message, tools []Tool) (<-chan LLMEvent, error) {
	return l.respond(ctx, sessionID, history, tools)
}

func (l *OpenAIChatLLM) respond(ctx context.Context, sessionID string, history []Message, tools []Tool) (<-chan LLMEvent, error) {
	if strings.TrimSpace(l.BaseURL) == "" {
		return nil, fmt.Errorf("llm base_url is required")
	}
	if strings.TrimSpace(l.Model) == "" {
		return nil, fmt.Errorf("llm model is required")
	}

	body, err := json.Marshal(openAIChatRequest{
		Model:     l.Model,
		Messages:  toOpenAIMessages(history),
		Stream:    true,
		MaxTokens: l.MaxTokens,
		Tools:     toOpenAITools(tools),
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL(l.BaseURL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if strings.TrimSpace(l.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(l.APIKey))
	}

	client := l.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("llm request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	out := make(chan LLMEvent)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		_ = readOpenAIStream(resp.Body, func(event LLMEvent) error {
			if event.Content == "" && event.ToolCall == nil {
				return nil
			}
			select {
			case out <- event:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return out, nil
}

func readOpenAIStream(r io.Reader, emit func(LLMEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	toolCalls := map[int]*ToolCall{}
	thinkFilter := newThinkTagFilter()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}
		var event openAIChatChunk
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return err
		}
		if len(event.Choices) == 0 {
			continue
		}
		choice := event.Choices[0]
		content := choice.Delta.Content
		if content != "" {
			for _, filtered := range thinkFilter.Filter(content) {
				if filtered == "" {
					continue
				}
				if err := emit(LLMEvent{Content: filtered}); err != nil {
					return err
				}
			}
		}
		for _, call := range choice.Delta.ToolCalls {
			tc := toolCalls[call.Index]
			if tc == nil {
				tc = &ToolCall{}
				toolCalls[call.Index] = tc
			}
			if call.ID != "" {
				tc.ID = call.ID
			}
			if call.Function.Name != "" {
				tc.Name = call.Function.Name
			}
			if call.Function.Arguments != "" {
				tc.Arguments += call.Function.Arguments
			}
		}
		if choice.FinishReason == "tool_calls" {
			indexes := make([]int, 0, len(toolCalls))
			for index := range toolCalls {
				indexes = append(indexes, index)
			}
			sort.Ints(indexes)
			for _, index := range indexes {
				call := toolCalls[index]
				if call == nil || call.Name == "" {
					continue
				}
				if err := emit(LLMEvent{ToolCall: call}); err != nil {
					return err
				}
			}
			toolCalls = map[int]*ToolCall{}
		}
	}
	return scanner.Err()
}

type thinkTagFilter struct {
	active  bool
	pending string
}

func newThinkTagFilter() *thinkTagFilter {
	return &thinkTagFilter{active: true}
}

func (f *thinkTagFilter) Filter(chunk string) []string {
	if chunk == "" {
		return nil
	}
	text := f.pending + chunk
	f.pending = ""
	var out []string
	for text != "" {
		if f.active {
			index := strings.Index(text, "<think>")
			if index >= 0 {
				if index > 0 {
					out = append(out, text[:index])
				}
				text = text[index+len("<think>"):]
				f.active = false
				continue
			}
			if suffix := partialThinkTagSuffix(text, "<think>"); suffix > 0 {
				if len(text) > suffix {
					out = append(out, text[:len(text)-suffix])
				}
				f.pending = text[len(text)-suffix:]
				return out
			}
			out = append(out, text)
			return out
		}

		index := strings.Index(text, "</think>")
		if index >= 0 {
			text = text[index+len("</think>"):]
			f.active = true
			continue
		}
		if suffix := partialThinkTagSuffix(text, "</think>"); suffix > 0 {
			f.pending = text[len(text)-suffix:]
			return out
		}
		text = ""
	}
	return out
}

func partialThinkTagSuffix(text string, tag string) int {
	max := len(tag) - 1
	if len(text) < max {
		max = len(text)
	}
	for size := max; size > 0; size-- {
		if strings.HasSuffix(text, tag[:size]) {
			return size
		}
	}
	return 0
}

func chatCompletionsURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/chat/completions") {
		return baseURL
	}
	return baseURL + "/chat/completions"
}

func toOpenAIMessages(history []Message) []openAIMessage {
	messages := make([]openAIMessage, 0, len(history))
	for _, message := range history {
		if strings.TrimSpace(message.Role) == "" {
			continue
		}
		if strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 {
			continue
		}
		messages = append(messages, openAIMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
			ToolCalls:  toOpenAIToolCalls(message.ToolCalls),
		})
	}
	return messages
}

func toOpenAITools(tools []Tool) []openAITool {
	out := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		out = append(out, openAITool{
			Type: "function",
			Function: openAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}
	return out
}

func toOpenAIToolCalls(calls []ToolCall) []openAIMessageToolCall {
	out := make([]openAIMessageToolCall, 0, len(calls))
	for i, call := range calls {
		out = append(out, openAIMessageToolCall{
			ID:    call.ID,
			Type:  "function",
			Index: i,
			Function: openAIToolCallFunction{
				Name:      call.Name,
				Arguments: call.Arguments,
			},
		})
	}
	return out
}

type openAIChatRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	Tools     []openAITool    `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role       string                  `json:"role"`
	Content    string                  `json:"content,omitempty"`
	ToolCallID string                  `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIMessageToolCall `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  ToolParameters `json:"parameters"`
}

type openAIMessageToolCall struct {
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type"`
	Index    int                    `json:"index,omitempty"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAIChatChunk struct {
	Choices []struct {
		Delta struct {
			Content   string                  `json:"content"`
			ToolCalls []openAIMessageToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}
