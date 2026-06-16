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
	return p
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
	splitter := NewSentenceSplitter()
	var assistant strings.Builder
	for chunk := range stream {
		assistant.WriteString(chunk)
		for _, text := range splitter.Push(chunk) {
			segment, err := p.synthesizeSegment(ctx, sessionID, text)
			if err != nil {
				return Response{}, err
			}
			appendSpeechSegment(&response.Segments, segment)
		}
	}
	for _, text := range splitter.Flush() {
		segment, err := p.synthesizeSegment(ctx, sessionID, text)
		if err != nil {
			return Response{}, err
		}
		appendSpeechSegment(&response.Segments, segment)
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
				segment, err := p.synthesizeSegment(ctx, sessionID, toolMessage.Content)
				if err != nil {
					return Response{}, err
				}
				appendSpeechSegment(&response.Segments, segment)
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
		segment, err := p.synthesizeSegment(ctx, sessionID, toolMessage.Content)
		if err != nil {
			return Response{}, false, err
		}
		appendSpeechSegment(&response.Segments, segment)
	}
	return response, true, nil
}

func (p *Pipeline) consumeLLMEvents(ctx context.Context, sessionID string, stream <-chan LLMEvent, response *Response) (string, []ToolCall, error) {
	var builder strings.Builder
	var calls []ToolCall
	splitter := NewSentenceSplitter()
	for event := range stream {
		if event.ToolCall != nil {
			calls = append(calls, *event.ToolCall)
			continue
		}
		builder.WriteString(event.Content)
		for _, text := range splitter.Push(event.Content) {
			segment, err := p.synthesizeSegment(ctx, sessionID, text)
			if err != nil {
				return "", nil, err
			}
			appendSpeechSegment(&response.Segments, segment)
		}
	}
	for _, text := range splitter.Flush() {
		segment, err := p.synthesizeSegment(ctx, sessionID, text)
		if err != nil {
			return "", nil, err
		}
		appendSpeechSegment(&response.Segments, segment)
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
