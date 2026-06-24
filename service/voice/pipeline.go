package voice

import (
	"context"
	"strings"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

type AudioFrame struct {
	Opus []byte
	PCM  *audio.PCMFrame
}

type Message struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
	SkipSpeech bool
	RequireLLM bool
}

type Utterance struct {
	SessionID string
	Prompt    string
	Frames    []AudioFrame
	History   []Message
}

type Response struct {
	Transcript string
	Assistant  string
	Emotion    string
	Segments   []SpeechSegment
}

type SpeechSegment struct {
	Text  string
	Audio [][]byte
}

type ASR interface {
	Transcribe(ctx context.Context, sessionID string, frames []AudioFrame) (string, error)
}

type LLM interface {
	Respond(ctx context.Context, sessionID string, history []Message) (<-chan string, error)
}

type ToolLLM interface {
	RespondWithTools(ctx context.Context, sessionID string, history []Message, tools []Tool) (<-chan LLMEvent, error)
}

type LLMEvent struct {
	Content  string
	ToolCall *ToolCall
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type Tool struct {
	Name        string
	Description string
	Parameters  ToolParameters
}

type ToolExecutor func(ctx context.Context, call ToolCall) (Message, error)

type ToolParameters struct {
	Type       string                  `json:"type"`
	Properties map[string]ToolProperty `json:"properties,omitempty"`
	Required   []string                `json:"required,omitempty"`
}

type ToolProperty struct {
	Type        string                  `json:"type"`
	Description string                  `json:"description,omitempty"`
	Properties  map[string]ToolProperty `json:"properties,omitempty"`
	Required    []string                `json:"required,omitempty"`
}

type TTS interface {
	Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error)
}

type VAD interface {
	HasVoice(ctx context.Context, frame AudioFrame) (bool, error)
}

type Memory interface {
	Query(ctx context.Context, query string) (string, error)
}

type Pipeline struct {
	asr        ASR
	llm        LLM
	tts        TTS
	ttsTimeout time.Duration
	memory     Memory
	splitMaxRunes int // 兜底切句长度（rune）；<=0 用默认值。流式合成时影响首句何时触达。
	// onSegment 在每段合成完成、追加到 Response.Segments 之前同步调用。
	// 非 nil 时实现"流式发送"——合成出一句即推送给调用方（如 handler 立即下发设备），
	// 不必等整段回复合成完毕。返回非 nil error（如取消 sentinel）会中止 pipeline。
	// nil = 批量模式（合成完全部攒进 Segments 后再返回，旧行为）。
	onSegment func(SpeechSegment) error
}

// WithSegmentSink 返回一个绑定了分段 sink 的新 *Pipeline（复制现有字段，不改接收者）。
// 调用方用返回值调 Respond*，原 pipeline 不受影响——避免在共享字段上反复装/卸 sink。
func (p *Pipeline) WithSegmentSink(fn func(SpeechSegment) error) *Pipeline {
	cp := *p
	cp.onSegment = fn
	return &cp
}

func NewPipeline(asr ASR, llm LLM, tts TTS) *Pipeline {
	return &Pipeline{asr: asr, llm: llm, tts: tts}
}

func (p *Pipeline) WithMemory(memory Memory) *Pipeline {
	p.memory = memory
	return p
}

func NewDefaultPipeline(conf config.LocalProviderConf) *Pipeline {
	return NewPipelineFromConfig(config.Get().ASR, config.Get().LLM, config.Get().TTS)
}

func NewPipelineFromConfig(asr config.ASRConf, llm config.LLMConf, tts config.TTSConf) *Pipeline {
	p := NewPipeline(
		NewASR(asr),
		NewLLM(llm),
		NewTTS(tts),
	)
	if tts.TimeoutSeconds > 0 {
		p.ttsTimeout = time.Duration(tts.TimeoutSeconds) * time.Second
	}
	p.splitMaxRunes = tts.SplitMaxChars
	return p
}

// newSplitter 构造本 pipeline 配置下的分句器（标点优先 + 长度兜底）。
func (p *Pipeline) newSplitter() *SentenceSplitter {
	if p.splitMaxRunes > 0 {
		return NewSentenceSplitterWithMax(p.splitMaxRunes)
	}
	return NewSentenceSplitter()
}

func (p *Pipeline) Process(ctx context.Context, req Utterance) (Response, error) {
	transcript, err := p.Transcribe(ctx, req.SessionID, req.Frames)
	if err != nil {
		return Response{}, err
	}
	if transcript == "" {
		return Response{}, nil
	}

	return p.Respond(ctx, req.SessionID, req.Prompt, req.History, transcript)
}

