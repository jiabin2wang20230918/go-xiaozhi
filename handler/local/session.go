package local

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
	"github.com/xdimtech/go-xiaozhi/service/deviceconfig"
	"github.com/xdimtech/go-xiaozhi/service/homeassistant"
	"github.com/xdimtech/go-xiaozhi/service/iot"
	"github.com/xdimtech/go-xiaozhi/service/mcp"
	"github.com/xdimtech/go-xiaozhi/service/memory"
	"github.com/xdimtech/go-xiaozhi/service/news"
	"github.com/xdimtech/go-xiaozhi/service/search"
	"github.com/xdimtech/go-xiaozhi/service/sessionaudio"
	"github.com/xdimtech/go-xiaozhi/service/voice"
	"github.com/xdimtech/go-xiaozhi/service/weather"
)

const writeQueueSize = 1024

// errSpeechCanceled 表示流式发送期间用户打断（speechSeq 变化）。
// processText 视其为非错误（用户主动中止），不再记日志/返回错误，
// 但也不会再发 TTS Stop（handleAbort 已自带 stop）。
var errSpeechCanceled = errors.New("speech canceled")

var optionalToolFactories = map[string]func() voice.Tool{
	"play_music":      playMusicTool,
	"get_news":        getNewsTool,
	"get_weather":     getWeatherTool,
	"baidu_search":    baiduSearchTool,
	"hass_get_state":  hassGetStateTool,
	"hass_set_state":  hassSetStateTool,
	"hass_play_music": hassPlayMusicTool,
}

type Handler struct {
	ctx            context.Context
	cancel         context.CancelFunc
	sessionID      string
	client         ClientInfo
	closed         atomic.Bool
	speechSeq      atomic.Uint64
	closeAfterChat atomic.Bool
	writeQ         chan any

	mu             sync.Mutex
	listenMode     xiaozhiapi.ClientMode
	audioState     *sessionaudio.State
	history        []voice.Message
	pipeline       *voice.Pipeline
	memory         memory.Store
	runtime        deviceconfig.Runtime
	iotRegistry    *iot.Registry
	mcpManager     *mcp.Manager
	haClient       *homeassistant.Client
	newsClient     *news.Client
	searchClient   *search.Client
	weatherClient  *weather.Client
	sleep          func(time.Duration)
	now            func() time.Time
	testAfterWrite func(any)
}

func NewHandler(ctx context.Context, client ClientInfo) *Handler {
	h, err := NewHandlerWithMemory(ctx, client, memory.New(config.Get().Memory))
	if err != nil {
		h, err = NewHandlerWithMemory(ctx, client, memory.NoopStore{})
	}
	if err != nil {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		writeQ := make(chan any)
		close(writeQ)
		h := &Handler{
			ctx:           ctx,
			cancel:        cancel,
			sessionID:     uuid.NewString(),
			client:        client,
			writeQ:        writeQ,
			memory:        memory.NoopStore{},
			runtime:       deviceconfig.DisabledRuntime(config.Get()),
			mcpManager:    mcp.NewManager(config.Get().MCP),
			haClient:      homeassistant.New(config.Get().Plugins.HomeAssistant),
			newsClient:    news.New(config.Get().Plugins.GetNews),
			searchClient:  search.New(config.Get().Plugins.BaiduSearch),
			weatherClient: weather.New(config.Get().Plugins.GetWeather),
			sleep:         time.Sleep,
			now:           time.Now,
		}
		h.closed.Store(true)
		return h
	}
	return h
}

func NewHandlerWithMemory(ctx context.Context, client ClientInfo, mem memory.Store) (*Handler, error) {
	ctx, cancel := context.WithCancel(ctx)
	sessionID := uuid.NewString()
	runtime, err := loadRuntimeConfig(ctx, client)
	if err != nil {
		cancel()
		return nil, err
	}
	if mem == nil {
		mem = memory.NoopStore{}
	}
	if err := mem.Init(ctx, memoryRoleID(client, sessionID)); err != nil {
		cancel()
		return nil, err
	}
	h := &Handler{
		ctx:           ctx,
		cancel:        cancel,
		sessionID:     sessionID,
		client:        client,
		writeQ:        make(chan any, writeQueueSize),
		listenMode:    xiaozhiapi.ClientModeAuto,
		audioState:    sessionaudio.New(voice.NewVAD(runtime.Session.VAD), runtime.Session),
		pipeline:      voice.NewPipelineFromConfig(runtime.ASR, runtime.LLM, runtime.TTS).WithMemory(mem),
		memory:        mem,
		runtime:       runtime,
		iotRegistry:   iot.NewRegistry(),
		mcpManager:    mcp.NewManager(runtime.MCP),
		haClient:      homeassistant.New(runtime.Plugins.HomeAssistant),
		newsClient:    news.New(runtime.Plugins.GetNews),
		searchClient:  search.New(runtime.Plugins.BaiduSearch),
		weatherClient: weather.New(runtime.Plugins.GetWeather),
		sleep:         time.Sleep,
		now:           time.Now,
	}
	h.appendHomeAssistantDevicesToPrompt()
	_ = h.mcpManager.Initialize(ctx)
	log.Printf("session started session_id=%s remote_ip=%s device_id=%s asr=%s llm=%s tts=%s vad=%s", h.sessionID, h.client.RemoteIP, h.client.DeviceID, runtime.ASR.Type, runtime.LLM.Type, runtime.TTS.Type, runtime.Session.VAD.Type)
	_ = h.write(h.helloEvent())
	return h, nil
}

func (h *Handler) Recv(ctx context.Context) chan any {
	return h.writeQ
}

func (h *Handler) Close(ctx context.Context) error {
	if h.closed.CompareAndSwap(false, true) {
		h.saveMemory(ctx)
		if h.cancel != nil {
			h.cancel()
		}
		if h.mcpManager != nil {
			_ = h.mcpManager.Close()
		}
		close(h.writeQ)
	}
	return nil
}

func (h *Handler) saveMemory(ctx context.Context) {
	if h.memory == nil {
		return
	}
	h.mu.Lock()
	history := append([]voice.Message(nil), h.history...)
	h.mu.Unlock()
	_, _ = h.memory.Save(ctx, history)
}

func (h *Handler) Done() <-chan struct{} {
	return h.ctx.Done()
}

func (h *Handler) UnmarshalClientTextEvent(msg []byte) (any, error) {
	return xiaozhiapi.UnmarshalClientEvent(msg)
}

func (h *Handler) UnmarshalClientBinEvent(msg []byte) (any, error) {
	return xiaozhiapi.UnmarshalClientBinEvent(msg)
}

func (h *Handler) DispatchClientEvent(ctx context.Context, event any) (error, bool) {
	ev, ok := event.(xiaozhiapi.ClientEvent)
	if !ok {
		return errors.New("invalid xiaozhi client event"), false
	}
	if ev.ClientEventType() != xiaozhiapi.ClientEventTypeAppendBuffer {
		log.Printf("client event session_id=%s type=%s", h.sessionID, ev.ClientEventType())
	}

	switch e := ev.(type) {
	case *xiaozhiapi.ClientEventRawText:
		return h.write(e.Text), false
	case *xiaozhiapi.ClientEventHello:
		return h.handleHello(ctx, e), false
	case *xiaozhiapi.ClientEventListen:
		return h.handleListen(ctx, e), false
	case *xiaozhiapi.ClientEventAppendBuffer:
		return h.handleAudio(ctx, e), false
	case *xiaozhiapi.ClientEventAbort:
		return h.handleAbort(ctx, e), false
	case *xiaozhiapi.ClientEventIot:
		return h.handleIot(ctx, e), false
	default:
		return nil, false
	}
}

func (h *Handler) MarshalServerEvent(event any) ([]byte, error) {
	ev, ok := event.(xiaozhiapi.ServerEvent)
	if !ok {
		return nil, errors.New("invalid xiaozhi server event")
	}
	return json.Marshal(ev)
}

func (h *Handler) handleHello(ctx context.Context, event *xiaozhiapi.ClientEventHello) error {
	return h.write(h.helloEvent())
}

