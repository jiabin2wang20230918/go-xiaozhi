package voice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/audio"
)

func TestDefaultPipelineProcessesUtterance(t *testing.T) {
	p := NewPipeline(
		fixedASR{text: "收到语音"},
		EchoLLM{WelcomeMessage: "你好", EchoTranscripts: true},
		captureTTS{},
	)

	resp, err := p.Process(context.Background(), Utterance{
		SessionID: "s1",
		Frames: []AudioFrame{
			{PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1, 2, 3}}},
		},
	})
	if err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	if resp.Transcript != "收到语音" {
		t.Fatalf("unexpected transcript: %q", resp.Transcript)
	}
	if len(resp.Segments) != 2 {
		t.Fatalf("expected two speech segments, got %d", len(resp.Segments))
	}
	if resp.Segments[0].Text != "我听到了：" || resp.Segments[1].Text != "收到语音" {
		t.Fatalf("unexpected response segments: %+v", resp.Segments)
	}
	if resp.Assistant != "我听到了：收到语音" {
		t.Fatalf("assistant raw text = %q", resp.Assistant)
	}
}

func TestDefaultPipelineIgnoresEmptyAudio(t *testing.T) {
	p := NewPipeline(
		fixedASR{text: ""},
		EchoLLM{},
		captureTTS{},
	)
	resp, err := p.Process(context.Background(), Utterance{SessionID: "s1"})
	if err != nil {
		t.Fatalf("process empty utterance: %v", err)
	}
	if resp.Transcript != "" || len(resp.Segments) != 0 {
		t.Fatalf("expected empty response, got %+v", resp)
	}
}

func TestPipelineSplitsStreamingLLMByPunctuation(t *testing.T) {
	p := NewPipeline(
		fixedASR{text: "用户语音"},
		streamLLM{chunks: []string{"第一", "句。第二句", "！尾巴"}},
		captureTTS{},
	)
	resp, err := p.Process(context.Background(), Utterance{
		SessionID: "s1",
		Frames: []AudioFrame{
			{PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1}}},
		},
	})
	if err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	if len(resp.Segments) != 3 {
		t.Fatalf("expected 3 segments, got %d: %+v", len(resp.Segments), resp.Segments)
	}
	want := []string{"第一句", "第二句", "尾巴"}
	for i, segment := range resp.Segments {
		if segment.Text != want[i] {
			t.Fatalf("segment[%d] got %q want %q", i, segment.Text, want[i])
		}
	}
	if resp.Assistant != "第一句。第二句！尾巴" {
		t.Fatalf("assistant raw text = %q", resp.Assistant)
	}
}

func TestPipelineAddsPromptBeforeHistory(t *testing.T) {
	llm := &captureLLM{chunks: []string{"好的"}}
	p := NewPipeline(
		fixedASR{text: "现在几点"},
		llm,
		captureTTS{},
	)

	_, err := p.Process(context.Background(), Utterance{
		SessionID: "s1",
		Prompt:    "你是小智。",
		History: []Message{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "你好。"},
		},
		Frames: []AudioFrame{{PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1}}}},
	})
	if err != nil {
		t.Fatalf("process utterance: %v", err)
	}

	want := []Message{
		{Role: "system", Content: "你是小智。"},
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好。"},
		{Role: "user", Content: "现在几点"},
	}
	if len(llm.history) != len(want) {
		t.Fatalf("history length got %d want %d: %+v", len(llm.history), len(want), llm.history)
	}
	for i := range want {
		if llm.history[i].Role != want[i].Role || llm.history[i].Content != want[i].Content {
			t.Fatalf("history[%d] got %+v want %+v", i, llm.history[i], want[i])
		}
	}
}

func TestPipelineAddsQueriedMemoryToPrompt(t *testing.T) {
	llm := &captureLLM{chunks: []string{"好的"}}
	memory := &captureMemory{text: "用户喜欢咖啡"}
	p := NewPipeline(
		fixedASR{text: "推荐饮品"},
		llm,
		captureTTS{},
	).WithMemory(memory)

	_, err := p.Process(context.Background(), Utterance{
		SessionID: "s1",
		Prompt:    "你是小智。",
		Frames:    []AudioFrame{{PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1}}}},
	})
	if err != nil {
		t.Fatalf("process utterance: %v", err)
	}

	if memory.query != "推荐饮品" {
		t.Fatalf("memory query got %q", memory.query)
	}
	if len(llm.history) == 0 || llm.history[0].Role != "system" {
		t.Fatalf("missing system prompt: %+v", llm.history)
	}
	if got := llm.history[0].Content; got != "你是小智。\n\n相关记忆：\n用户喜欢咖啡" {
		t.Fatalf("unexpected prompt with memory: %q", got)
	}
}

