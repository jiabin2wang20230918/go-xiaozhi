package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

func init() {
	if err := loadConfig(); err != nil {
		panic(fmt.Sprintf("Failed to load configuration: %v", err))
	}
}

var (
	conf            BizConf
	configFileUsed  string
	runtimeWarnings []error
)

type XiaozhiConf struct {
	Type          string              `yaml:"type"`
	Version       int                 `yaml:"version"`
	Format        string              `yaml:"format"`
	Transport     string              `yaml:"transport"`
	SampleRate    int                 `yaml:"sample_rate"`
	Channels      int                 `yaml:"channels"`
	FrameDuration int                 `yaml:"frame_duration"`
	AudioParams   *XiaozhiAudioParams `yaml:"audio_params"`
}

type XiaozhiAudioParams struct {
	Format        string `yaml:"format"`
	SampleRate    int    `yaml:"sample_rate"`
	Channels      int    `yaml:"channels"`
	FrameDuration int    `yaml:"frame_duration"`
}

type LocalProviderConf struct {
	Prompt          string `yaml:"prompt"`
	WelcomeMessage  string `yaml:"welcome_message"`
	EchoTranscripts bool   `yaml:"echo_transcripts"`
	NoVoicePrompt   string `yaml:"no_voice_prompt"`
}

type ServerConf struct {
	IP   string   `yaml:"ip"`
	Port int      `yaml:"port"`
	Auth AuthConf `yaml:"auth"`
}

type AuthConf struct {
	Enabled        bool        `yaml:"enabled"`
	Tokens         []AuthToken `yaml:"tokens"`
	AllowedDevices []string    `yaml:"allowed_devices"`
}

type AuthToken struct {
	Token string `yaml:"token"`
	Name  string `yaml:"name"`
}

type AudioSessionConf struct {
	MinFrames           int     `yaml:"min_frames"`
	PreBufferFrames     int     `yaml:"pre_buffer_frames"`
	NoVoiceCloseSeconds int     `yaml:"no_voice_close_seconds"`
	SilenceStopMs       int     `yaml:"silence_stop_ms"`
	OpusSampleRate      int     `yaml:"opus_sample_rate"`
	OpusChannels        int     `yaml:"opus_channels"`
	OpusFrameSize       int     `yaml:"opus_frame_size"`
	OpusGain            float32 `yaml:"opus_gain"`
	VAD                 VADConf `yaml:"vad"`
}

type AudioFlowConf struct {
	Enabled            bool    `yaml:"enabled"`
	PreBufferFrames    int     `yaml:"pre_buffer_frames"`
	FrameDurationMs    int     `yaml:"frame_duration_ms"`
	SendRateMultiplier float64 `yaml:"send_rate_multiplier"`
	MaxDelayMs         int     `yaml:"max_delay_ms"`
}

type SentenceDelayConf struct {
	Enabled             bool `yaml:"enabled"`
	BaseDelayMs         int  `yaml:"base_delay_ms"`
	Dynamic             bool `yaml:"dynamic"`
	LengthThreshold     int  `yaml:"length_threshold"`
	LongSentenceExtraMs int  `yaml:"long_sentence_extra_ms"`
	// StreamingGapMs 是"流式发送"路径下分段之间的间隔（ms）。
	// 流式边合成边发送，本就有天然合成间隙，再叠加批量路径的完整段间延迟
	// （base + 长句额外）会让短句多的回复听起来断断续续。流式路径只用这个
	// 较小（或 0）的固定间隔。<=0 表示流式段间无延迟。仅影响本地 TTS 流式合成。
	StreamingGapMs int `yaml:"streaming_gap_ms"`
}

type VADConf struct {
	Type                 string            `yaml:"type"`
	EnergyThreshold      float64           `yaml:"energy_threshold"`
	Threshold            float64           `yaml:"threshold"`
	Command              string            `yaml:"command"`
	Args                 []string          `yaml:"args"`
	Env                  map[string]string `yaml:"env"`
	OutputDir            string            `yaml:"output_dir"`
	SampleRate           int               `yaml:"sample_rate"`
	Channels             int               `yaml:"channels"`
	ModelDir             string            `yaml:"model_dir"`
	MinSilenceDurationMs int               `yaml:"min_silence_duration_ms"`
	MaxSpeechDurationS   float64           `yaml:"max_speech_duration_s"`
	NumThreads           int               `yaml:"num_threads"`
}

type ASRConf struct {
	Type           string            `yaml:"type"`
	APIURL         string            `yaml:"api_url"`
	APIKey         string            `yaml:"api_key"`
	Model          string            `yaml:"model"`
	Language       string            `yaml:"language"`
	Prompt         string            `yaml:"prompt"`
	ResponseFormat string            `yaml:"response_format"`
	Command        string            `yaml:"command"`
	Args           []string          `yaml:"args"`
	Env            map[string]string `yaml:"env"`
	OutputDir      string            `yaml:"output_dir"`
	DeleteAudio    bool              `yaml:"delete_audio"`
	StubTranscript string            `yaml:"stub_transcript"`
	SampleRate     int               `yaml:"sample_rate"`
	Channels       int               `yaml:"channels"`
	ModelDir       string            `yaml:"model_dir"`
	NumThreads     int               `yaml:"num_threads"`
}