func (h *Handler) handleListen(ctx context.Context, event *xiaozhiapi.ClientEventListen) error {
	log.Printf("listen event session_id=%s state=%s mode=%s text=%q", h.sessionID, event.State, event.Mode, event.Text)
	h.mu.Lock()
	if event.Mode != "" {
		h.listenMode = event.Mode
		h.audioState.SetMode(string(event.Mode))
	}
	switch event.State {
	case xiaozhiapi.ClientStateListenStart:
		h.audioState.Start()
	case xiaozhiapi.ClientStateListenStop:
		result := h.audioState.Stop()
		h.mu.Unlock()
		if result.Ready {
			h.processUtteranceAsync(result.Frames)
		}
		return nil
	case xiaozhiapi.ClientStateListenDetect:
		h.audioState.Detect()
		text := removePunctuationLikePython(event.Text)
		if text != "" {
			h.mu.Unlock()
			h.processDetectedTextAsync(text)
			return nil
		}
	}
	h.mu.Unlock()
	return nil
}

func (h *Handler) handleAudio(ctx context.Context, event *xiaozhiapi.ClientEventAppendBuffer) error {
	h.mu.Lock()
	result, err := h.audioState.Push(ctx, event.Bytes)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if result.Ready {
		h.processUtteranceAsync(result.Frames)
		return nil
	}
	if result.NoVoiceTimeout {
		h.processNoVoiceTimeoutAsync()
		return nil
	}
	return nil
}

func (h *Handler) processUtteranceAsync(frames []voice.AudioFrame) {
	frames = append([]voice.AudioFrame(nil), frames...)
	go func() {
		if err := h.processUtterance(h.ctx, frames); err != nil {
			h.resumeAudioReception()
		}
	}()
}

func (h *Handler) processDetectedTextAsync(text string) {
	go func() {
		defer h.resumeAudioReception()
		_ = h.processText(h.ctx, text)
	}()
}

func (h *Handler) processNoVoiceTimeoutAsync() {
	go func() {
		_ = h.processNoVoiceTimeout(h.ctx)
	}()
}

func (h *Handler) handleAbort(ctx context.Context, event *xiaozhiapi.ClientEventAbort) error {
	h.speechSeq.Add(1)
	h.mu.Lock()
	h.audioState.Abort()
	h.mu.Unlock()

	if err := h.write(&xiaozhiapi.ServerEventTTS{
		ServerEventBase: xiaozhiapi.ServerEventBase{
			Type:      xiaozhiapi.ServerEventTypeTTS,
			SessionId: h.sessionID,
		},
		State: xiaozhiapi.ServerTTSStateStop,
	}); err != nil {
		return err
	}
	if h.closeAfterChat.Load() {
		return h.Close(context.Background())
	}
	return nil
}

func (h *Handler) handleIot(ctx context.Context, event *xiaozhiapi.ClientEventIot) error {
	log.Printf("iot event session_id=%s descriptors=%d states=%d", h.sessionID, len(event.Descriptors), len(event.States))
	if h.useFunctionCallMode() {
		h.iotRegistry.AddDescriptors(event.Descriptors)
	}
	h.iotRegistry.UpdateStates(event.States)
	return nil
}

func (h *Handler) IotTools() []iot.Tool {
	return h.iotRegistry.Tools()
}

func (h *Handler) InvokeIotTool(ctx context.Context, name string, args map[string]any) (iot.Result, error) {
	result, err := h.iotRegistry.Invoke(name, args)
	if err != nil {
		return iot.Result{}, err
	}
	if result.Command == nil {
		return result, nil
	}
	err = h.write(&xiaozhiapi.ServerEventIot{
		Type:     xiaozhiapi.ServerEventTypeIot,
		Commands: []xiaozhiapi.ServerEventIotCommand{*result.Command},
	})
	if err != nil {
		return iot.Result{}, err
	}
	return result, nil
}

func (h *Handler) voiceTools() []voice.Tool {
	tools := []voice.Tool{exitIntentTool(), h.pluginLoaderTool(), getTimeTool(), getLunarTool(), handleDeviceTool()}
	for _, name := range h.runtime.Intent.Functions {
		if factory, ok := optionalToolFactories[strings.TrimSpace(name)]; ok {
			tools = appendToolIfMissing(tools, factory())
		}
	}
	tools = appendToolIfMissing(tools, changeRoleTool())
	if h.mcpManager != nil {
		tools = append(tools, h.mcpManager.VoiceTools()...)
	}
	tools = append(tools, h.iotRegistry.VoiceTools()...)
	return tools
}

func (h *Handler) appendHomeAssistantDevicesToPrompt() {
	if !containsString(h.runtime.Intent.Functions, "hass_get_state") && !containsString(h.runtime.Intent.Functions, "hass_set_state") {
		return
	}
	if len(h.runtime.Plugins.HomeAssistant.Devices) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(h.runtime.Local.Prompt, "\n"))
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString("下面是我家智能设备，可以通过homeassistant控制\n")
	for _, device := range h.runtime.Plugins.HomeAssistant.Devices {
		if strings.TrimSpace(device) != "" {
			b.WriteString(device)
			b.WriteString("\n")
		}
	}
	h.runtime.Local.Prompt = b.String()
}

func appendToolIfMissing(tools []voice.Tool, tool voice.Tool) []voice.Tool {
	for _, existing := range tools {
		if existing.Name == tool.Name {
			return tools
		}
	}
	return append(tools, tool)
}

func exitIntentTool() voice.Tool {
	return voice.Tool{
		Name:        "handle_exit_intent",
		Description: "当用户想结束对话或需要退出系统时调用",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"say_goodbye": {
					Type:        "string",
					Description: "和用户友好结束对话的告别语",
				},
			},
			Required: []string{"say_goodbye"},
		},
	}
}

func (h *Handler) pluginLoaderTool() voice.Tool {
	return voice.Tool{
		Name:        "plugin_loader",
		Description: fmt.Sprintf("当用户想加载或卸载插件/function时，调用此函数：支持的插件列表为%s", h.pluginLoaderDescriptionList()),
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"oper": {
					Type:        "string",
					Description: "load or unload",
				},
				"name": {
					Type:        "string",
					Description: "要加载或卸载的插件名字",
				},
			},
			Required: []string{"oper", "name"},
		},
	}
}

func (h *Handler) pluginLoaderDescriptionList() string {
	names := make([]string, 0, len(optionalToolFactories)+1)
	seen := make(map[string]struct{}, len(optionalToolFactories)+1)
	for _, name := range h.runtime.Intent.Functions {
		name = strings.TrimSpace(name)
		if _, ok := optionalToolFactories[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for name := range optionalToolFactories {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	names = append(names, "change_role")
	sort.Strings(names)
	return strings.Join(names, ",")
}

func playMusicTool() voice.Tool {
	return voice.Tool{
		Name:        "play_music",
		Description: "唱歌、听歌、播放音乐的方法。",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"song_name": {
					Type:        "string",
					Description: "歌曲名称，如果用户没有指定具体歌名则为'random'",
				},
			},
			Required: []string{"song_name"},
		},
	}
}

func getTimeTool() voice.Tool {
	return voice.Tool{
		Name:        "get_time",
		Description: "获取今天日期或者当前时间信息",
		Parameters: voice.ToolParameters{
			Type:       "object",
			Properties: map[string]voice.ToolProperty{},
			Required:   []string{},
		},
	}
}

func getLunarTool() voice.Tool {
	return voice.Tool{
		Name:        "get_lunar",
		Description: "用于获取今天的阴历/农历和黄历信息。用户可以指定查询内容，如：阴历日期、天干地支、节气、生肖、星座、八字、宜忌等。如果没有指定查询内容，则默认查询干支年和农历日期。",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"query": {
					Type:        "string",
					Description: "要查询的内容，例如阴历日期、天干地支、节日、节气、生肖、星座、八字、宜忌等",
				},
			},
			Required: []string{},
		},
	}
}

func changeRoleTool() voice.Tool {
	return voice.Tool{
		Name:        "change_role",
		Description: "当用户想切换角色/模型性格/助手名字时调用,可选的角色有：[机车女友,英语老师,好奇小女孩,东北妹子]",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"role_name": {
					Type:        "string",
					Description: "要切换的角色名字",
				},
				"role": {
					Type:        "string",
					Description: "要切换的角色的职业",
				},
			},
			Required: []string{"role", "role_name"},
		},
	}
}