func TestTrimEdgePunctuationAndEmojiMatchesPythonTTSInput(t *testing.T) {
	tests := map[string]string{
		"。你好！":        "你好",
		"😊第一句。":       "第一句",
		"你好吗？":        "你好吗？",
		"hello-world": "hello-world",
		"！！":          "",
	}
	for input, want := range tests {
		if got := trimEdgePunctuationAndEmoji(input); got != want {
			t.Fatalf("trimEdgePunctuationAndEmoji(%q)=%q want %q", input, got, want)
		}
	}
}

func TestCleanMarkdownForTTSMatchesPythonCleaner(t *testing.T) {
	input := strings.Join([]string{
		"# 标题",
		"这是 **重点** 和 [链接](https://example.com)。",
		"![图片](https://example.com/a.png)",
		"> 引用内容",
		"| 名称 | 数量 |",
		"| --- | --- |",
		"| 苹果 | 2 |",
		"",
		"```go",
		"fmt.Println(\"skip\")",
		"```",
		"公式 $$x = y$$ 和 $a+b$，价格 $12$。",
	}, "\n")

	got := cleanMarkdownForTTS(input)
	want := strings.Join([]string{
		"标题",
		"这是 重点 和 链接。",
		"引用内容",
		"表头是：名称, 数量",
		"第 1 行：名称 = 苹果, 数量 = 2",
		"公式  和 a+b，价格 $12$。",
	}, "\n")
	if got != want {
		t.Fatalf("cleanMarkdownForTTS() = %q, want %q", got, want)
	}
}

func TestPipelineCleansMarkdownBeforeTTS(t *testing.T) {
	p := NewPipeline(
		fixedASR{text: "用户语音"},
		streamLLM{chunks: []string{"**你好**，[看这里](https://example.com)。"}},
		captureTTS{},
	)

	resp, err := p.Process(context.Background(), Utterance{
		SessionID: "s1",
		Frames:    []AudioFrame{{PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1}}}},
	})
	if err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	if len(resp.Segments) != 1 || resp.Segments[0].Text != "你好，看这里" {
		t.Fatalf("unexpected cleaned tts segment: %+v", resp.Segments)
	}
	if string(resp.Segments[0].Audio[0]) != "你好，看这里" {
		t.Fatalf("tts received unclean text: %q", resp.Segments[0].Audio[0])
	}
	if resp.Assistant != "**你好**，[看这里](https://example.com)。" {
		t.Fatalf("assistant history text should preserve raw llm output, got %q", resp.Assistant)
	}
}

func TestPipelineTrimsBeforeMarkdownLikePythonTTS(t *testing.T) {
	p := NewPipeline(
		fixedASR{text: "用户语音"},
		streamLLM{chunks: []string{"[你好!](https://example.com)。"}},
		captureTTS{},
	)

	resp, err := p.Process(context.Background(), Utterance{
		SessionID: "s1",
		Frames:    []AudioFrame{{PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1}}}},
	})
	if err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	if len(resp.Segments) != 1 || resp.Segments[0].Text != "你好!" {
		t.Fatalf("unexpected tts segment: %+v", resp.Segments)
	}
}

func TestPipelineSkipsSegmentsThatCleanToEmptyText(t *testing.T) {
	p := NewPipeline(
		fixedASR{text: "用户语音"},
		streamLLM{chunks: []string{"```go\nfmt.Println(\"skip\")\n```。"}},
		captureTTS{},
	)

	resp, err := p.Process(context.Background(), Utterance{
		SessionID: "s1",
		Frames:    []AudioFrame{{PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1}}}},
	})
	if err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	if len(resp.Segments) != 0 {
		t.Fatalf("empty cleaned text should not produce tts segments: %+v", resp.Segments)
	}
}

func TestPipelineAppliesTTSTimeout(t *testing.T) {
	p := NewPipeline(
		fixedASR{text: "用户语音"},
		streamLLM{chunks: []string{"回复。"}},
		blockingTTS{},
	)
	p.ttsTimeout = time.Millisecond

	_, err := p.Process(context.Background(), Utterance{
		SessionID: "s1",
		Frames:    []AudioFrame{{PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1}}}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected tts deadline exceeded, got %v", err)
	}
}

func TestPipelineRespondUsesExistingTranscript(t *testing.T) {
	asr := &countingASR{text: "should not be used"}
	llm := &captureLLM{chunks: []string{"回复"}}
	p := NewPipeline(asr, llm, captureTTS{})

	resp, err := p.Respond(context.Background(), "s1", "prompt", nil, "已有文本")
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if asr.calls != 0 {
		t.Fatalf("respond should not call asr, got %d calls", asr.calls)
	}
	if resp.Transcript != "已有文本" {
		t.Fatalf("transcript got %q", resp.Transcript)
	}
}