func (p *Pipeline) Respond(ctx context.Context, sessionID string, prompt string, history []Message, transcript string) (Response, error) {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return Response{}, nil
	}
	if p.memory != nil {
		memoryText, err := p.memory.Query(ctx, transcript)
		if err != nil {
			return Response{}, err
		}
		prompt = promptWithMemory(prompt, memoryText)
	}

	history = buildLLMHistory(prompt, history)
	history = append(history, Message{Role: "user", Content: transcript})
	stream, err := p.llm.Respond(ctx, sessionID, history)
	if err != nil {
		return Response{}, err
	}

	response := Response{
		Transcript: transcript,
		Emotion:    "happy",
	}
	splitter := p.newSplitter()
	var assistant strings.Builder
	for chunk := range stream {
		assistant.WriteString(chunk)
		for _, text := range splitter.Push(chunk) {
			if err := p.emitSegment(ctx, sessionID, text, &response); err != nil {
				return Response{}, err
			}
		}
	}
	for _, text := range splitter.Flush() {
		if err := p.emitSegment(ctx, sessionID, text, &response); err != nil {
			return Response{}, err
		}
	}
	response.Assistant = assistant.String()
	return response, nil
}

func (p *Pipeline) RespondWithTools(ctx context.Context, sessionID string, prompt string, history []Message, transcript string, tools []Tool, exec ToolExecutor) (Response, error) {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return Response{}, nil
	}
	if len(tools) == 0 || exec == nil {
		return p.Respond(ctx, sessionID, prompt, history, transcript)
	}
	toolLLM, ok := p.llm.(ToolLLM)
	if !ok {
		return p.Respond(ctx, sessionID, prompt, history, transcript)
	}
	if p.memory != nil {
		memoryText, err := p.memory.Query(ctx, transcript)
		if err != nil {
			return Response{}, err
		}
		prompt = promptWithMemory(prompt, memoryText)
	}

	history = buildLLMHistory(prompt, history)
	history = append(history, Message{Role: "user", Content: transcript})
	response := Response{Transcript: transcript, Emotion: "happy"}
	for depth := 0; depth < 3; depth++ {
		stream, err := toolLLM.RespondWithTools(ctx, sessionID, history, tools)
		if err != nil {
			return Response{}, err
		}
		text, calls, err := p.consumeLLMEvents(ctx, sessionID, stream, &response)
		if err != nil {
			return Response{}, err
		}
		if text != "" {
			history = append(history, Message{Role: "assistant", Content: text})
			response.Assistant += text
		}
		if len(calls) == 0 {
			return response, nil
		}
		history = append(history, Message{Role: "assistant", ToolCalls: calls})
		for _, call := range calls {
			toolMessage, err := exec(ctx, call)
			if err != nil {
				return Response{}, err
			}
			history = append(history, toolMessage)
			if toolMessage.SkipSpeech {
				return response, nil
			}
			if toolMessage.RequireLLM {
				continue
			}
			if strings.TrimSpace(toolMessage.Content) != "" {
				response.Assistant += toolMessage.Content
				if err := p.emitSegment(ctx, sessionID, toolMessage.Content, &response); err != nil {
					return Response{}, err
				}
			}
		}
		if len(response.Segments) > 0 {
			return response, nil
		}
	}
	return response, nil
}

func (p *Pipeline) RespondWithIntentTools(ctx context.Context, sessionID string, prompt string, history []Message, transcript string, tools []Tool, exec ToolExecutor) (Response, bool, error) {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" || len(tools) == 0 || exec == nil {
		return Response{}, false, nil
	}
	toolLLM, ok := p.llm.(ToolLLM)
	if !ok {
		return Response{}, false, nil
	}

	intentHistory := buildLLMHistory(intentSystemPrompt(tools), history)
	intentHistory = append(intentHistory, Message{Role: "user", Content: transcript})
	stream, err := toolLLM.RespondWithTools(ctx, sessionID, intentHistory, tools)
	if err != nil {
		return Response{}, false, err
	}
	var calls []ToolCall
	for event := range stream {
		if event.ToolCall != nil {
			calls = append(calls, *event.ToolCall)
		}
	}
	if len(calls) == 0 {
		return Response{}, false, nil
	}

	response := Response{Transcript: transcript, Emotion: "happy"}
	for _, call := range calls {
		if strings.TrimSpace(call.Name) == "continue_chat" {
			return Response{}, false, nil
		}
		toolMessage, err := exec(ctx, call)
		if err != nil {
			return Response{}, false, err
		}
		if toolMessage.SkipSpeech {
			return response, true, nil
		}
		content := strings.TrimSpace(toolMessage.Content)
		if content == "" {
			continue
		}
		response.Assistant += toolMessage.Content
		if err := p.emitSegment(ctx, sessionID, toolMessage.Content, &response); err != nil {
			return Response{}, false, err
		}
	}
	return response, true, nil
}