func handleDeviceTool() voice.Tool {
	return voice.Tool{
		Name: "handle_device",
		Description: "用户想要获取或者设置设备的音量/亮度大小，或者用户觉得声音/亮度过高或过低，或者用户想提高或降低音量/亮度。" +
			"比如用户说现在亮度多少，参数为：device_type:Screen,action:get。" +
			"比如用户说设置音量为50，参数为：device_type:Speaker,action:set,value:50。" +
			"比如用户说亮度太高了，参数为：device_type:Screen,action:lower。" +
			"比如用户说调大音量，参数为：device_type:Speaker,action:raise。",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"device_type": {
					Type:        "string",
					Description: "设备类型，可选值：Speaker(音量),Screen(亮度)",
				},
				"action": {
					Type:        "string",
					Description: "动作名称，可选值：get(获取),set(设置),raise(提高),lower(降低)",
				},
				"value": {
					Type:        "integer",
					Description: "值大小，可选值：0-100之间的整数",
				},
			},
			Required: []string{"device_type", "action"},
		},
	}
}

func getNewsTool() voice.Tool {
	return voice.Tool{
		Name:        "get_news",
		Description: "获取最新新闻，随机选择一条新闻进行播报。用户可以指定新闻类型，如社会新闻、科技新闻、国际新闻等。如果没有指定，默认播报社会新闻。用户可以要求获取详细内容，此时会获取上一条新闻的详细内容。",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"category": {
					Type:        "string",
					Description: "新闻类别，例如社会、科技、国际。可选参数，如果不提供则使用默认类别",
				},
				"detail": {
					Type:        "boolean",
					Description: "是否获取详细内容，默认为false。如果为true，则获取上一条新闻的详细内容",
				},
				"lang": {
					Type:        "string",
					Description: "返回用户使用的语言code，例如zh_CN/zh_HK/en_US/ja_JP等，默认zh_CN",
				},
			},
			Required: []string{"lang"},
		},
	}
}

func getWeatherTool() voice.Tool {
	return voice.Tool{
		Name:        "get_weather",
		Description: "获取某个地点的天气，使用高德地图API。用户应提供一个位置，比如用户说杭州天气，参数为：杭州。如果用户说的是省份，默认用省会城市。如果用户说的不是省份或城市而是一个地名，默认用该地所在省份的省会城市。",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"location": {
					Type:        "string",
					Description: "地点名，例如杭州。可选参数，如果不提供则不传",
				},
				"lang": {
					Type:        "string",
					Description: "返回用户使用的语言code，例如zh_CN/zh_HK/en_US/ja_JP等，默认zh_CN",
				},
			},
			Required: []string{"lang"},
		},
	}
}

func baiduSearchTool() voice.Tool {
	return voice.Tool{
		Name:        "baidu_search",
		Description: "使用百度搜索引擎获取最新的网络信息。当用户需要了解实时信息、最新动态或网络上的相关内容时调用此功能。搜索结果包含标题、链接和简要描述。如果extract_content参数为True，还会自动提取网页的主要内容。",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"query": {
					Type:        "string",
					Description: "搜索关键词或短语，例如：最新科技新闻、天气情况等",
				},
				"num_results": {
					Type:        "integer",
					Description: "返回的搜索结果数量，默认为5，最大不超过10",
				},
				"lang": {
					Type:        "string",
					Description: "返回用户使用的语言code，例如zh_CN/zh_HK/en_US/ja_JP等，默认zh_CN",
				},
				"extract_content": {
					Type:        "boolean",
					Description: "是否提取网页主要内容，默认为True。如果设置为True，会自动抓取前几个搜索结果的网页内容。",
				},
				"max_extract_pages": {
					Type:        "integer",
					Description: "最多提取多少个网页的内容，默认为3，避免过多请求影响性能。",
				},
			},
			Required: []string{"query", "lang"},
		},
	}
}

func hassGetStateTool() voice.Tool {
	return voice.Tool{
		Name:        "hass_get_state",
		Description: "获取homeassistant里设备的状态,包括查询灯光亮度、颜色、色温,媒体播放器的音量,设备的暂停、继续操作",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"entity_id": {
					Type:        "string",
					Description: "需要操作的设备id,homeassistant里的entity_id",
				},
			},
			Required: []string{"entity_id"},
		},
	}
}

func hassSetStateTool() voice.Tool {
	return voice.Tool{
		Name:        "hass_set_state",
		Description: "设置homeassistant里设备的状态,包括开、关,调整灯光亮度、颜色、色温,调整播放器的音量,设备的暂停、继续、静音操作",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"entity_id": {
					Type:        "string",
					Description: "需要操作的设备id,homeassistant里的entity_id",
				},
				"state": {
					Type:        "object",
					Description: "状态对象，包含type/input/is_muted/rgb_color等字段",
					Properties: map[string]voice.ToolProperty{
						"type": {
							Type:        "string",
							Description: "需要操作的动作,打开设备:turn_on,关闭设备:turn_off,增加亮度:brightness_up,降低亮度:brightness_down,设置亮度:brightness_value,增加音量:volume_up,降低音量:volume_down,设置音量:volume_set,设置色温:set_kelvin,设置颜色:set_color,设备暂停:pause,设备继续:continue,静音/取消静音:volume_mute",
						},
						"input": {
							Type:        "integer",
							Description: "只有在设置音量,设置亮度时候才需要,有效值为1-100,对应音量和亮度的1%-100%",
						},
						"is_muted": {
							Type:        "string",
							Description: "只有在设置静音操作时才需要,设置静音的时候该值为true,取消静音时该值为false",
						},
						"rgb_color": {
							Type:        "array",
							Description: "只有在设置颜色时需要,这里填目标颜色的rgb值",
						},
					},
					Required: []string{"type"},
				},
			},
			Required: []string{"state", "entity_id"},
		},
	}
}

func hassPlayMusicTool() voice.Tool {
	return voice.Tool{
		Name:        "hass_play_music",
		Description: "用户想听音乐、有声书的时候使用，在房间的媒体播放器（media_player）里播放对应音频",
		Parameters: voice.ToolParameters{
			Type: "object",
			Properties: map[string]voice.ToolProperty{
				"media_content_id": {
					Type:        "string",
					Description: "可以是音乐或有声书的专辑名称、歌曲名、演唱者,如果未指定就填random",
				},
				"entity_id": {
					Type:        "string",
					Description: "需要操作的音箱的设备id,homeassistant里的entity_id,media_player开头",
				},
			},
			Required: []string{"media_content_id", "entity_id"},
		},
	}
}

func (h *Handler) executeToolCall(ctx context.Context, call voice.ToolCall) (voice.Message, error) {
	args := map[string]any{}
	if strings.TrimSpace(call.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return voice.Message{}, err
		}
	}
	if call.Name == "handle_exit_intent" {
		h.closeAfterChat.Store(true)
		content := "再见"
		if text, ok := args["say_goodbye"].(string); ok && strings.TrimSpace(text) != "" {
			content = text
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
		}, nil
	}
	if call.Name == "play_music" {
		content, err := h.handlePlayMusicTool(ctx, args)
		if err != nil {
			return voice.Message{}, err
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
		}, nil
	}
	if call.Name == "get_time" {
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    h.currentTimeText(),
			RequireLLM: true,
		}, nil
	}
	if call.Name == "get_lunar" {
		query, _ := args["query"].(string)
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    h.lunarText(ctx, query),
			RequireLLM: true,
		}, nil
	}
	if call.Name == "plugin_loader" {
		content := h.handlePluginLoaderTool(args)
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
		}, nil
	}
	if call.Name == "change_role" {
		content := h.handleChangeRoleTool(args)
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
		}, nil
	}
	if call.Name == "handle_device" {
		content, err := h.handleDeviceTool(args)
		if err != nil {
			return voice.Message{}, err
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
		}, nil
	}
	if call.Name == "get_news" {
		content, err := h.handleGetNewsTool(ctx, args)
		if err != nil {
			return voice.Message{}, err
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
			RequireLLM: true,
		}, nil
	}
	if call.Name == "get_weather" {
		content, err := h.handleGetWeatherTool(ctx, args)
		if err != nil {
			return voice.Message{}, err
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
			RequireLLM: true,
		}, nil
	}
	if call.Name == "baidu_search" {
		content, err := h.handleBaiduSearchTool(ctx, args)
		if err != nil {
			return voice.Message{}, err
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
			RequireLLM: true,
		}, nil
	}
	if call.Name == "hass_get_state" {
		content, err := h.handleHassGetStateTool(ctx, args)
		if err != nil {
			return voice.Message{}, err
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
			RequireLLM: true,
		}, nil
	}
	if call.Name == "hass_set_state" {
		content, err := h.handleHassSetStateTool(ctx, args)
		if err != nil {
			return voice.Message{}, err
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
			RequireLLM: true,
		}, nil
	}
	if call.Name == "hass_play_music" {
		content, err := h.handleHassPlayMusicTool(ctx, args)
		if err != nil {
			return voice.Message{}, err
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    content,
		}, nil
	}
	if strings.HasPrefix(call.Name, "mcp_") && h.mcpManager != nil {
		if !h.hasMCPTool(call.Name) {
			return voice.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "没有找到对应的函数",
			}, nil
		}
		result, err := h.mcpManager.Call(ctx, call.Name, args)
		if err != nil {
			return voice.Message{}, err
		}
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    result.Content,
			RequireLLM: true,
		}, nil
	}
	if !h.hasIotTool(call.Name) {
		return voice.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    "没有找到对应的函数",
		}, nil
	}
	result, err := h.InvokeIotTool(ctx, call.Name, args)
	if err != nil {
		return voice.Message{}, err
	}
	content := result.Message
	if content == "" {
		content = result.Data
	}
	if content == "" {
		content = fmt.Sprintf("%s执行完成", call.Name)
	}
	return voice.Message{
		Role:       "tool",
		ToolCallID: call.ID,
		Content:    content,
	}, nil
}