func TestPipelineRespondWithToolsExecutesToolCall(t *testing.T) {
	llm := &toolLLM{events: []LLMEvent{{ToolCall: &ToolCall{ID: "call_1", Name: "lamp_setpower", Arguments: "{\"value\":false}"}}}}
	p := NewPipeline(fixedASR{text: "关灯"}, llm, captureTTS{})

	resp, err := p.RespondWithTools(context.Background(), "s1", "prompt", nil, "关灯", []Tool{{Name: "lamp_setpower"}}, func(ctx context.Context, call ToolCall) (Message, error) {
		if call.Name != "lamp_setpower" || call.Arguments != "{\"value\":false}" {
			t.Fatalf("unexpected call: %+v", call)
		}
		return Message{Role: "tool", ToolCallID: call.ID, Content: "已关灯"}, nil
	})
	if err != nil {
		t.Fatalf("respond with tools: %v", err)
	}
	if len(resp.Segments) != 1 || resp.Segments[0].Text != "已关灯" {
		t.Fatalf("expected tool response segment, got %+v", resp.Segments)
	}
	if resp.Assistant != "已关灯" {
		t.Fatalf("assistant tool response = %q", resp.Assistant)
	}
}

func TestPipelineRespondWithToolsAddsQueriedMemoryToPrompt(t *testing.T) {
	llm := &toolLLM{events: []LLMEvent{{Content: "好的。"}}}
	memory := &captureMemory{text: "用户喜欢安静"}
	p := NewPipeline(fixedASR{text: "调暗灯光"}, llm, captureTTS{}).WithMemory(memory)

	resp, err := p.RespondWithTools(context.Background(), "s1", "你是小智。", nil, "调暗灯光", []Tool{{Name: "lamp_setpower"}}, func(ctx context.Context, call ToolCall) (Message, error) {
		return Message{}, nil
	})
	if err != nil {
		t.Fatalf("respond with tools: %v", err)
	}
	if memory.query != "调暗灯光" {
		t.Fatalf("memory query got %q", memory.query)
	}
	if resp.Assistant != "好的。" {
		t.Fatalf("assistant response got %q", resp.Assistant)
	}
	if len(llm.history) == 0 || llm.history[0].Role != "system" {
		t.Fatalf("missing system prompt: %+v", llm.history)
	}
	if got := llm.history[0].Content; got != "你是小智。\n\n相关记忆：\n用户喜欢安静" {
		t.Fatalf("unexpected tool prompt with memory: %q", got)
	}
}

func TestPipelineRespondWithToolsStopsAfterSideEffectTool(t *testing.T) {
	llm := &toolLLM{events: []LLMEvent{{ToolCall: &ToolCall{ID: "call_1", Name: "play_music", Arguments: "{\"song_name\":\"random\"}"}}}}
	p := NewPipeline(fixedASR{text: "播放音乐"}, llm, captureTTS{})

	resp, err := p.RespondWithTools(context.Background(), "s1", "prompt", nil, "播放音乐", []Tool{{Name: "play_music"}}, func(ctx context.Context, call ToolCall) (Message, error) {
		return Message{Role: "tool", ToolCallID: call.ID, SkipSpeech: true}, nil
	})
	if err != nil {
		t.Fatalf("respond with tools: %v", err)
	}
	if len(resp.Segments) != 0 {
		t.Fatalf("side-effect tool should not synthesize speech: %+v", resp.Segments)
	}
	if resp.Assistant != "" {
		t.Fatalf("side-effect tool should not create assistant history text, got %q", resp.Assistant)
	}
	if llm.calls != 1 {
		t.Fatalf("side-effect tool should stop tool loop after one llm call, got %d", llm.calls)
	}
}

func TestPipelineRespondWithToolsCanRequireSecondLLMCall(t *testing.T) {
	llm := &sequenceToolLLM{responses: [][]LLMEvent{
		{{ToolCall: &ToolCall{ID: "call_1", Name: "get_time", Arguments: "{}"}}},
		{{Content: "现在是上午八点。"}},
	}}
	p := NewPipeline(fixedASR{text: "几点了"}, llm, captureTTS{})

	resp, err := p.RespondWithTools(context.Background(), "s1", "prompt", nil, "几点了", []Tool{{Name: "get_time"}}, func(ctx context.Context, call ToolCall) (Message, error) {
		return Message{Role: "tool", ToolCallID: call.ID, Content: "当前日期: 2026-06-15，当前时间: 08:00:00， 星期一", RequireLLM: true}, nil
	})
	if err != nil {
		t.Fatalf("respond with tools: %v", err)
	}
	if len(resp.Segments) != 1 || resp.Segments[0].Text != "现在是上午八点" {
		t.Fatalf("expected second llm speech, got %+v", resp.Segments)
	}
	if llm.calls != 2 {
		t.Fatalf("expected second llm call, got %d", llm.calls)
	}
}