type TTSConf struct {
	Type           string            `yaml:"type"`
	APIURL         string            `yaml:"api_url"`
	APIKey         string            `yaml:"api_key"`
	Params         map[string]any    `yaml:"params"`
	Headers        map[string]string `yaml:"headers"`
	Format         string            `yaml:"format"`
	Command        string            `yaml:"command"`
	Args           []string          `yaml:"args"`
	Env            map[string]string `yaml:"env"`
	OutputDir      string            `yaml:"output_dir"`
	DeleteAudio    *bool             `yaml:"delete_audio,omitempty"`
	Model          string            `yaml:"model"`
	Voice          string            `yaml:"voice"`
	ResponseFormat string            `yaml:"response_format"`
	Speed          float64           `yaml:"speed"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	SampleRate     int               `yaml:"sample_rate"`
	Channels       int               `yaml:"channels"`
	FrameSize      int               `yaml:"frame_size"`
	DurationMs     int               `yaml:"duration_ms"`
	Frequency      float64           `yaml:"frequency"`
	Amplitude      int16             `yaml:"amplitude"`
	LegacyURL      string            `yaml:"url"`
	StopNotify     NotifyConf        `yaml:"stop_notify"`
	// 本地 sherpa-onnx Kokoro TTS 相关字段。
	ModelDir     string  `yaml:"model_dir"`
	Voices       string  `yaml:"voices"`
	Lexicon      string  `yaml:"lexicon"`
	DataDir      string  `yaml:"data_dir"`
	Lang         string  `yaml:"lang"`
	Sid          int     `yaml:"sid"`
	SilenceScale float64 `yaml:"silence_scale"`
	NumThreads   int     `yaml:"num_threads"`
	SplitMaxChars int    `yaml:"split_max_chars"`
}

type NotifyConf struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type LLMConf struct {
	Type            string      `yaml:"type"`
	BaseURL         string      `yaml:"base_url"`
	APIKey          string      `yaml:"api_key"`
	Model           string      `yaml:"model"`
	MaxTokens       int         `yaml:"max_tokens"`
	Echo            EchoLLMConf `yaml:"echo"`
	LegacyURL       string      `yaml:"url"`
	LegacyModelName string      `yaml:"model_name"`
}

type MemoryConf struct {
	Type   string `yaml:"type"`
	Path   string `yaml:"path"`
	MaxLen int    `yaml:"max_len"`
}

type PrivateConfigConf struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type IntentConf struct {
	ExitCommands               []string `yaml:"exit_commands"`
	WakeupWords                []string `yaml:"wakeup_words"`
	EnableGreeting             *bool    `yaml:"enable_greeting"`
	WakeupResponseCache        bool     `yaml:"wakeup_response_cache"`
	WakeupResponseCacheDir     string   `yaml:"wakeup_response_cache_dir"`
	WakeupResponseCacheText    string   `yaml:"wakeup_response_cache_text"`
	WakeupResponseCacheMinSize int64    `yaml:"wakeup_response_cache_min_size"`
	Mode                       string   `yaml:"mode"`
	Functions                  []string `yaml:"functions"`
	LunarCommand               string   `yaml:"lunar_command"`
	LunarArgs                  []string `yaml:"lunar_args"`
}

type LegacyIntentModuleConf struct {
	Type      string   `yaml:"type"`
	LLM       string   `yaml:"llm"`
	Functions []string `yaml:"functions"`
}

type DevicePrivateConf struct {
	SelectedModule map[string]string                 `yaml:"selected_module"`
	Prompt         string                            `yaml:"prompt"`
	Nickname       string                            `yaml:"nickname"`
	Owner          string                            `yaml:"owner"`
	AuthCode       string                            `yaml:"auth_code"`
	LastChatTime   int64                             `yaml:"last_chat_time"`
	Local          *LocalProviderConf                `yaml:"local"`
	LLM            *LLMConf                          `yaml:"llm"`
	TTS            *TTSConf                          `yaml:"tts"`
	ASR            *ASRConf                          `yaml:"asr"`
	Session        *AudioSessionConf                 `yaml:"session"`
	Intent         *IntentConf                       `yaml:"intent"`
	LegacyLLM      map[string]LLMConf                `yaml:"LLM"`
	LegacyTTS      map[string]TTSConf                `yaml:"TTS"`
	LegacyASR      map[string]ASRConf                `yaml:"ASR"`
	LegacyVAD      map[string]VADConf                `yaml:"VAD"`
	LegacyIntent   map[string]LegacyIntentModuleConf `yaml:"Intent"`
}

type EchoLLMConf struct {
	WelcomeMessage  string `yaml:"welcome_message"`
	EchoTranscripts bool   `yaml:"echo_transcripts"`
}

type PluginsConf struct {
	PlayMusic     PlayMusicConf     `yaml:"play_music"`
	GetNews       GetNewsConf       `yaml:"get_news"`
	GetWeather    GetWeatherConf    `yaml:"get_weather"`
	BaiduSearch   BaiduSearchConf   `yaml:"baidu_search"`
	HomeAssistant HomeAssistantConf `yaml:"home_assistant"`
}

type PlayMusicConf struct {
	MusicDir    string   `yaml:"music_dir"`
	MusicExt    []string `yaml:"music_ext"`
	RefreshTime int      `yaml:"refresh_time"`
}

type GetNewsConf struct {
	DefaultRSSURL string            `yaml:"default_rss_url"`
	CategoryURLs  map[string]string `yaml:"category_urls"`
}

type GetWeatherConf struct {
	APIKey          string `yaml:"api_key"`
	DefaultLocation string `yaml:"default_location"`
	BaseURL         string `yaml:"base_url"`
}

type BaiduSearchConf struct {
	Enabled          bool   `yaml:"enabled"`
	SearchURL        string `yaml:"search_url"`
	MaxResults       int    `yaml:"max_results"`
	MaxExtractPages  int    `yaml:"max_extract_pages"`
	MaxContentLength int    `yaml:"max_content_length"`
}

type HomeAssistantConf struct {
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Devices []string `yaml:"devices"`
}

type MCPConf struct {
	Enabled bool                     `yaml:"enabled"`
	Path    string                   `yaml:"path"`
	Servers map[string]MCPServerConf `yaml:"servers"`
}

type MCPServerConf struct {
	Command string            `yaml:"command" json:"command"`
	Args    []string          `yaml:"args" json:"args"`
	Env     map[string]string `yaml:"env" json:"env"`
}

type BizConf struct {
	Server        ServerConf        `yaml:"server"`
	Xiaozhi       XiaozhiConf       `yaml:"xiaozhi"`
	Local         LocalProviderConf `yaml:"local"`
	Session       AudioSessionConf  `yaml:"session"`
	AudioFlow     AudioFlowConf     `yaml:"audio_flow"`
	SentenceDelay SentenceDelayConf `yaml:"sentence_delay"`
	ASR           ASRConf           `yaml:"asr"`
	LLM           LLMConf           `yaml:"llm"`
	Memory        MemoryConf        `yaml:"memory"`
	Intent        IntentConf        `yaml:"intent"`
	Private       PrivateConfigConf `yaml:"private_config"`
	TTS           TTSConf           `yaml:"tts"`
	Plugins       PluginsConf       `yaml:"plugins"`
	MCP           MCPConf           `yaml:"mcp"`
	Audio         struct {
		InputFormat  string `yaml:"input_format"`
		OutputFormat string `yaml:"output_format"`
		SampleRate   int    `yaml:"sample_rate"`
		Channels     int    `yaml:"channels"`
		MaxDuration  int    `yaml:"max_duration"`
	} `yaml:"audio"`
	DefaultParams struct {
		ChatCompletions struct {
			FrequencyPenalty *float32 `yaml:"frequency_penalty"`
			Temperature      *float32 `yaml:"temperature"`
			TopP             *float32 `yaml:"top_p"`
		} `yaml:"chat_completions"`
	} `yaml:"default_params"`

	LegacySelectedModule                 map[string]string                 `yaml:"-"`
	LegacyLLM                            map[string]LLMConf                `yaml:"-"`
	LegacyTTS                            map[string]TTSConf                `yaml:"-"`
	LegacyASR                            map[string]ASRConf                `yaml:"-"`
	LegacyVAD                            map[string]VADConf                `yaml:"-"`
	LegacyMemory                         map[string]MemoryConf             `yaml:"-"`
	LegacyIntent                         map[string]LegacyIntentModuleConf `yaml:"-"`
	LegacyCMDExit                        []string                          `yaml:"CMD_exit"`
	LegacyPrompt                         string                            `yaml:"prompt"`
	LegacyWakeupWords                    []string                          `yaml:"wakeup_words"`
	LegacyEnableGreeting                 *bool                             `yaml:"enable_greeting"`
	LegacyEnableWakeupWordsResponseCache *bool                             `yaml:"enable_wakeup_words_response_cache"`
	LegacyEnableStopTTSNotify            *bool                             `yaml:"enable_stop_tts_notify"`
	LegacyStopTTSNotifyVoice             string                            `yaml:"stop_tts_notify_voice"`
	LegacyDeleteAudio                    *bool                             `yaml:"delete_audio"`
	LegacyTTSTimeout                     int                               `yaml:"tts_timeout"`
	LegacyCloseConnectionNoVoiceTime     int                               `yaml:"close_connection_no_voice_time"`
	LegacyUsePrivateConfig               *bool                             `yaml:"use_private_config"`
	LegacyEnableSmartAudioFlowControl    *bool                             `yaml:"enable_smart_audio_flow_control"`
	LegacyAudioSendRateMultiplier        float64                           `yaml:"audio_send_rate_multiplier"`
	LegacySentenceIntervalDelay          int                               `yaml:"sentence_interval_delay"`
	LegacyEnableDynamicSentenceDelay     *bool                             `yaml:"enable_dynamic_sentence_delay"`
	LegacySentenceLengthThreshold        int                               `yaml:"sentence_length_threshold"`
	LegacyLongSentenceExtraDelay         int                               `yaml:"long_sentence_extra_delay"`
}

func Get() *BizConf {
	return &conf
}

func Replace(c BizConf) func() {
	old := conf
	conf = c
	return func() {
		conf = old
	}
}

func Xiaozhi() *XiaozhiConf {
	return &conf.Xiaozhi
}

func Server() *ServerConf {
	return &conf.Server
}

func loadConfig() error {
	v := viper.New()
	v.SetConfigType("yaml")
	if configFile := explicitConfigPathFromArgs(os.Args); configFile != "" {
		v.SetConfigFile(configFile)
	} else if configFile, ok := firstExistingConfigFile(configSearchFiles()); ok {
		v.SetConfigFile(configFile)
	} else {
		v.SetConfigName("biz")
		v.AddConfigPath("conf")
		v.AddConfigPath(".")
	}

	v.AutomaticEnv()
	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var next BizConf
	if decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		WeaklyTypedInput: true,
		Result:           &next,
		TagName:          "yaml",
	}); err != nil {
		return fmt.Errorf("failed to create decoder: %w", err)
	} else if err = decoder.Decode(v.AllSettings()); err != nil {
		return fmt.Errorf("failed to decode config: %w", err)
	}
	if err := next.loadLegacyProviderTables(v.ConfigFileUsed()); err != nil {
		return err
	}
	if err := checkPrivateConfigFreshness(v.ConfigFileUsed()); err != nil {
		return err
	}

	next.LoadEnv(v)
	next.Normalize()
	if err := next.Validate(); err != nil {
		return err
	}
	warnings := next.EnsureRuntimeDirectories(v.ConfigFileUsed(), os.MkdirAll)
	conf = next
	configFileUsed = v.ConfigFileUsed()
	runtimeWarnings = warnings

	v.WatchConfig()
	return nil
}

func explicitConfigPathFromArgs(args []string) string {
	for i := 1; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "--config_path" {
			if i+1 >= len(args) {
				return ""
			}
			return strings.TrimSpace(args[i+1])
		}
		if value, ok := strings.CutPrefix(arg, "--config_path="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func configSearchFiles() []string {
	files := []string{
		filepath.Join("data", ".config.yaml"),
		filepath.Join("conf", "biz.yaml"),
		"biz.yaml",
		"config.yaml",
	}
	if repoRoot, ok := repoRootFromCaller(); ok {
		files = append(files,
			filepath.Join(repoRoot, "data", ".config.yaml"),
			filepath.Join(repoRoot, "conf", "biz.yaml"),
			filepath.Join(repoRoot, "config.yaml"),
		)
	}
	return files
}

func repoRootFromCaller() (string, bool) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..")), true
}

func firstExistingConfigFile(files []string) (string, bool) {
	for _, file := range files {
		if strings.TrimSpace(file) == "" {
			continue
		}
		info, err := os.Stat(file)
		if err == nil && !info.IsDir() {
			return file, true
		}
	}
	return "", false
}

func checkPrivateConfigFreshness(configFile string) error {
	configFile = strings.TrimSpace(configFile)
	if filepath.Base(configFile) != ".config.yaml" || filepath.Base(filepath.Dir(configFile)) != "data" {
		return nil
	}
	defaultConfig, ok := defaultConfigForPrivateConfig(configFile)
	if !ok {
		return nil
	}
	currentData, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read private config for compatibility check: %w", err)
	}
	defaultData, err := os.ReadFile(defaultConfig)
	if err != nil {
		return fmt.Errorf("failed to read default config for compatibility check: %w", err)
	}
	var current, defaults map[string]any
	if err := yaml.Unmarshal(currentData, &current); err != nil {
		return fmt.Errorf("failed to decode private config for compatibility check: %w", err)
	}
	if err := yaml.Unmarshal(defaultData, &defaults); err != nil {
		return fmt.Errorf("failed to decode default config for compatibility check: %w", err)
	}
	missing := missingConfigKeys(defaults, current, "")
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("private config is too old and is missing keys:\n- %s\nplease back up data/.config.yaml, refresh it from config.yaml, and copy your secrets into the new file", strings.Join(missing, "\n- "))
}

func defaultConfigForPrivateConfig(privateConfig string) (string, bool) {
	baseDir := runtimeBaseDir(privateConfig)
	candidate := filepath.Join(baseDir, "config.yaml")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, true
	}
	if repoRoot, ok := repoRootFromCaller(); ok {
		candidate = filepath.Join(repoRoot, "config.yaml")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func missingConfigKeys(defaults map[string]any, current map[string]any, parent string) []string {
	missing := make([]string, 0)
	for key, defaultValue := range defaults {
		fullKey := key
		if parent != "" {
			fullKey = parent + "." + key
		}
		currentValue, ok := current[key]
		if !ok {
			missing = append(missing, fullKey)
			continue
		}
		defaultMap, ok := defaultValue.(map[string]any)
		if !ok {
			continue
		}
		currentMap, ok := currentValue.(map[string]any)
		if !ok {
			continue
		}
		missing = append(missing, missingConfigKeys(defaultMap, currentMap, fullKey)...)
	}
	return missing
}

func GetConfigFilePath() string {
	return configFileUsed
}

func RuntimeWarnings() []error {
	return append([]error(nil), runtimeWarnings...)
}

func ReloadConfig() error {
	return loadConfig()
}

// EnsureRuntimeDirectories creates Python-compatible runtime output directories.
func (c *BizConf) EnsureRuntimeDirectories(configFile string, mkdirAll func(string, os.FileMode) error) []error {
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	var warnings []error
	for _, dir := range c.runtimeDirectories(configFile) {
		if err := mkdirAll(dir, 0o755); err != nil {
			warnings = append(warnings, fmt.Errorf("create runtime directory %q: %w", dir, err))
		}
	}
	return warnings
}

func (c *BizConf) runtimeDirectories(configFile string) []string {
	baseDir := runtimeBaseDir(configFile)
	dirs := make([]string, 0, 8)
	seen := map[string]struct{}{}
	addDir := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(baseDir, dir)
		}
		dir = filepath.Clean(dir)
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}

	addDir("tmp")
	addDir(c.ASR.OutputDir)
	addDir(c.TTS.OutputDir)
	addDir(c.Session.VAD.OutputDir)
	for _, provider := range c.LegacyASR {
		addDir(provider.OutputDir)
	}
	for _, provider := range c.LegacyTTS {
		addDir(provider.OutputDir)
	}
	for _, provider := range c.LegacyVAD {
		addDir(provider.OutputDir)
	}
	return dirs
}

func runtimeBaseDir(configFile string) string {
	configFile = strings.TrimSpace(configFile)
	if configFile == "" {
		return "."
	}
	dir := filepath.Clean(filepath.Dir(configFile))
	switch filepath.Base(dir) {
	case "conf", "data":
		return filepath.Clean(filepath.Dir(dir))
	default:
		return dir
	}
}

func (c *BizConf) LoadEnv(v *viper.Viper) {
}

func (c *BizConf) loadLegacyProviderTables(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read legacy provider config: %w", err)
	}
	var legacy struct {
		SelectedModule map[string]string                 `yaml:"selected_module"`
		LLM            map[string]LLMConf                `yaml:"LLM"`
		TTS            map[string]TTSConf                `yaml:"TTS"`
		ASR            map[string]ASRConf                `yaml:"ASR"`
		VAD            map[string]VADConf                `yaml:"VAD"`
		Memory         map[string]MemoryConf             `yaml:"Memory"`
		Intent         map[string]LegacyIntentModuleConf `yaml:"Intent"`
	}
	if err := yaml.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("failed to decode legacy provider config: %w", err)
	}
	c.LegacySelectedModule = legacy.SelectedModule
	c.LegacyLLM = legacy.LLM
	c.LegacyTTS = legacy.TTS
	c.LegacyASR = legacy.ASR
	c.LegacyVAD = legacy.VAD
	c.LegacyMemory = legacy.Memory
	c.LegacyIntent = legacy.Intent
	return nil
}

func (c *BizConf) Normalize() {
	if params := c.Xiaozhi.AudioParams; params != nil {
		if c.Xiaozhi.Format == "" {
			c.Xiaozhi.Format = params.Format
		}
		if c.Xiaozhi.SampleRate == 0 {
			c.Xiaozhi.SampleRate = params.SampleRate
		}
		if c.Xiaozhi.Channels == 0 {
			c.Xiaozhi.Channels = params.Channels
		}
		if c.Xiaozhi.FrameDuration == 0 {
			c.Xiaozhi.FrameDuration = params.FrameDuration
		}
	}
	if strings.TrimSpace(c.Local.Prompt) == "" && strings.TrimSpace(c.LegacyPrompt) != "" {
		c.Local.Prompt = c.LegacyPrompt
	}
	if len(c.Intent.ExitCommands) == 0 {
		c.Intent.ExitCommands = append([]string(nil), c.LegacyCMDExit...)
	}
	if len(c.Intent.WakeupWords) == 0 {
		c.Intent.WakeupWords = append([]string(nil), c.LegacyWakeupWords...)
	}
	if c.LegacyEnableGreeting != nil && c.Intent.EnableGreeting == nil {
		enabled := *c.LegacyEnableGreeting
		c.Intent.EnableGreeting = &enabled
	}
	if c.LegacyEnableWakeupWordsResponseCache != nil {
		c.Intent.WakeupResponseCache = *c.LegacyEnableWakeupWordsResponseCache
	}
	if c.LegacyEnableStopTTSNotify != nil {
		c.TTS.StopNotify.Enabled = *c.LegacyEnableStopTTSNotify
	}
	if c.TTS.StopNotify.Path == "" && strings.TrimSpace(c.LegacyStopTTSNotifyVoice) != "" {
		c.TTS.StopNotify.Path = c.LegacyStopTTSNotifyVoice
	}
	if c.LegacyTTSTimeout > 0 && c.TTS.TimeoutSeconds == 0 {
		c.TTS.TimeoutSeconds = c.LegacyTTSTimeout
	}
	if c.Intent.WakeupResponseCacheDir == "" {
		c.Intent.WakeupResponseCacheDir = "config/assets"
	}
	if c.Intent.WakeupResponseCacheMinSize == 0 {
		c.Intent.WakeupResponseCacheMinSize = 15 * 1024
	}
	if c.LegacyCloseConnectionNoVoiceTime > 0 && c.Session.NoVoiceCloseSeconds == 0 {
		c.Session.NoVoiceCloseSeconds = c.LegacyCloseConnectionNoVoiceTime
	}
	if c.LegacyUsePrivateConfig != nil {
		c.Private.Enabled = *c.LegacyUsePrivateConfig
	}
	if c.LegacyEnableSmartAudioFlowControl != nil {
		c.AudioFlow.Enabled = *c.LegacyEnableSmartAudioFlowControl
	}
	if c.LegacyAudioSendRateMultiplier > 0 && c.AudioFlow.SendRateMultiplier == 0 {
		c.AudioFlow.SendRateMultiplier = c.LegacyAudioSendRateMultiplier
	}
	if c.LegacySentenceIntervalDelay > 0 && c.SentenceDelay.BaseDelayMs == 0 {
		c.SentenceDelay.BaseDelayMs = c.LegacySentenceIntervalDelay
	}
	if c.LegacyEnableDynamicSentenceDelay != nil {
		c.SentenceDelay.Dynamic = *c.LegacyEnableDynamicSentenceDelay
	}
	if c.LegacySentenceLengthThreshold > 0 && c.SentenceDelay.LengthThreshold == 0 {
		c.SentenceDelay.LengthThreshold = c.LegacySentenceLengthThreshold
	}
	if c.LegacyLongSentenceExtraDelay > 0 && c.SentenceDelay.LongSentenceExtraMs == 0 {
		c.SentenceDelay.LongSentenceExtraMs = c.LegacyLongSentenceExtraDelay
	}
	c.applyLegacySelectedModules()
	if c.LegacyDeleteAudio != nil {
		c.ASR.DeleteAudio = *c.LegacyDeleteAudio
		if c.TTS.DeleteAudio == nil {
			deleteAudio := *c.LegacyDeleteAudio
			c.TTS.DeleteAudio = &deleteAudio
		}
	}
	c.Memory.Normalize()
	c.LLM.Normalize()
	c.TTS.Normalize()
	c.Plugins.PlayMusic.Normalize()
	c.Plugins.GetNews.Normalize()
	c.Plugins.GetWeather.Normalize()
	c.Plugins.BaiduSearch.Normalize()
	c.MCP.Normalize()
}

func (c *BizConf) applyLegacySelectedModules() {
	if len(c.LegacySelectedModule) == 0 {
		return
	}
	if name := strings.TrimSpace(c.LegacySelectedModule["LLM"]); name != "" && shouldUseLegacyLLM(c.LLM) {
		if candidate, ok := c.LegacyLLM[name]; ok {
			if inferLegacyLLMType(name, &candidate) {
				candidate.Normalize()
				if isSupportedLLM(candidate) {
					c.LLM = candidate
				}
			}
		}
	}
	if name := strings.TrimSpace(c.LegacySelectedModule["TTS"]); name != "" && shouldUseLegacyTTS(c.TTS) {
		if candidate, ok := c.LegacyTTS[name]; ok {
			if inferLegacyTTSType(name, &candidate) {
				candidate.Normalize()
				if isSupportedTTS(candidate) {
					c.TTS = candidate
				}
			}
		}
	}
	if name := strings.TrimSpace(c.LegacySelectedModule["ASR"]); name != "" && shouldUseLegacyASR(c.ASR) {
		if candidate, ok := c.LegacyASR[name]; ok {
			if inferLegacyASRType(name, &candidate) {
				if isSupportedASR(candidate) {
					c.ASR = candidate
				}
			}
		}
	}
	if name := strings.TrimSpace(c.LegacySelectedModule["VAD"]); name != "" && shouldUseLegacyVAD(c.Session.VAD) {
		if candidate, ok := c.LegacyVAD[name]; ok {
			if inferLegacyVADType(name, &candidate) {
				if isSupportedVAD(candidate) {
					c.Session.VAD = candidate
				}
			}
		}
	}
	if name := strings.TrimSpace(c.LegacySelectedModule["Memory"]); name != "" && shouldUseLegacyMemory(c.Memory) {
		if candidate, ok := c.LegacyMemory[name]; ok {
			candidate.Normalize()
			if isSupportedMemory(candidate) {
				c.Memory = candidate
			}
		}
	}
	if name := strings.TrimSpace(c.LegacySelectedModule["Intent"]); name != "" {
		if strings.TrimSpace(c.Intent.Mode) == "" {
			c.Intent.Mode = name
		}
		if len(c.Intent.Functions) == 0 {
			if candidate, ok := c.LegacyIntent[name]; ok && len(candidate.Functions) > 0 {
				c.Intent.Functions = append([]string(nil), candidate.Functions...)
			} else if candidate, ok := c.LegacyIntent["function_call"]; ok && len(candidate.Functions) > 0 {
				c.Intent.Functions = append([]string(nil), candidate.Functions...)
			}
		}
	}
}

// ApplyLegacySelectedProviders applies Python-style selected_module provider tables.
func ApplyLegacySelectedProviders(conf *BizConf) {
	if conf == nil {
		return
	}
	conf.applyLegacySelectedModules()
	conf.LLM.Normalize()
	conf.TTS.Normalize()
}

// ApplyLegacySelectedProvidersOverride applies selected_module provider tables over existing providers.
func ApplyLegacySelectedProvidersOverride(conf *BizConf) {
	if conf == nil || len(conf.LegacySelectedModule) == 0 {
		return
	}
	if name := strings.TrimSpace(conf.LegacySelectedModule["LLM"]); name != "" {
		if candidate, ok := conf.LegacyLLM[name]; ok {
			if inferLegacyLLMType(name, &candidate) {
				candidate.Normalize()
				if isSupportedLLM(candidate) {
					conf.LLM = candidate
				}
			}
		}
	}
	if name := strings.TrimSpace(conf.LegacySelectedModule["TTS"]); name != "" {
		if candidate, ok := conf.LegacyTTS[name]; ok {
			if inferLegacyTTSType(name, &candidate) {
				candidate.Normalize()
				if isSupportedTTS(candidate) {
					conf.TTS = candidate
				}
			}
		}
	}
	if name := strings.TrimSpace(conf.LegacySelectedModule["ASR"]); name != "" {
		if candidate, ok := conf.LegacyASR[name]; ok {
			if inferLegacyASRType(name, &candidate) {
				if isSupportedASR(candidate) {
					conf.ASR = candidate
				}
			}
		}
	}
	if name := strings.TrimSpace(conf.LegacySelectedModule["VAD"]); name != "" {
		if candidate, ok := conf.LegacyVAD[name]; ok {
			if inferLegacyVADType(name, &candidate) {
				if isSupportedVAD(candidate) {
					conf.Session.VAD = candidate
				}
			}
		}
	}
	if name := strings.TrimSpace(conf.LegacySelectedModule["Intent"]); name != "" {
		conf.Intent.Mode = name
		conf.Intent.Functions = nil
		if candidate, ok := conf.LegacyIntent[name]; ok && len(candidate.Functions) > 0 {
			conf.Intent.Functions = append([]string(nil), candidate.Functions...)
		} else if candidate, ok := conf.LegacyIntent["function_call"]; ok && len(candidate.Functions) > 0 {
			conf.Intent.Functions = append([]string(nil), candidate.Functions...)
		}
	}
	conf.LLM.Normalize()
	conf.TTS.Normalize()
}

func inferLegacyLLMType(name string, conf *LLMConf) bool {
	provider := strings.TrimSpace(conf.Type)
	if provider != "" {
		return true
	}
	lowerName := strings.ToLower(name)
	switch {
	case strings.Contains(lowerName, "ollama"):
		conf.Type = "ollama"
	case strings.Contains(lowerName, "xinference"):
		conf.Type = "xinference"
	case strings.Contains(lowerName, "openai") || isKnownOpenAICompatibleLegacyLLM(lowerName):
		conf.Type = "openai"
	case strings.Contains(lowerName, "echo"):
		conf.Type = "echo"
	default:
		return false
	}
	return true
}

func isKnownOpenAICompatibleLegacyLLM(lowerName string) bool {
	for _, marker := range []string{
		"alillm",
		"chatglm",
		"deepseek",
		"doubao",
		"lmstudio",
		"openrouter",
		"qwen",
	} {
		if strings.Contains(lowerName, marker) {
			return true
		}
	}
	return false
}

func inferLegacyTTSType(name string, conf *TTSConf) bool {
	provider := strings.TrimSpace(conf.Type)
	if provider != "" {
		return true
	}
	lowerName := strings.ToLower(name)
	if isUnsupportedLegacyTTS(lowerName) {
		return false
	}
	switch {
	case strings.TrimSpace(conf.Command) != "" || strings.Contains(lowerName, "command"):
		conf.Type = "command"
	case strings.Contains(lowerName, "custom") || strings.TrimSpace(conf.LegacyURL) != "" && len(conf.Params) > 0:
		conf.Type = "custom"
	case strings.Contains(lowerName, "openai") || strings.TrimSpace(conf.Model) != "" || strings.TrimSpace(conf.Voice) != "":
		conf.Type = "openai"
	case strings.Contains(lowerName, "kokoro"):
		conf.Type = "kokoro"
	case strings.Contains(lowerName, "stub"):
		conf.Type = "stub"
	default:
		return false
	}
	return true
}

func isUnsupportedLegacyTTS(lowerName string) bool {
	for _, marker := range []string{
		"edge",
		"fishspeech",
		"gizwits",
		"minimax",
		"volcengine",
	} {
		if strings.Contains(lowerName, marker) {
			return true
		}
	}
	return false
}

func inferLegacyASRType(name string, conf *ASRConf) bool {
	provider := strings.TrimSpace(conf.Type)
	if provider != "" {
		return true
	}
	lowerName := strings.ToLower(name)
	if isUnsupportedLegacyASR(lowerName) {
		return false
	}
	switch {
	case strings.TrimSpace(conf.Command) != "" || strings.Contains(lowerName, "command"):
		conf.Type = "command"
	case strings.Contains(lowerName, "openai") || strings.TrimSpace(conf.Model) != "" || strings.TrimSpace(conf.APIURL) != "":
		conf.Type = "openai"
	case strings.Contains(lowerName, "file_stub") || strings.Contains(lowerName, "stub"):
		conf.Type = "file_stub"
	default:
		return false
	}
	return true
}

func isUnsupportedLegacyASR(lowerName string) bool {
	for _, marker := range []string{
		"funasr",
		"vosk",
	} {
		if strings.Contains(lowerName, marker) && !strings.Contains(lowerName, "command") {
			return true
		}
	}
	return false
}

func inferLegacyVADType(name string, conf *VADConf) bool {
	provider := strings.TrimSpace(conf.Type)
	if provider != "" {
		return true
	}
	lowerName := strings.ToLower(name)
	switch {
	case strings.TrimSpace(conf.Command) != "" || strings.Contains(lowerName, "command"):
		conf.Type = "command"
	case strings.Contains(lowerName, "silero") && strings.TrimSpace(conf.ModelDir) != "":
		conf.Type = "silero"
	case strings.Contains(lowerName, "non_empty"):
		conf.Type = "non_empty"
	case strings.Contains(lowerName, "energy") || conf.EnergyThreshold > 0:
		conf.Type = "energy"
	default:
		return false
	}
	return true
}

func shouldUseLegacyLLM(conf LLMConf) bool {
	provider := strings.TrimSpace(conf.Type)
	return provider == "" || provider == "echo"
}

func isSupportedLLM(conf LLMConf) bool {
	switch strings.TrimSpace(conf.Type) {
	case "", "echo":
		return true
	case "openai":
		return strings.TrimSpace(conf.BaseURL) != "" && strings.TrimSpace(conf.Model) != ""
	default:
		return false
	}
}

func shouldUseLegacyTTS(conf TTSConf) bool {
	provider := strings.TrimSpace(conf.Type)
	return provider == "" || provider == "stub"
}

func shouldUseLegacyASR(conf ASRConf) bool {
	provider := strings.TrimSpace(conf.Type)
	return provider == "" || provider == "file_stub"
}

func shouldUseLegacyVAD(conf VADConf) bool {
	provider := strings.TrimSpace(conf.Type)
	return provider == "" || (provider == "energy" && conf.EnergyThreshold == 0)
}

func isSupportedASR(conf ASRConf) bool {
	switch strings.TrimSpace(conf.Type) {
	case "", "file_stub":
		return true
	case "openai":
		return strings.TrimSpace(conf.Model) != ""
	case "command":
		return strings.TrimSpace(conf.Command) != ""
	case "sherpa_sensevoice":
		return strings.TrimSpace(conf.ModelDir) != ""
	default:
		return false
	}
}

func isSupportedTTS(conf TTSConf) bool {
	switch strings.TrimSpace(conf.Type) {
	case "", "stub":
		return true
	case "openai":
		return strings.TrimSpace(conf.Model) != "" && strings.TrimSpace(conf.Voice) != ""
	case "custom":
		return strings.TrimSpace(conf.LegacyURL) != ""
	case "command":
		return strings.TrimSpace(conf.Command) != ""
	case "kokoro":
		return strings.TrimSpace(conf.ModelDir) != ""
	default:
		return false
	}
}

func isSupportedVAD(conf VADConf) bool {
	switch strings.TrimSpace(conf.Type) {
	case "", "energy", "non_empty":
		return true
	case "command":
		return strings.TrimSpace(conf.Command) != ""
	case "silero":
		return strings.TrimSpace(conf.ModelDir) != ""
	default:
		return false
	}
}

func shouldUseLegacyMemory(conf MemoryConf) bool {
	provider := strings.TrimSpace(conf.Type)
	return provider == "" || provider == "none"
}

func isSupportedMemory(conf MemoryConf) bool {
	switch strings.TrimSpace(conf.Type) {
	case "", "none", "local_short":
		return true
	default:
		return false
	}
}

func (c *MemoryConf) Normalize() {
	switch strings.TrimSpace(c.Type) {
	case "nomem":
		c.Type = "none"
	case "mem_local_short":
		c.Type = "local_short"
	}
}

func (c *LLMConf) Normalize() {
	if strings.TrimSpace(c.BaseURL) == "" && strings.TrimSpace(c.LegacyURL) != "" {
		c.BaseURL = c.LegacyURL
	}
	if strings.TrimSpace(c.Model) == "" && strings.TrimSpace(c.LegacyModelName) != "" {
		c.Model = c.LegacyModelName
	}
	switch strings.TrimSpace(c.Type) {
	case "ollama":
		c.Type = "openai"
		c.BaseURL = ensureOpenAICompatPath(c.BaseURL)
	case "xinference":
		c.Type = "openai"
		c.BaseURL = ensureOpenAICompatPath(c.BaseURL)
	}
}

func ensureOpenAICompatPath(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}

func (c *TTSConf) Normalize() {
	if strings.TrimSpace(c.Type) != "custom" && strings.TrimSpace(c.APIURL) == "" && strings.TrimSpace(c.LegacyURL) != "" {
		c.APIURL = c.LegacyURL
	}
}

func (c *PlayMusicConf) Normalize() {
	if strings.TrimSpace(c.MusicDir) == "" {
		c.MusicDir = "./music"
	}
	if len(c.MusicExt) == 0 {
		c.MusicExt = []string{".mp3", ".wav", ".p3"}
	}
	if c.RefreshTime <= 0 {
		c.RefreshTime = 60
	}
	for i, ext := range c.MusicExt {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext != "" && !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		c.MusicExt[i] = ext
	}
}

func (c *GetNewsConf) Normalize() {
	if strings.TrimSpace(c.DefaultRSSURL) == "" {
		c.DefaultRSSURL = "https://www.chinanews.com.cn/rss/society.xml"
	}
	if c.CategoryURLs == nil {
		c.CategoryURLs = map[string]string{
			"society": "https://www.chinanews.com.cn/rss/society.xml",
			"world":   "https://www.chinanews.com.cn/rss/world.xml",
			"finance": "https://www.chinanews.com.cn/rss/finance.xml",
		}
	}
}

func (c *GetWeatherConf) Normalize() {
	if strings.TrimSpace(c.DefaultLocation) == "" {
		c.DefaultLocation = "广州"
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = "https://restapi.amap.com"
	}
}

func (c *BaiduSearchConf) Normalize() {
	if strings.TrimSpace(c.SearchURL) == "" {
		c.SearchURL = "https://www.baidu.com/s"
	}
	if c.MaxResults <= 0 {
		c.MaxResults = 10
	}
	if c.MaxExtractPages <= 0 {
		c.MaxExtractPages = 3
	}
	if c.MaxContentLength <= 0 {
		c.MaxContentLength = 1000
	}
}

func (c *MCPConf) Normalize() {
	if strings.TrimSpace(c.Path) == "" {
		c.Path = filepath.Join("data", ".mcp_server_settings.json")
	}
}

func (c *BizConf) Validate() error {
	if c.Server.Port < 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 0 and 65535")
	}
	if c.Xiaozhi.Format == "" {
		return fmt.Errorf("xiaozhi.format is required")
	}
	if c.Xiaozhi.Transport == "" {
		return fmt.Errorf("xiaozhi.transport is required")
	}
	if err := validateASR(c.ASR); err != nil {
		return err
	}
	if err := validateLLM(c.LLM); err != nil {
		return err
	}
	if err := validateTTS(c.TTS); err != nil {
		return err
	}
	if err := validateVAD(c.Session.VAD); err != nil {
		return err
	}
	return nil
}

func validateASR(conf ASRConf) error {
	switch strings.TrimSpace(conf.Type) {
	case "", "file_stub":
		return nil
	case "openai":
		if strings.TrimSpace(conf.Model) == "" {
			return fmt.Errorf("asr.model is required when asr.type=openai")
		}
		format := strings.TrimSpace(conf.ResponseFormat)
		if !isSupportedASRResponseFormat(format) {
			return fmt.Errorf("asr.response_format must be one of json, text, verbose_json, srt, vtt")
		}
		return nil
	case "command":
		if strings.TrimSpace(conf.Command) == "" {
			return fmt.Errorf("asr.command is required when asr.type=command")
		}
		return nil
	case "sherpa_sensevoice":
		if strings.TrimSpace(conf.ModelDir) == "" {
			return fmt.Errorf("asr.model_dir is required when asr.type=sherpa_sensevoice")
		}
		return nil
	default:
		return fmt.Errorf("unsupported asr.type: %s", conf.Type)
	}
}

func isSupportedASRResponseFormat(format string) bool {
	switch strings.TrimSpace(format) {
	case "", "json", "text", "verbose_json", "srt", "vtt":
		return true
	default:
		return false
	}
}

func validateLLM(conf LLMConf) error {
	switch strings.TrimSpace(conf.Type) {
	case "", "echo":
		return nil
	case "openai":
		if strings.TrimSpace(conf.BaseURL) == "" {
			return fmt.Errorf("llm.base_url is required when llm.type=openai")
		}
		if strings.TrimSpace(conf.Model) == "" {
			return fmt.Errorf("llm.model is required when llm.type=openai")
		}
		return nil
	default:
		return fmt.Errorf("unsupported llm.type: %s", conf.Type)
	}
}

func validateTTS(conf TTSConf) error {
	switch strings.TrimSpace(conf.Type) {
	case "", "stub":
		return nil
	case "openai":
		if strings.TrimSpace(conf.Model) == "" {
			return fmt.Errorf("tts.model is required when tts.type=openai")
		}
		if strings.TrimSpace(conf.Voice) == "" {
			return fmt.Errorf("tts.voice is required when tts.type=openai")
		}
		format := strings.TrimSpace(conf.ResponseFormat)
		if format != "" && format != "wav" {
			return fmt.Errorf("tts.response_format must be wav")
		}
		return nil
	case "custom":
		if strings.TrimSpace(conf.LegacyURL) == "" {
			return fmt.Errorf("tts.url is required when tts.type=custom")
		}
		return nil
	case "command":
		if strings.TrimSpace(conf.Command) == "" {
			return fmt.Errorf("tts.command is required when tts.type=command")
		}
		return nil
	case "kokoro":
		if strings.TrimSpace(conf.ModelDir) == "" {
			return fmt.Errorf("tts.model_dir is required when tts.type=kokoro")
		}
		return nil
	default:
		return fmt.Errorf("unsupported tts.type: %s", conf.Type)
	}
}

func validateVAD(conf VADConf) error {
	switch strings.TrimSpace(conf.Type) {
	case "", "energy", "non_empty":
		return nil
	case "command":
		if strings.TrimSpace(conf.Command) == "" {
			return fmt.Errorf("session.vad.command is required when session.vad.type=command")
		}
		return nil
	case "silero":
		if strings.TrimSpace(conf.ModelDir) == "" {
			return fmt.Errorf("session.vad.model_dir is required when session.vad.type=silero")
		}
		return nil
	default:
		return fmt.Errorf("unsupported session.vad.type: %s", conf.Type)
	}
}