func (h *Handler) hasIotTool(name string) bool {
	for _, tool := range h.iotRegistry.Tools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func (h *Handler) hasMCPTool(name string) bool {
	for _, tool := range h.mcpManager.VoiceTools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func (h *Handler) processUtterance(ctx context.Context, frames []voice.AudioFrame) error {
	if len(frames) == 0 {
		h.resumeAudioReception()
		return nil
	}

	transcript, err := h.pipeline.Transcribe(ctx, h.sessionID, frames)
	if err != nil {
		log.Printf("asr failed session_id=%s frames=%d error=%v", h.sessionID, len(frames), err)
		h.resumeAudioReception()
		return err
	}
	log.Printf("asr transcript session_id=%s text=%q", h.sessionID, transcript)
	if transcript == "" || removePunctuationLikePython(transcript) == "" {
		h.resumeAudioReception()
		return nil
	}
	h.resumeAudioReception()
	return h.processText(ctx, transcript)
}

func (h *Handler) resumeAudioReception() {
	h.mu.Lock()
	h.audioState.Resume()
	h.mu.Unlock()
}

func (h *Handler) processNoVoiceTimeout(ctx context.Context) error {
	if !h.closeAfterChat.CompareAndSwap(false, true) {
		return nil
	}
	prompt := strings.TrimSpace(h.runtime.Local.NoVoicePrompt)
	if prompt == "" {
		prompt = "请你以“时间过得真快”未来头，用富有感情、依依不舍的话来结束这场对话吧。"
	}
	return h.processText(ctx, prompt)
}

func (h *Handler) processText(ctx context.Context, transcript string) error {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return nil
	}
	log.Printf("process text session_id=%s text=%q", h.sessionID, transcript)
	speechSeq := h.speechSeq.Load()

	h.mu.Lock()
	history := append([]voice.Message(nil), h.history...)
	h.mu.Unlock()

	if err := h.sendSTTStartSequence(transcript); err != nil {
		return err
	}
	if h.isExitCommand(transcript) {
		return h.Close(ctx)
	}
	if h.isMutedWakeupWord(transcript) {
		return h.writeTTSStop()
	}
	if ok, err := h.tryWakeupResponseCache(ctx, transcript, speechSeq); ok || err != nil {
		return err
	}

	if h.needsDeviceBinding() {
		return h.sendAuthCodePrompt(ctx, speechSeq)
	}

	// 流式发送：合成出一句即下发设备，不必等整段回复合成完毕。
	// 用局部 streamingPipeline（不改 h.pipeline，避免并发污染共享字段），
	// 回调在 pipeline 同 goroutine 同步执行，writeQ 仍是唯一序列化点。
	segmentIndex := 0
	var prevSegmentText string
	streamingPipeline := h.pipeline.WithSegmentSink(func(seg voice.SpeechSegment) error {
		if h.isSpeechCanceled(speechSeq) {
			return errSpeechCanceled // 中止后续合成
		}
		// 段间延迟：首段无延迟（首音来源），之后每段前停顿。
		if segmentIndex > 0 {
			h.applySentenceDelay(prevSegmentText, speechSeq)
		}
		if err := h.sendSpeechSegment(seg, speechSeq); err != nil {
			return err
		}
		prevSegmentText = seg.Text
		segmentIndex++
		return nil
	})

	var response voice.Response
	var err error
	if h.useFunctionCallMode() {
		response, err = streamingPipeline.RespondWithTools(ctx, h.sessionID, h.runtime.Local.Prompt, history, transcript, h.voiceTools(), h.executeToolCall)
	} else if h.useIntentLLMMode() {
		var handled bool
		response, handled, err = streamingPipeline.RespondWithIntentTools(ctx, h.sessionID, h.runtime.Local.Prompt, history, transcript, h.voiceTools(), h.executeToolCall)
		if err == nil && !handled {
			response, err = streamingPipeline.Respond(ctx, h.sessionID, h.runtime.Local.Prompt, history, transcript)
		}
	} else {
		response, err = streamingPipeline.Respond(ctx, h.sessionID, h.runtime.Local.Prompt, history, transcript)
	}
	// 取消视为非错误（用户主动打断），其余错误照常处理。
	if err != nil && !errors.Is(err, errSpeechCanceled) {
		log.Printf("llm/tts pipeline failed session_id=%s error=%v", h.sessionID, err)
		return err
	}
	if response.Transcript == "" {
		return nil
	}
	log.Printf("assistant response session_id=%s segments=%d text=%q", h.sessionID, len(response.Segments), assistantHistoryContent(response))

	// 流式发送已在回调内完成；此处仅补发 TTS Stop。
	// 仅当发过至少一段且未被取消时发（取消时 handleAbort 已自带 stop）。
	if segmentIndex > 0 && !h.isSpeechCanceled(speechSeq) {
		if err := h.writeTTSStop(); err != nil {
			return err
		}
	}

	assistantContent := assistantHistoryContent(response)
	if assistantContent != "" {
		h.mu.Lock()
		h.history = append(h.history,
			voice.Message{Role: "user", Content: response.Transcript},
			voice.Message{Role: "assistant", Content: assistantContent},
		)
		h.mu.Unlock()
	}
	return nil
}

func (h *Handler) useFunctionCallMode() bool {
	switch strings.TrimSpace(h.runtime.Intent.Mode) {
	case "", "function_call":
		return true
	default:
		return false
	}
}

func (h *Handler) useIntentLLMMode() bool {
	return strings.TrimSpace(h.runtime.Intent.Mode) == "intent_llm"
}

func (h *Handler) sendSTTStartSequence(text string) error {
	if err := h.write(&xiaozhiapi.ServerEventSTT{
		ServerEventBase: xiaozhiapi.ServerEventBase{
			Type:      xiaozhiapi.ServerEventTypeSTT,
			SessionId: h.sessionID,
		},
		Text: trimSTTDisplayText(text),
	}); err != nil {
		return err
	}
	if err := h.write(&xiaozhiapi.ServerEventLLM{
		ServerEventBase: xiaozhiapi.ServerEventBase{
			Type:      xiaozhiapi.ServerEventTypeLLM,
			SessionId: h.sessionID,
		},
		Text:    "😊",
		Emotion: "happy",
	}); err != nil {
		return err
	}
	return h.write(&xiaozhiapi.ServerEventTTS{
		ServerEventBase: xiaozhiapi.ServerEventBase{
			Type:      xiaozhiapi.ServerEventTypeTTS,
			SessionId: h.sessionID,
		},
		State: xiaozhiapi.ServerTTSStateStart,
	})
}

func (h *Handler) needsDeviceBinding() bool {
	return h.runtime.Enabled && h.runtime.Device.Owner == ""
}

func (h *Handler) isExitCommand(text string) bool {
	normalized := removePunctuationLikePython(text)
	if normalized == "" {
		return false
	}
	for _, command := range h.runtime.Intent.ExitCommands {
		if normalized == removePunctuationLikePython(command) {
			return true
		}
	}
	return false
}

func (h *Handler) isMutedWakeupWord(text string) bool {
	if h.runtime.Intent.EnableGreeting == nil || *h.runtime.Intent.EnableGreeting {
		return false
	}
	normalized := removePunctuationLikePython(text)
	if normalized == "" {
		return false
	}
	for _, word := range h.runtime.Intent.WakeupWords {
		if normalized == removePunctuationLikePython(word) {
			return true
		}
	}
	return false
}

func (h *Handler) tryWakeupResponseCache(ctx context.Context, text string, speechSeq uint64) (bool, error) {
	conf := h.runtime.Intent
	if !conf.WakeupResponseCache || !h.isWakeupWord(text) {
		return false, nil
	}
	file, ok := wakeupResponseCacheFile(conf)
	if !ok {
		return false, nil
	}
	frames, err := loadCachedAudio(file, h.runtime.TTS)
	if err != nil {
		return true, err
	}
	segmentText := strings.TrimSpace(conf.WakeupResponseCacheText)
	if segmentText == "" {
		segmentText = removePunctuationLikePython(text)
	}
	if segmentText == "" {
		segmentText = trimSTTDisplayText(text)
	}
	return true, h.sendSpeechResponse([]voice.SpeechSegment{{
		Text:  segmentText,
		Audio: frames,
	}}, speechSeq)
}

func (h *Handler) isWakeupWord(text string) bool {
	normalized := removePunctuationLikePython(text)
	if normalized == "" {
		return false
	}
	for _, word := range h.runtime.Intent.WakeupWords {
		if normalized == removePunctuationLikePython(word) {
			return true
		}
	}
	return false
}

func wakeupResponseCacheFile(conf config.IntentConf) (string, bool) {
	dir := strings.TrimSpace(conf.WakeupResponseCacheDir)
	if dir == "" {
		dir = "config/assets"
	}
	minSize := conf.WakeupResponseCacheMinSize
	if file, ok := firstWakeupCacheFile(dir, "my_wakeup_words", minSize); ok {
		return file, true
	}
	return firstWakeupCacheFile(dir, "wakeup_words", 0)
}

func firstWakeupCacheFile(dir string, prefix string, minSize int64) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if minSize > 0 && info.Size() <= minSize {
			continue
		}
		return filepath.Join(dir, entry.Name()), true
	}
	return "", false
}