func (p *Pipeline) consumeLLMEvents(ctx context.Context, sessionID string, stream <-chan LLMEvent, response *Response) (string, []ToolCall, error) {
	var builder strings.Builder
	var calls []ToolCall
	splitter := p.newSplitter()
	for event := range stream {
		if event.ToolCall != nil {
			calls = append(calls, *event.ToolCall)
			continue
		}
		builder.WriteString(event.Content)
		for _, text := range splitter.Push(event.Content) {
			if err := p.emitSegment(ctx, sessionID, text, response); err != nil {
				return "", nil, err
			}
		}
	}
	for _, text := range splitter.Flush() {
		if err := p.emitSegment(ctx, sessionID, text, response); err != nil {
			return "", nil, err
		}
	}
	return builder.String(), calls, nil
}

func appendSpeechSegment(segments *[]SpeechSegment, segment SpeechSegment) {
	if strings.TrimSpace(segment.Text) == "" {
		return
	}
	*segments = append(*segments, segment)
}

func (p *Pipeline) Transcribe(ctx context.Context, sessionID string, frames []AudioFrame) (string, error) {
	transcript, err := p.asr.Transcribe(ctx, sessionID, frames)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(transcript), nil
}

func (p *Pipeline) SynthesizeText(ctx context.Context, sessionID string, text string) (SpeechSegment, error) {
	return p.synthesizeSegment(ctx, sessionID, text)
}

func (p *Pipeline) synthesizeSegment(ctx context.Context, sessionID string, text string) (SpeechSegment, error) {
	text = trimEdgePunctuationAndEmoji(text)
	text = cleanMarkdownForTTS(text)
	if text == "" {
		return SpeechSegment{}, nil
	}
	ttsCtx := ctx
	cancel := func() {}
	if p.ttsTimeout > 0 {
		ttsCtx, cancel = context.WithTimeout(ctx, p.ttsTimeout)
	}
	defer cancel()
	audio, err := p.tts.Synthesize(ttsCtx, sessionID, text)
	if err != nil {
		return SpeechSegment{}, err
	}
	return SpeechSegment{Text: text, Audio: audio}, nil
}

// emitSegment 合成一段、可选流式推送、并记录到 response.Segments。
// onSegment 非 nil 时同步流式（合成出即推送）；返回其错误（如取消 sentinel）则中止。
// onSegment 为 nil 时与旧行为一致（合成后仅 append）。
func (p *Pipeline) emitSegment(ctx context.Context, sessionID, text string, response *Response) error {
	segment, err := p.synthesizeSegment(ctx, sessionID, text)
	if err != nil {
		return err
	}
	if p.onSegment != nil {
		if err := p.onSegment(segment); err != nil {
			return err
		}
	}
	appendSpeechSegment(&response.Segments, segment)
	return nil
}

func buildLLMHistory(prompt string, history []Message) []Message {
	out := make([]Message, 0, len(history)+1)
	prompt = strings.TrimSpace(prompt)
	hasSystem := false
	for _, message := range history {
		if strings.TrimSpace(message.Role) == "system" {
			hasSystem = true
			break
		}
	}
	if prompt != "" && !hasSystem {
		out = append(out, Message{Role: "system", Content: prompt})
	}
	out = append(out, history...)
	return out
}

func promptWithMemory(prompt string, memoryText string) string {
	memoryText = strings.TrimSpace(memoryText)
	if memoryText == "" {
		return prompt
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "相关记忆：\n" + memoryText
	}
	return prompt + "\n\n相关记忆：\n" + memoryText
}

func intentSystemPrompt(tools []Tool) string {
	names := make([]string, 0, len(tools)+1)
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	names = append(names, "continue_chat")
	return "你是一个意图识别助手。请只通过工具调用判断用户意图。可用意图：" + strings.Join(names, ",") + "。如果没有明显意图，请调用continue_chat。"
}

type EchoLLM struct {
	WelcomeMessage  string
	EchoTranscripts bool
}

func (l EchoLLM) Respond(ctx context.Context, sessionID string, history []Message) (<-chan string, error) {
	ch := make(chan string, 1)
	reply := strings.TrimSpace(l.WelcomeMessage)
	if reply == "" {
		reply = "你好，我是小智。"
	}
	if l.EchoTranscripts {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role == "user" && strings.TrimSpace(history[i].Content) != "" {
				reply = "我听到了：" + strings.TrimSpace(history[i].Content)
				break
			}
		}
	}
	ch <- reply
	close(ch)
	return ch, nil
}

type SilentTTS struct{}

func (SilentTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	return nil, nil
}