func TestPipelineRespondWithIntentToolsExecutesDirectToolLikePythonIntent(t *testing.T) {
	llm := &toolLLM{events: []LLMEvent{{ToolCall: &ToolCall{ID: "call_1", Name: "get_time", Arguments: "{}"}}}}
	p := NewPipeline(fixedASR{text: "几点了"}, llm, captureTTS{})

	resp, handled, err := p.RespondWithIntentTools(context.Background(), "s1", "prompt", nil, "几点了", []Tool{{Name: "get_time"}}, func(ctx context.Context, call ToolCall) (Message, error) {
		if call.Name != "get_time" {
			t.Fatalf("unexpected call: %+v", call)
		}
		return Message{Role: "tool", ToolCallID: call.ID, Content: "当前日期: 2026-06-15，当前时间: 08:00:00， 星期一", RequireLLM: true}, nil
	})
	if err != nil {
		t.Fatalf("respond with intent tools: %v", err)
	}
	if !handled {
		t.Fatal("intent tool call should be handled")
	}
	if len(resp.Segments) != 1 || resp.Segments[0].Text != "当前日期: 2026-06-15，当前时间: 08:00:00， 星期一" {
		t.Fatalf("expected direct intent tool speech, got %+v", resp.Segments)
	}
	if llm.calls != 1 || llm.plainCalls != 0 {
		t.Fatalf("intent tool should not run second llm call, tool=%d plain=%d", llm.calls, llm.plainCalls)
	}
}

func TestPipelineRespondWithIntentToolsContinueChatFallsBack(t *testing.T) {
	llm := &toolLLM{events: []LLMEvent{{ToolCall: &ToolCall{ID: "call_1", Name: "continue_chat", Arguments: "{}"}}}}
	p := NewPipeline(fixedASR{text: "闲聊"}, llm, captureTTS{})

	resp, handled, err := p.RespondWithIntentTools(context.Background(), "s1", "prompt", nil, "闲聊", []Tool{{Name: "get_time"}}, func(ctx context.Context, call ToolCall) (Message, error) {
		t.Fatalf("continue_chat should not execute a tool: %+v", call)
		return Message{}, nil
	})
	if err != nil {
		t.Fatalf("respond with intent tools: %v", err)
	}
	if handled {
		t.Fatal("continue_chat should fall back to normal chat")
	}
	if resp.Transcript != "" || len(resp.Segments) != 0 {
		t.Fatalf("fallback marker should not contain speech response: %+v", resp)
	}
}

type fixedASR struct {
	text string
}

func (a fixedASR) Transcribe(ctx context.Context, sessionID string, frames []AudioFrame) (string, error) {
	return a.text, nil
}

type countingASR struct {
	text  string
	calls int
}

func (a *countingASR) Transcribe(ctx context.Context, sessionID string, frames []AudioFrame) (string, error) {
	a.calls++
	return a.text, nil
}

type streamLLM struct {
	chunks []string
}

func (l streamLLM) Respond(ctx context.Context, sessionID string, history []Message) (<-chan string, error) {
	ch := make(chan string, len(l.chunks))
	for _, chunk := range l.chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

type captureLLM struct {
	chunks  []string
	history []Message
}

func (l *captureLLM) Respond(ctx context.Context, sessionID string, history []Message) (<-chan string, error) {
	l.history = append([]Message(nil), history...)
	ch := make(chan string, len(l.chunks))
	for _, chunk := range l.chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

type toolLLM struct {
	events     []LLMEvent
	calls      int
	plainCalls int
	history    []Message
}

func (l *toolLLM) Respond(ctx context.Context, sessionID string, history []Message) (<-chan string, error) {
	l.plainCalls++
	ch := make(chan string)
	close(ch)
	return ch, nil
}

func (l *toolLLM) RespondWithTools(ctx context.Context, sessionID string, history []Message, tools []Tool) (<-chan LLMEvent, error) {
	l.calls++
	l.history = append([]Message(nil), history...)
	ch := make(chan LLMEvent, len(l.events))
	for _, event := range l.events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

type sequenceToolLLM struct {
	responses [][]LLMEvent
	calls     int
}

func (l *sequenceToolLLM) Respond(ctx context.Context, sessionID string, history []Message) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, nil
}

func (l *sequenceToolLLM) RespondWithTools(ctx context.Context, sessionID string, history []Message, tools []Tool) (<-chan LLMEvent, error) {
	var events []LLMEvent
	if l.calls < len(l.responses) {
		events = l.responses[l.calls]
	}
	l.calls++
	ch := make(chan LLMEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

type captureMemory struct {
	text  string
	query string
}

func (m *captureMemory) Query(ctx context.Context, query string) (string, error) {
	m.query = query
	return m.text, nil
}

type captureTTS struct{}

func (captureTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	return [][]byte{[]byte(text)}, nil
}

type blockingTTS struct{}

func (blockingTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