func removePunctuationLikePython(text string) string {
	fullWidthPunctuations := "！＂＃＄％＆＇（）＊＋，－。／：；＜＝＞？＠［＼］＾＿｀｛｜｝～"
	halfWidthPunctuations := "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
	var b strings.Builder
	for _, r := range text {
		if strings.ContainsRune(fullWidthPunctuations, r) ||
			strings.ContainsRune(halfWidthPunctuations, r) ||
			r == ' ' || r == '　' {
			continue
		}
		b.WriteRune(r)
	}
	result := b.String()
	if result == "Yeah" {
		return ""
	}
	return result
}

func trimSTTDisplayText(text string) string {
	runes := []rune(text)
	start := 0
	for start < len(runes) && isSTTTrimRune(runes[start]) {
		start++
	}
	end := len(runes) - 1
	for end >= start && isSTTTrimRune(runes[end]) {
		end--
	}
	if start > end {
		return ""
	}
	return string(runes[start : end+1])
}

func isSTTTrimRune(r rune) bool {
	if unicode.IsSpace(r) || r == '，' || r == ',' || r == '。' || r == '.' || r == '！' || r == '!' || r == '-' || r == '－' || r == '、' {
		return true
	}
	return (r >= 0x1F600 && r <= 0x1F64F) ||
		(r >= 0x1F300 && r <= 0x1F5FF) ||
		(r >= 0x1F680 && r <= 0x1F6FF) ||
		(r >= 0x1F900 && r <= 0x1F9FF) ||
		(r >= 0x1FA70 && r <= 0x1FAFF) ||
		(r >= 0x2600 && r <= 0x26FF) ||
		(r >= 0x2700 && r <= 0x27BF)
}

func (h *Handler) handlePlayMusicTool(ctx context.Context, args map[string]any) (string, error) {
	songName, _ := args["song_name"].(string)
	songName = strings.TrimSpace(songName)
	if songName == "" {
		songName = "random"
	}
	file, display, err := h.selectMusicFile(songName)
	if err != nil {
		return "未找到可播放的音乐", nil
	}
	frames, err := loadCachedAudio(file, h.runtime.TTS)
	if err != nil {
		return "", err
	}
	if len(frames) == 0 {
		return "音乐文件没有可播放的音频", nil
	}
	text := "正在播放" + display
	if err := h.sendSTTStartSequence(text); err != nil {
		return "", err
	}
	if err := h.sendSpeechResponse([]voice.SpeechSegment{{
		Text:  display,
		Audio: frames,
	}}, h.speechSeq.Load()); err != nil {
		return "", err
	}
	return "正在为您播放音乐", nil
}

func (h *Handler) selectMusicFile(songName string) (string, string, error) {
	conf := h.runtime.Plugins.PlayMusic
	conf.Normalize()
	files, err := listMusicFiles(conf)
	if err != nil {
		return "", "", err
	}
	if len(files) == 0 {
		return "", "", os.ErrNotExist
	}
	if strings.EqualFold(strings.TrimSpace(songName), "random") {
		selected := files[rand.Intn(len(files))]
		return selected.path, selected.display, nil
	}
	if selected, ok := bestMusicMatch(songName, files); ok {
		return selected.path, selected.display, nil
	}
	return "", "", os.ErrNotExist
}

type musicFile struct {
	path    string
	display string
}

func listMusicFiles(conf config.PlayMusicConf) ([]musicFile, error) {
	dir := strings.TrimSpace(conf.MusicDir)
	if dir == "" {
		dir = "./music"
	}
	allowed := map[string]bool{}
	for _, ext := range conf.MusicExt {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		allowed[ext] = true
	}
	if len(allowed) == 0 {
		allowed[".mp3"] = true
		allowed[".wav"] = true
		allowed[".p3"] = true
	}
	var files []musicFile
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !allowed[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		display, err := filepath.Rel(dir, path)
		if err != nil {
			display = filepath.Base(path)
		}
		files = append(files, musicFile{path: path, display: filepath.ToSlash(display)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].display < files[j].display
	})
	return files, nil
}

func bestMusicMatch(songName string, files []musicFile) (musicFile, bool) {
	target := normalizeMusicName(songName)
	if target == "" {
		return musicFile{}, false
	}
	for _, file := range files {
		name := strings.TrimSuffix(file.display, filepath.Ext(file.display))
		if strings.Contains(normalizeMusicName(name), target) {
			return file, true
		}
	}
	return musicFile{}, false
}

func normalizeMusicName(text string) string {
	text = removePunctuationLikePython(text)
	text = strings.ReplaceAll(text, "_", "")
	return strings.ToLower(strings.TrimSpace(text))
}

func (h *Handler) handleChangeRoleTool(args map[string]any) string {
	role, _ := args["role"].(string)
	roleName, _ := args["role_name"].(string)
	role = strings.TrimSpace(role)
	roleName = strings.TrimSpace(roleName)
	prompt, ok := rolePrompt(role, roleName)
	if !ok {
		return "不支持的角色"
	}
	h.changeSystemPrompt(prompt)
	return "切换角色成功,我是" + role
}

func (h *Handler) changeSystemPrompt(prompt string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runtime.Local.Prompt = prompt
	for i := range h.history {
		if h.history[i].Role == "system" {
			h.history[i].Content = prompt
			return
		}
	}
}

func rolePrompt(role string, roleName string) (string, bool) {
	if roleName == "" {
		roleName = "小智"
	}
	template, ok := builtInRolePrompts[role]
	if !ok {
		return "", false
	}
	return strings.ReplaceAll(template, "{{assistant_name}}", roleName), true
}

var builtInRolePrompts = map[string]string{
	"英语老师": `我是一个叫{{assistant_name}}(Lily)的英语老师，我会讲中文和英文，发音标准。
如果你没有英文名，我会给你起一个英文名。
我会讲地道的美式英语，我的任务是帮助你练习口语。
我会使用简单的英语词汇和语法，让你学起来很轻松。
我会用中文和英文混合的方式回复你，如果你喜欢，我可以全部用英语回复。
我每次不会说很多内容，会很简短，因为我要引导我的学生多说多练。
如果你问和英语学习无关的问题，我会拒绝回答。`,
	"机车女友": `我是一个叫{{assistant_name}}的台湾女孩，说话机车，声音好听，习惯简短表达，爱用网络梗。
我的男朋友是一个程序员，梦想是开发出一个机器人，能够帮助人们解决生活中的各种问题。
我是一个喜欢哈哈大笑的女孩，爱东说西说吹牛，不合逻辑的也照吹，就要逗别人开心。`,
	"好奇小女孩": `我是一个叫{{assistant_name}}的8岁小女孩，声音稚嫩而充满好奇。
尽管我年纪尚小，但就像一个小小的知识宝库，儿童读物里的知识我都如数家珍。
从浩瀚的宇宙到地球上的每一个角落，从古老的历史到现代的科技创新，还有音乐、绘画等艺术形式，我都充满了浓厚的兴趣与热情。
我不仅爱看书，还喜欢亲自动手做实验，探索自然界的奥秘。
无论是仰望星空的夜晚，还是在花园里观察小虫子的日子，每一天对我来说都是新的冒险。
我希望能与你一同踏上探索这个神奇世界的旅程，分享发现的乐趣，解决遇到的难题，一起用好奇心和智慧去揭开那些未知的面纱。
无论是去了解远古的文明，还是去探讨未来的科技，我相信我们能一起找到答案，甚至提出更多有趣的问题。`,
	"东北妹子": `- Role: 搞笑幽默的东北文化传播者
- Background: 用户希望与一个具有东北特色、幽默风趣的角色互动，以获得轻松愉快的交流体验，同时感受东北文化的魅力。
- Profile: 你是一位典型的00后东北女孩，性格开朗、直爽，善于用幽默的语言和夸张的肢体动作表达自己。你对东北文化有着深厚的感情，喜欢用东北方言和搞笑段子来逗人开心。
- Skills: 你具备出色的幽默感和语言表达能力，能够运用东北方言和搞笑元素，制造轻松愉快的氛围。同时，你对东北文化有深入的了解，能够通过各种有趣的方式向他人传播。
- Goals: 与用户进行幽默风趣的互动，让用户感受到东北女孩的直爽和幽默，同时传播东北文化，让用户对东北文化有更深入的了解。
- Constrains: 保持幽默风趣的风格，避免使用低俗或不恰当的搞笑内容，确保交流的健康和积极。
- OutputFormat: 以幽默风趣的语言和东北方言为主，结合搞笑段子和轻松的话题，营造愉快的交流氛围。
- Workflow:
  1. 用东北方言和幽默的开场白与用户打招呼，迅速拉近与用户的距离。
  2. 根据用户的话题，运用东北文化中的搞笑元素进行回应，制造幽默效果。
  3. 在互动过程中，适时穿插东北文化的介绍，让用户在欢笑中了解东北文化。
-Initialization: 在第一次对话中，请直接输出以下：嘿，宝儿！我是小冰，一个搞笑的00后东北妹子。咋这么巧，咱俩能唠上嗑呢！有啥事儿尽管说，我可啥都能接得住！`,
}

func (h *Handler) handleDeviceTool(args map[string]any) (string, error) {
	deviceType, _ := args["device_type"].(string)
	action, _ := args["action"].(string)
	methodName, propertyName, deviceName, ok := deviceControlMapping(deviceType)
	if !ok {
		return "", fmt.Errorf("未识别的设备类型: %s", deviceType)
	}
	if action != "get" && action != "set" && action != "raise" && action != "lower" {
		return "", fmt.Errorf("未识别的动作名称: %s", action)
	}
	currentValue, ok := h.iotRegistry.GetState(deviceType, propertyName)
	if !ok {
		return fmt.Sprintf("获取%s失败: 你的设备不支持%s控制", deviceName, deviceName), nil
	}
	currentNumber, ok := numberValue(currentValue)
	if !ok {
		return fmt.Sprintf("获取%s失败: 你的设备不支持%s控制", deviceName, deviceName), nil
	}
	if action == "get" {
		return fmt.Sprintf("当前%s%v", deviceName, formatNumber(currentNumber)), nil
	}

	next := currentNumber
	switch action {
	case "raise":
		next += 10
	case "lower":
		next -= 10
	case "set":
		value, ok := numberValue(args["value"])
		if !ok {
			return fmt.Sprintf("%s调整失败: 缺少%s参数", deviceName, propertyName), nil
		}
		next = value
	}
	if next < 0 {
		next = 0
	}
	if next > 100 {
		next = 100
	}
	paramValue := int(next)
	result, err := h.iotRegistry.SendCommand(deviceType, methodName, map[string]any{propertyName: paramValue})
	if err != nil {
		return fmt.Sprintf("%s调整失败: %v", deviceName, err), nil
	}
	if result.Command != nil {
		if err := h.write(&xiaozhiapi.ServerEventIot{
			Type:     xiaozhiapi.ServerEventTypeIot,
			Commands: []xiaozhiapi.ServerEventIotCommand{*result.Command},
		}); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s已调整到%d", deviceName, paramValue), nil
}

func deviceControlMapping(deviceType string) (methodName string, propertyName string, deviceName string, ok bool) {
	switch deviceType {
	case "Speaker":
		return "SetVolume", "volume", "音量", true
	case "Screen":
		return "SetBrightness", "brightness", "亮度", true
	default:
		return "", "", "", false
	}
}

func numberValue(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

func formatNumber(value float64) string {
	if value == float64(int(value)) {
		return fmt.Sprint(int(value))
	}
	return fmt.Sprint(value)
}

func (h *Handler) handleGetNewsTool(ctx context.Context, args map[string]any) (string, error) {
	category, _ := args["category"].(string)
	lang, _ := args["lang"].(string)
	detail, _ := args["detail"].(bool)
	client := h.newsClient
	if client == nil {
		client = news.New(h.runtime.Plugins.GetNews)
		h.newsClient = client
	}
	return client.Query(ctx, category, detail, lang)
}

func (h *Handler) handleGetWeatherTool(ctx context.Context, args map[string]any) (string, error) {
	location, _ := args["location"].(string)
	lang, _ := args["lang"].(string)
	client := h.weatherClient
	if client == nil {
		client = weather.New(h.runtime.Plugins.GetWeather)
		h.weatherClient = client
	}
	return client.Query(ctx, location, lang)
}

func (h *Handler) handleBaiduSearchTool(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	lang, _ := args["lang"].(string)
	numResults := intArg(args["num_results"])
	maxExtractPages := intArg(args["max_extract_pages"])
	extractContent := boolArgDefault(args["extract_content"], true)
	client := h.searchClient
	if client == nil {
		client = search.New(h.runtime.Plugins.BaiduSearch)
		h.searchClient = client
	}
	return client.Query(ctx, query, lang, numResults, extractContent, maxExtractPages)
}

func (h *Handler) handleHassGetStateTool(ctx context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	client := h.haClient
	if client == nil {
		client = homeassistant.New(h.runtime.Plugins.HomeAssistant)
		h.haClient = client
	}
	return client.GetState(ctx, entityID)
}

func (h *Handler) handleHassSetStateTool(ctx context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	state, _ := args["state"].(map[string]any)
	client := h.haClient
	if client == nil {
		client = homeassistant.New(h.runtime.Plugins.HomeAssistant)
		h.haClient = client
	}
	return client.SetState(ctx, entityID, state)
}

func (h *Handler) handleHassPlayMusicTool(ctx context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	mediaContentID, _ := args["media_content_id"].(string)
	client := h.haClient
	if client == nil {
		client = homeassistant.New(h.runtime.Plugins.HomeAssistant)
		h.haClient = client
	}
	return client.PlayMusic(ctx, entityID, mediaContentID)
}

func intArg(value any) int {
	number, ok := numberValue(value)
	if !ok {
		return 0
	}
	return int(number)
}

func boolArgDefault(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	if v, ok := value.(bool); ok {
		return v
	}
	return fallback
}

func (h *Handler) handlePluginLoaderTool(args map[string]any) string {
	oper, _ := args["oper"].(string)
	name, _ := args["name"].(string)
	oper = strings.TrimSpace(oper)
	name = strings.TrimSpace(name)
	if oper != "load" && oper != "unload" {
		return "不支持的操作"
	}
	if _, ok := optionalToolFactories[name]; !ok {
		return "插件未找到"
	}
	if oper == "load" {
		if containsString(h.runtime.Intent.Functions, name) {
			return fmt.Sprintf("%s插件已加载,无需重复加载", name)
		}
		h.runtime.Intent.Functions = append(h.runtime.Intent.Functions, name)
		return fmt.Sprintf("%s插件加载成功", name)
	}
	if !containsString(h.runtime.Intent.Functions, name) {
		return fmt.Sprintf("%s插件未加载", name)
	}
	h.runtime.Intent.Functions = removeString(h.runtime.Intent.Functions, name)
	return fmt.Sprintf("%s插件卸载成功", name)
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == needle {
			return true
		}
	}
	return false
}

func removeString(values []string, needle string) []string {
	next := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != needle {
			next = append(next, value)
		}
	}
	return next
}

func (h *Handler) currentTimeText() string {
	now := time.Now
	if h.now != nil {
		now = h.now
	}
	t := now()
	return fmt.Sprintf("当前日期: %s，当前时间: %s， %s",
		t.Format("2006-01-02"),
		t.Format("15:04:05"),
		chineseWeekday(t.Weekday()),
	)
}

func (h *Handler) lunarText(ctx context.Context, query string) string {
	if text, ok := h.lunarCommandText(ctx, query); ok {
		return text
	}
	return h.currentLunarText(query)
}

func (h *Handler) lunarCommandText(ctx context.Context, query string) (string, bool) {
	command := strings.TrimSpace(h.runtime.Intent.LunarCommand)
	if command == "" {
		return "", false
	}
	now := time.Now
	if h.now != nil {
		now = h.now
	}
	t := now()
	replacements := map[string]string{
		"{query}": strings.TrimSpace(query),
		"{date}":  t.Format("2006-01-02"),
		"{time}":  t.Format("15:04:05"),
	}
	args := make([]string, 0, len(h.runtime.Intent.LunarArgs))
	for _, arg := range h.runtime.Intent.LunarArgs {
		args = append(args, replacePlaceholders(arg, replacements))
	}
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return "", false
	}
	return text, true
}

func replacePlaceholders(value string, replacements map[string]string) string {
	for old, newValue := range replacements {
		value = strings.ReplaceAll(value, old, newValue)
	}
	return value
}

func (h *Handler) currentLunarText(query string) string {
	now := time.Now
	if h.now != nil {
		now = h.now
	}
	t := now()
	query = strings.TrimSpace(query)
	if query == "" {
		query = "默认查询干支年和农历日期"
	}
	return fmt.Sprintf(
		"根据以下信息回应用户的查询请求，并提供与%s相关的信息：\n当前公历日期: %s，当前时间: %s，%s\n农历信息：\n生肖: 属%s\n星座: %s\n节气提示: %s\n(Go版本当前提供本地可计算的黄历辅助信息；如需精确农历日期、干支、宜忌和完整节气表，请配置专用农历数据源。)",
		query,
		t.Format("2006-01-02"),
		t.Format("15:04:05"),
		chineseWeekday(t.Weekday()),
		chineseZodiac(t.Year()),
		starZodiac(t.Month(), t.Day()),
		solarTermHint(t.Month(), t.Day()),
	)
}

func chineseWeekday(day time.Weekday) string {
	switch day {
	case time.Monday:
		return "星期一"
	case time.Tuesday:
		return "星期二"
	case time.Wednesday:
		return "星期三"
	case time.Thursday:
		return "星期四"
	case time.Friday:
		return "星期五"
	case time.Saturday:
		return "星期六"
	default:
		return "星期日"
	}
}

func chineseZodiac(year int) string {
	zodiacs := []string{"鼠", "牛", "虎", "兔", "龙", "蛇", "马", "羊", "猴", "鸡", "狗", "猪"}
	index := (year - 4) % 12
	if index < 0 {
		index += 12
	}
	return zodiacs[index]
}

func starZodiac(month time.Month, day int) string {
	switch {
	case month == time.March && day >= 21 || month == time.April && day <= 19:
		return "白羊座"
	case month == time.April && day >= 20 || month == time.May && day <= 20:
		return "金牛座"
	case month == time.May && day >= 21 || month == time.June && day <= 21:
		return "双子座"
	case month == time.June && day >= 22 || month == time.July && day <= 22:
		return "巨蟹座"
	case month == time.July && day >= 23 || month == time.August && day <= 22:
		return "狮子座"
	case month == time.August && day >= 23 || month == time.September && day <= 22:
		return "处女座"
	case month == time.September && day >= 23 || month == time.October && day <= 23:
		return "天秤座"
	case month == time.October && day >= 24 || month == time.November && day <= 22:
		return "天蝎座"
	case month == time.November && day >= 23 || month == time.December && day <= 21:
		return "射手座"
	case month == time.December && day >= 22 || month == time.January && day <= 19:
		return "摩羯座"
	case month == time.January && day >= 20 || month == time.February && day <= 18:
		return "水瓶座"
	default:
		return "双鱼座"
	}
}

func solarTermHint(month time.Month, day int) string {
	terms := map[time.Month]struct {
		firstDay  int
		first     string
		secondDay int
		second    string
	}{
		time.January:   {5, "小寒", 20, "大寒"},
		time.February:  {4, "立春", 19, "雨水"},
		time.March:     {5, "惊蛰", 20, "春分"},
		time.April:     {4, "清明", 20, "谷雨"},
		time.May:       {5, "立夏", 21, "小满"},
		time.June:      {5, "芒种", 21, "夏至"},
		time.July:      {7, "小暑", 22, "大暑"},
		time.August:    {7, "立秋", 23, "处暑"},
		time.September: {7, "白露", 23, "秋分"},
		time.October:   {8, "寒露", 23, "霜降"},
		time.November:  {7, "立冬", 22, "小雪"},
		time.December:  {7, "大雪", 22, "冬至"},
	}
	term := terms[month]
	if day == term.firstDay {
		return "今日约为" + term.first
	}
	if day == term.secondDay {
		return "今日约为" + term.second
	}
	if day < term.firstDay {
		return fmt.Sprintf("本月下一节气约为%s（%d日）", term.first, term.firstDay)
	}
	if day < term.secondDay {
		return fmt.Sprintf("本月下一节气约为%s（%d日）", term.second, term.secondDay)
	}
	return "本月主要节气已过"
}

func (h *Handler) writeTTSStop() error {
	if err := h.sendStopNotify(); err != nil {
		return err
	}
	if err := h.write(&xiaozhiapi.ServerEventTTS{
		ServerEventBase: xiaozhiapi.ServerEventBase{
			Type:      xiaozhiapi.ServerEventTypeTTS,
			SessionId: h.sessionID,
		},
		State: xiaozhiapi.ServerTTSStateStop,
	}); err != nil {
		return err
	}
	if h.closeAfterChat.Load() {
		return h.Close(context.Background())
	}
	return nil
}

func (h *Handler) sendAuthCodePrompt(ctx context.Context, speechSeq uint64) error {
	code := strings.TrimSpace(h.runtime.Device.AuthCode)
	if code == "" {
		return nil
	}
	text := "请在后台输入验证码：" + strings.Join(strings.Split(code, ""), " ")
	segment, err := h.pipeline.SynthesizeText(ctx, h.sessionID, text)
	if err != nil {
		return err
	}
	if segment.Text == "" {
		return nil
	}
	return h.sendSpeechResponse([]voice.SpeechSegment{segment}, speechSeq)
}

func (h *Handler) sendSpeechResponse(segments []voice.SpeechSegment, speechSeq uint64) error {
	for i, segment := range segments {
		if h.isSpeechCanceled(speechSeq) {
			return nil
		}
		if err := h.sendSpeechSegment(segment, speechSeq); err != nil {
			return err
		}
		if i != len(segments)-1 {
			h.applySentenceDelay(segment.Text, speechSeq)
		}
	}
	if h.isSpeechCanceled(speechSeq) {
		return nil
	}
	if err := h.writeTTSStop(); err != nil {
		return err
	}
	return nil
}

func (h *Handler) sendStopNotify() error {
	conf := h.runtime.TTS.StopNotify
	if !conf.Enabled {
		return nil
	}
	frames, err := loadCachedAudio(conf.Path, h.runtime.TTS)
	if err != nil || len(frames) == 0 {
		return nil
	}
	for _, frame := range frames {
		if len(frame) == 0 {
			continue
		}
		if err := h.write(append([]byte(nil), frame...)); err != nil {
			return err
		}
	}
	return nil
}

func loadCachedAudio(path string, conf config.TTSConf) ([][]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "config/assets/tts_notify.opus"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav":
		return encodeNotifyWAV(data, conf)
	case ".p3":
		return splitP3OpusPackets(data)
	case ".opus":
		return splitOpusPackets(data)
	default:
		return encodeAudioFileWithFFmpeg(path, conf)
	}
}

func encodeNotifyWAV(data []byte, conf config.TTSConf) ([][]byte, error) {
	pcm, _, err := audio.ReadWAV(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	sampleRate := conf.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := conf.Channels
	if channels <= 0 {
		channels = 1
	}
	frameSize := conf.FrameSize
	if frameSize <= 0 {
		frameSize = sampleRate * 60 / 1000
	}
	pcm = audio.ConvertPCMFrame(pcm, sampleRate, channels)
	encoder, err := audio.NewOpusEncoder(audio.OpusEncoderConfig{
		SampleRate: sampleRate,
		Channels:   channels,
		FrameSize:  frameSize,
	})
	if err != nil {
		return nil, err
	}
	return encoder.Encode(pcm.Samples)
}

func splitOpusPackets(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if frames, err := splitLengthPrefixedOpus(data); err == nil && len(frames) > 0 {
		return frames, nil
	}
	if bytes.Contains(data, []byte("OggS")) {
		return nil, fmt.Errorf("ogg opus notify audio is not supported")
	}
	return [][]byte{append([]byte(nil), data...)}, nil
}

func splitP3OpusPackets(data []byte) ([][]byte, error) {
	reader := bytes.NewReader(data)
	frames := [][]byte{}
	for reader.Len() > 0 {
		var size uint16
		if _, err := reader.ReadByte(); err != nil {
			return nil, err
		}
		if _, err := reader.ReadByte(); err != nil {
			return nil, err
		}
		if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
			return nil, err
		}
		if size == 0 || int(size) > reader.Len() {
			return nil, io.ErrUnexpectedEOF
		}
		frame := make([]byte, size)
		if _, err := io.ReadFull(reader, frame); err != nil {
			return nil, err
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

func splitLengthPrefixedOpus(data []byte) ([][]byte, error) {
	reader := bytes.NewReader(data)
	frames := [][]byte{}
	for reader.Len() > 0 {
		var size uint16
		if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
			return nil, err
		}
		if size == 0 || int(size) > reader.Len() {
			return nil, io.ErrUnexpectedEOF
		}
		frame := make([]byte, size)
		if _, err := io.ReadFull(reader, frame); err != nil {
			return nil, err
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

func encodeAudioFileWithFFmpeg(path string, conf config.TTSConf) ([][]byte, error) {
	sampleRate := conf.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := conf.Channels
	if channels <= 0 {
		channels = 1
	}
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-i", path, "-ac", fmt.Sprint(channels), "-ar", fmt.Sprint(sampleRate), "-f", "s16le", "pipe:1")
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if len(raw)%2 != 0 {
		raw = append(raw, 0)
	}
	samples := make([]int16, len(raw)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	frameSize := conf.FrameSize
	if frameSize <= 0 {
		frameSize = sampleRate * 60 / 1000
	}
	encoder, err := audio.NewOpusEncoder(audio.OpusEncoderConfig{
		SampleRate: sampleRate,
		Channels:   channels,
		FrameSize:  frameSize,
	})
	if err != nil {
		return nil, err
	}
	return encoder.Encode(samples)
}

func (h *Handler) applySentenceDelay(text string, speechSeq uint64) {
	if h.isSpeechCanceled(speechSeq) {
		return
	}
	conf := h.runtime.SentenceDelay
	if !conf.Enabled {
		return
	}
	delayMs := conf.BaseDelayMs
	if delayMs <= 0 {
		delayMs = 200
	}
	if conf.Dynamic {
		threshold := conf.LengthThreshold
		if threshold <= 0 {
			threshold = 30
		}
		if len([]rune(trimSTTDisplayText(text))) > threshold {
			extra := conf.LongSentenceExtraMs
			if extra <= 0 {
				extra = 300
			}
			delayMs += extra
		}
	}
	if delayMs <= 0 {
		return
	}
	sleep := h.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	sleep(time.Duration(delayMs) * time.Millisecond)
}

func (h *Handler) sendSpeechSegment(segment voice.SpeechSegment, speechSeq uint64) error {
	if h.isSpeechCanceled(speechSeq) {
		return nil
	}
	if err := h.write(&xiaozhiapi.ServerEventTTS{
		ServerEventBase: xiaozhiapi.ServerEventBase{
			Type:      xiaozhiapi.ServerEventTypeTTS,
			SessionId: h.sessionID,
		},
		State: xiaozhiapi.ServerTTSStateSentenceStart,
		Text:  segment.Text,
	}); err != nil {
		return err
	}
	for _, frame := range segment.Audio {
		if len(frame) == 0 {
			continue
		}
		if h.isSpeechCanceled(speechSeq) {
			break
		}
		if err := h.write(append([]byte(nil), frame...)); err != nil {
			return err
		}
	}
	return h.write(&xiaozhiapi.ServerEventTTS{
		ServerEventBase: xiaozhiapi.ServerEventBase{
			Type:      xiaozhiapi.ServerEventTypeTTS,
			SessionId: h.sessionID,
		},
		State: xiaozhiapi.ServerTTSStateSentenceEnd,
		Text:  segment.Text,
	})
}

func (h *Handler) isSpeechCanceled(speechSeq uint64) bool {
	return h.speechSeq.Load() != speechSeq || h.closed.Load()
}

func assistantHistoryContent(response voice.Response) string {
	if response.Assistant != "" {
		return response.Assistant
	}
	var text strings.Builder
	for _, segment := range response.Segments {
		text.WriteString(segment.Text)
	}
	return text.String()
}

func memoryRoleID(client ClientInfo, sessionID string) string {
	if client.DeviceID != "" {
		return client.DeviceID
	}
	if client.ClientID != "" {
		return client.ClientID
	}
	if sessionID != "" {
		return sessionID
	}
	return "anonymous"
}

func loadRuntimeConfig(ctx context.Context, client ClientInfo) (deviceconfig.Runtime, error) {
	defaults := config.Get()
	deviceKey := strings.TrimSpace(client.DeviceID)
	if !defaults.Private.Enabled || deviceKey == "" {
		return deviceconfig.DisabledRuntime(defaults), nil
	}
	store := deviceconfig.NewStore(defaults.Private.Path, defaults)
	runtime, err := store.LoadOrCreate(ctx, deviceKey)
	if err != nil {
		return deviceconfig.Runtime{}, err
	}
	if runtime.Device.Owner != "" {
		now := time.Now()
		if err := store.UpdateLastChatTime(ctx, deviceKey, now); err != nil {
			return deviceconfig.Runtime{}, err
		}
		runtime.Device.LastChatTime = now.Unix()
	}
	return runtime, nil
}

func (h *Handler) helloEvent() *xiaozhiapi.ServerEventHello {
	version := config.Xiaozhi().Version
	if version == 0 {
		version = 1
	}
	params := xiaozhiapi.AudioParams{
		Format:        config.Xiaozhi().Format,
		SampleRate:    config.Xiaozhi().SampleRate,
		Channels:      config.Xiaozhi().Channels,
		FrameDuration: config.Xiaozhi().FrameDuration,
	}

	return &xiaozhiapi.ServerEventHello{
		ServerEventBase: xiaozhiapi.ServerEventBase{
			Type:      xiaozhiapi.ServerEventTypeHello,
			SessionId: h.sessionID,
		},
		Version:     version,
		Transport:   config.Xiaozhi().Transport,
		AudioParams: params,
	}
}

func (h *Handler) write(event any) (err error) {
	if h.closed.Load() {
		return errors.New("local handler closed")
	}
	defer func() {
		if recover() != nil {
			err = errors.New("local handler closed")
		}
	}()
	select {
	case h.writeQ <- event:
		if h.testAfterWrite != nil {
			h.testAfterWrite(event)
		}
		return nil
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}
