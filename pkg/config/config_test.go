package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDefaultProviderTypes(t *testing.T) {
	conf := BizConf{
		Server:  ServerConf{Port: 8000},
		Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
		ASR:     ASRConf{Type: "file_stub"},
		LLM:     LLMConf{Type: "echo"},
		TTS:     TTSConf{Type: "stub"},
	}
	if err := conf.Validate(); err != nil {
		t.Fatalf("validate defaults: %v", err)
	}
}

func TestValidateOpenAIProviders(t *testing.T) {
	conf := BizConf{
		Server:  ServerConf{Port: 8000},
		Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
		ASR:     ASRConf{Type: "openai", Model: "whisper-1", ResponseFormat: "json"},
		LLM:     LLMConf{Type: "openai", BaseURL: "http://localhost/v1", Model: "chat-model"},
		TTS:     TTSConf{Type: "openai", Model: "tts-1", Voice: "alloy", ResponseFormat: "wav"},
	}
	if err := conf.Validate(); err != nil {
		t.Fatalf("validate openai providers: %v", err)
	}
}

func TestValidateCustomTTSProvider(t *testing.T) {
	conf := BizConf{
		Server:  ServerConf{Port: 8000},
		Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
		ASR:     ASRConf{Type: "file_stub"},
		LLM:     LLMConf{Type: "echo"},
		TTS: TTSConf{
			Type:      "custom",
			LegacyURL: "http://localhost:9880/tts",
			Format:    "wav",
		},
	}
	if err := conf.Validate(); err != nil {
		t.Fatalf("validate custom tts: %v", err)
	}

	conf.TTS.LegacyURL = ""
	if err := conf.Validate(); err == nil {
		t.Fatal("expected missing custom tts url to fail")
	}

	conf.TTS.LegacyURL = "http://localhost:9880/tts"
	conf.TTS.Format = "mp3"
	if err := conf.Validate(); err != nil {
		t.Fatalf("custom tts should allow ffmpeg-backed formats: %v", err)
	}
}

func TestValidateCommandTTSProvider(t *testing.T) {
	conf := BizConf{
		Server:  ServerConf{Port: 8000},
		Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
		ASR:     ASRConf{Type: "file_stub"},
		LLM:     LLMConf{Type: "echo"},
		TTS: TTSConf{
			Type:      "command",
			Command:   "python3",
			Args:      []string{"edge_tts_bridge.py", "--text", "{text}", "--output", "{output}"},
			Env:       map[string]string{"EDGE_TTS_VOICE": "zh-CN-XiaoxiaoNeural"},
			Format:    "mp3",
			OutputDir: "tmp",
			Voice:     "zh-CN-XiaoxiaoNeural",
		},
	}
	if err := conf.Validate(); err != nil {
		t.Fatalf("validate command tts: %v", err)
	}

	conf.TTS.Command = ""
	if err := conf.Validate(); err == nil {
		t.Fatal("expected missing command tts command to fail")
	}
}

func TestValidateCommandVADProvider(t *testing.T) {
	conf := BizConf{
		Server:  ServerConf{Port: 8000},
		Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
		ASR:     ASRConf{Type: "file_stub"},
		LLM:     LLMConf{Type: "echo"},
		TTS:     TTSConf{Type: "stub"},
		Session: AudioSessionConf{
			VAD: VADConf{
				Type:       "command",
				Command:    "python3",
				Args:       []string{"silero_vad_bridge.py", "{file}"},
				Env:        map[string]string{"SILERO_MODEL_DIR": "models/snakers4_silero-vad"},
				Threshold:  0.5,
				SampleRate: 16000,
				Channels:   1,
			},
		},
	}
	if err := conf.Validate(); err != nil {
		t.Fatalf("validate command vad: %v", err)
	}

	conf.Session.VAD.Command = ""
	if err := conf.Validate(); err == nil {
		t.Fatal("expected missing command vad command to fail")
	}
}

func TestValidateOpenAIASRResponseFormats(t *testing.T) {
	formats := []string{"", "json", "text", "verbose_json", "srt", "vtt"}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			conf := BizConf{
				Server:  ServerConf{Port: 8000},
				Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
				ASR:     ASRConf{Type: "openai", Model: "whisper-1", ResponseFormat: format},
				LLM:     LLMConf{Type: "echo"},
				TTS:     TTSConf{Type: "stub"},
			}
			if err := conf.Validate(); err != nil {
				t.Fatalf("validate response_format=%q: %v", format, err)
			}
		})
	}

	conf := BizConf{
		Server:  ServerConf{Port: 8000},
		Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
		ASR:     ASRConf{Type: "openai", Model: "whisper-1", ResponseFormat: "xml"},
		LLM:     LLMConf{Type: "echo"},
		TTS:     TTSConf{Type: "stub"},
	}
	if err := conf.Validate(); err == nil {
		t.Fatal("expected unsupported asr response_format to fail")
	}
}

func TestValidateCommandASRProvider(t *testing.T) {
	conf := BizConf{
		Server:  ServerConf{Port: 8000},
		Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
		ASR: ASRConf{
			Type:    "command",
			Command: "python3",
			Args:    []string{"asr.py", "{file}", "{session_id}"},
			Env:     map[string]string{"MODEL": "iic/SenseVoiceSmall"},
		},
		LLM: LLMConf{Type: "echo"},
		TTS: TTSConf{Type: "stub"},
	}
	if err := conf.Validate(); err != nil {
		t.Fatalf("validate command asr: %v", err)
	}

	conf.ASR.Command = ""
	if err := conf.Validate(); err == nil {
		t.Fatal("expected missing command asr command to fail")
	}
}

func TestValidateRejectsUnsupportedProvider(t *testing.T) {
	conf := BizConf{
		Server:  ServerConf{Port: 8000},
		Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
		ASR:     ASRConf{Type: "unknown"},
	}
	if err := conf.Validate(); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestReloadConfigDoesNotMutateGlobalOnValidationError(t *testing.T) {
	old := conf
	oldPath := configFileUsed
	conf = BizConf{
		Server:  ServerConf{Port: 8000},
		Xiaozhi: XiaozhiConf{Format: "opus", Transport: "websocket"},
		ASR:     ASRConf{Type: "file_stub"},
		LLM:     LLMConf{Type: "echo"},
		TTS:     TTSConf{Type: "stub"},
	}
	defer func() {
		conf = old
		configFileUsed = oldPath
	}()
	configFileUsed = "previous.yaml"

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "biz.yaml"), []byte(`
server:
  port: 8000
xiaozhi:
  format: opus
  transport: websocket
asr:
  type: unknown
llm:
  type: echo
tts:
  type: stub
`), 0o644); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp config dir: %v", err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	if err := ReloadConfig(); err == nil {
		t.Fatal("expected bad reload to fail")
	}
	if conf.ASR.Type != "file_stub" || conf.Server.Port != 8000 || conf.Xiaozhi.Format != "opus" {
		t.Fatalf("global config mutated after failed reload: %+v", conf)
	}
	if GetConfigFilePath() != "previous.yaml" {
		t.Fatalf("config path mutated after failed reload: %q", GetConfigFilePath())
	}
}

func TestConfigSearchPrefersPythonPrivateConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "conf"), 0o755); err != nil {
		t.Fatalf("mkdir conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", ".config.yaml"), minimalConfigYAML(8101, "private"), 0o644); err != nil {
		t.Fatalf("write private config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conf", "biz.yaml"), minimalConfigYAML(8102, "biz"), 0o644); err != nil {
		t.Fatalf("write biz config: %v", err)
	}
	withWorkingDir(t, dir, func() {
		file, ok := firstExistingConfigFile(configSearchFiles())
		if !ok {
			t.Fatal("expected config file")
		}
		if filepath.ToSlash(file) != "data/.config.yaml" {
			t.Fatalf("config file got %q want data/.config.yaml", file)
		}
	})
}

func TestReloadPrivateConfigFailsWhenDefaultConfigHasMissingKeys(t *testing.T) {
	old := conf
	oldPath := configFileUsed
	defer func() {
		conf = old
		configFileUsed = oldPath
	}()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), minimalConfigYAML(8101, "default"), 0o644); err != nil {
		t.Fatalf("write default config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", ".config.yaml"), []byte(`
server:
  port: 8101
xiaozhi:
  format: opus
  transport: websocket
asr:
  type: file_stub
tts:
  type: stub
local:
  prompt: private
`), 0o644); err != nil {
		t.Fatalf("write stale private config: %v", err)
	}

	withWorkingDir(t, dir, func() {
		err := ReloadConfig()
		if err == nil {
			t.Fatal("expected stale private config to fail")
		}
		if !strings.Contains(err.Error(), "missing keys") || !strings.Contains(err.Error(), "llm") {
			t.Fatalf("unexpected stale config error: %v", err)
		}
	})
}

func TestReloadPrivateConfigSkipsFreshnessCheckWhenDefaultConfigMissing(t *testing.T) {
	old := conf
	oldPath := configFileUsed
	defer func() {
		conf = old
		configFileUsed = oldPath
	}()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", ".config.yaml"), minimalConfigYAML(8106, "private"), 0o644); err != nil {
		t.Fatalf("write private config: %v", err)
	}

	withWorkingDir(t, dir, func() {
		if err := ReloadConfig(); err != nil {
			t.Fatalf("reload private config without default config: %v", err)
		}
	})
	if conf.Server.Port != 8106 || conf.Local.Prompt != "private" {
		t.Fatalf("unexpected private config loaded: %+v", conf)
	}
}

func TestExplicitConfigPathFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "space separated",
			args: []string{"go-xiaozhi", "--config_path", "custom.yaml"},
			want: "custom.yaml",
		},
		{
			name: "equals separated",
			args: []string{"go-xiaozhi", "--config_path=data/.config.yaml"},
			want: "data/.config.yaml",
		},
		{
			name: "trims value",
			args: []string{"go-xiaozhi", "--config_path", "  conf/biz.yaml  "},
			want: "conf/biz.yaml",
		},
		{
			name: "missing value",
			args: []string{"go-xiaozhi", "--config_path"},
			want: "",
		},
		{
			name: "empty equals value",
			args: []string{"go-xiaozhi", "--config_path=  "},
			want: "",
		},
		{
			name: "ignores test flags",
			args: []string{"go-xiaozhi", "-test.v", "-test.run", "Config"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := explicitConfigPathFromArgs(tt.args); got != tt.want {
				t.Fatalf("explicitConfigPathFromArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReloadConfigUsesExplicitConfigPathArg(t *testing.T) {
	old := conf
	oldPath := configFileUsed
	oldArgs := os.Args
	defer func() {
		conf = old
		configFileUsed = oldPath
		os.Args = oldArgs
	}()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", ".config.yaml"), minimalConfigYAML(8107, "private"), 0o644); err != nil {
		t.Fatalf("write private config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chosen.yaml"), minimalConfigYAML(8200, "explicit"), 0o644); err != nil {
		t.Fatalf("write explicit config: %v", err)
	}

	os.Args = []string{"go-xiaozhi", "--config_path", "chosen.yaml"}
	withWorkingDir(t, dir, func() {
		if err := ReloadConfig(); err != nil {
			t.Fatalf("reload explicit config: %v", err)
		}
	})
	if conf.Server.Port != 8200 || conf.Local.Prompt != "explicit" {
		t.Fatalf("unexpected explicit config loaded: %+v", conf)
	}
	if filepath.ToSlash(GetConfigFilePath()) != "chosen.yaml" {
		t.Fatalf("config path got %q want chosen.yaml", GetConfigFilePath())
	}
}

func TestReloadPythonPrivateConfigMapsTopLevelPrompt(t *testing.T) {
	old := conf
	oldPath := configFileUsed
	defer func() {
		conf = old
		configFileUsed = oldPath
	}()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", ".config.yaml"), []byte(`
server:
  port: 8104
xiaozhi:
  transport: websocket
  audio_params:
    format: opus
    sample_rate: 16000
    channels: 1
    frame_duration: 60
asr:
  type: file_stub
llm:
  type: echo
tts:
  type: stub
delete_audio: true
prompt: python private prompt
CMD_exit:
  - 退出
wakeup_words:
  - 小智
`), 0o644); err != nil {
		t.Fatalf("write private config: %v", err)
	}
	withWorkingDir(t, dir, func() {
		if err := ReloadConfig(); err != nil {
			t.Fatalf("reload python private config: %v", err)
		}
	})
	if conf.Local.Prompt != "python private prompt" {
		t.Fatalf("top-level prompt not mapped: %q", conf.Local.Prompt)
	}
	if conf.Xiaozhi.Format != "opus" || conf.Xiaozhi.SampleRate != 16000 {
		t.Fatalf("legacy xiaozhi audio params not mapped: %+v", conf.Xiaozhi)
	}
	if got, want := conf.Intent.ExitCommands, []string{"退出"}; !equalStrings(got, want) {
		t.Fatalf("exit commands got %v want %v", got, want)
	}
	if !conf.ASR.DeleteAudio {
		t.Fatal("top-level delete_audio=true not mapped to asr.delete_audio")
	}
	if conf.TTS.DeleteAudio == nil || !*conf.TTS.DeleteAudio {
		t.Fatalf("top-level delete_audio=true not mapped to tts.delete_audio: %v", conf.TTS.DeleteAudio)
	}
	if filepath.ToSlash(GetConfigFilePath()) != "data/.config.yaml" {
		t.Fatalf("config path got %q want data/.config.yaml", GetConfigFilePath())
	}
}

func TestReloadConfigReadsConfBizWhenPrivateConfigMissing(t *testing.T) {
	old := conf
	oldPath := configFileUsed
	defer func() {
		conf = old
		configFileUsed = oldPath
	}()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "conf"), 0o755); err != nil {
		t.Fatalf("mkdir conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conf", "biz.yaml"), minimalConfigYAML(8103, "biz"), 0o644); err != nil {
		t.Fatalf("write biz config: %v", err)
	}
	withWorkingDir(t, dir, func() {
		if err := ReloadConfig(); err != nil {
			t.Fatalf("reload config: %v", err)
		}
	})
	if conf.Server.Port != 8103 || conf.Local.Prompt != "biz" {
		t.Fatalf("unexpected loaded config: %+v", conf)
	}
	if filepath.ToSlash(GetConfigFilePath()) != "conf/biz.yaml" {
		t.Fatalf("config path got %q want conf/biz.yaml", GetConfigFilePath())
	}
}

func TestReloadConfigReadsPythonRootConfigWhenGoConfigMissing(t *testing.T) {
	old := conf
	oldPath := configFileUsed
	defer func() {
		conf = old
		configFileUsed = oldPath
	}()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
server:
  port: 8105
xiaozhi:
  type: hello
  version: 1
  transport: websocket
  audio_params:
    format: opus
    sample_rate: 16000
    channels: 1
    frame_duration: 60
asr:
  type: file_stub
llm:
  type: echo
tts:
  type: stub
plugins:
  play_music:
    music_dir: ./music
    music_ext:
      - .mp3
      - .wav
    refresh_time: 300
prompt: python root prompt
CMD_exit:
  - 关闭
`), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	withWorkingDir(t, dir, func() {
		if err := ReloadConfig(); err != nil {
			t.Fatalf("reload python root config: %v", err)
		}
	})
	if conf.Server.Port != 8105 || conf.Local.Prompt != "python root prompt" {
		t.Fatalf("unexpected config loaded from config.yaml: %+v", conf)
	}
	if conf.Plugins.PlayMusic.MusicDir != "./music" ||
		len(conf.Plugins.PlayMusic.MusicExt) != 2 ||
		conf.Plugins.PlayMusic.MusicExt[0] != ".mp3" ||
		conf.Plugins.PlayMusic.RefreshTime != 300 {
		t.Fatalf("play music plugin config not loaded: %+v", conf.Plugins.PlayMusic)
	}
	if filepath.ToSlash(GetConfigFilePath()) != "config.yaml" {
		t.Fatalf("config path got %q want config.yaml", GetConfigFilePath())
	}
}

func TestNormalizeLegacyPythonConfigFields(t *testing.T) {
	enableGreeting := false
	wakeupCache := true
	stopNotify := true
	deleteAudio := false
	usePrivate := true
	flowEnabled := true
	dynamicDelay := true
	conf := BizConf{
		Xiaozhi: XiaozhiConf{
			Transport: "websocket",
			AudioParams: &XiaozhiAudioParams{
				Format:        "opus",
				SampleRate:    16000,
				Channels:      1,
				FrameDuration: 60,
			},
		},
		LegacyPrompt:                         "python prompt",
		LegacyCMDExit:                        []string{"退出", "关闭"},
		LegacyWakeupWords:                    []string{"你好小智"},
		LegacyEnableGreeting:                 &enableGreeting,
		LegacyEnableWakeupWordsResponseCache: &wakeupCache,
		LegacyEnableStopTTSNotify:            &stopNotify,
		LegacyStopTTSNotifyVoice:             "config/assets/tts_notify.opus",
		LegacyDeleteAudio:                    &deleteAudio,
		LegacyTTSTimeout:                     10,
		LegacyCloseConnectionNoVoiceTime:     120,
		LegacyUsePrivateConfig:               &usePrivate,
		LegacyEnableSmartAudioFlowControl:    &flowEnabled,
		LegacyAudioSendRateMultiplier:        1.2,
		LegacySentenceIntervalDelay:          200,
		LegacyEnableDynamicSentenceDelay:     &dynamicDelay,
		LegacySentenceLengthThreshold:        30,
		LegacyLongSentenceExtraDelay:         300,
	}

	conf.Normalize()

	if conf.Xiaozhi.Format != "opus" || conf.Xiaozhi.SampleRate != 16000 ||
		conf.Xiaozhi.Channels != 1 || conf.Xiaozhi.FrameDuration != 60 {
		t.Fatalf("legacy xiaozhi audio params not normalized: %+v", conf.Xiaozhi)
	}
	if conf.Local.Prompt != "python prompt" {
		t.Fatalf("legacy top-level prompt not normalized: %q", conf.Local.Prompt)
	}
	if got, want := conf.Intent.ExitCommands, []string{"退出", "关闭"}; !equalStrings(got, want) {
		t.Fatalf("exit commands = %v, want %v", got, want)
	}
	if got, want := conf.Intent.WakeupWords, []string{"你好小智"}; !equalStrings(got, want) {
		t.Fatalf("wakeup words = %v, want %v", got, want)
	}
	if conf.Intent.EnableGreeting == nil || *conf.Intent.EnableGreeting {
		t.Fatalf("enable greeting = %v, want false", conf.Intent.EnableGreeting)
	}
	if !conf.Intent.WakeupResponseCache {
		t.Fatal("wakeup response cache should be enabled")
	}
	if !conf.TTS.StopNotify.Enabled || conf.TTS.StopNotify.Path != "config/assets/tts_notify.opus" {
		t.Fatalf("stop tts notify not normalized: %+v", conf.TTS.StopNotify)
	}
	if conf.Intent.WakeupResponseCacheDir != "config/assets" || conf.Intent.WakeupResponseCacheMinSize != 15*1024 {
		t.Fatalf("unexpected wakeup cache defaults: %+v", conf.Intent)
	}
	if conf.Session.NoVoiceCloseSeconds != 120 {
		t.Fatalf("no voice close seconds = %d, want 120", conf.Session.NoVoiceCloseSeconds)
	}
	if !conf.Private.Enabled {
		t.Fatal("private config should be enabled")
	}
	if conf.ASR.DeleteAudio {
		t.Fatal("legacy delete_audio=false should disable asr delete_audio")
	}
	if conf.TTS.DeleteAudio == nil || *conf.TTS.DeleteAudio {
		t.Fatalf("legacy delete_audio=false should disable tts delete_audio: %v", conf.TTS.DeleteAudio)
	}
	if conf.TTS.TimeoutSeconds != 10 {
		t.Fatalf("tts timeout = %d, want 10", conf.TTS.TimeoutSeconds)
	}
	if !conf.AudioFlow.Enabled || conf.AudioFlow.SendRateMultiplier != 1.2 {
		t.Fatalf("audio flow not normalized: %+v", conf.AudioFlow)
	}
	if !conf.SentenceDelay.Dynamic || conf.SentenceDelay.BaseDelayMs != 200 ||
		conf.SentenceDelay.LengthThreshold != 30 || conf.SentenceDelay.LongSentenceExtraMs != 300 {
		t.Fatalf("sentence delay not normalized: %+v", conf.SentenceDelay)
	}
}

func TestNormalizeLegacyDeleteAudioAppliesAfterSelectedProviders(t *testing.T) {
	deleteAudio := true
	conf := BizConf{
		ASR: ASRConf{Type: "file_stub"},
		TTS: TTSConf{Type: "stub"},
		LegacySelectedModule: map[string]string{
			"ASR": "OpenAIASR",
			"TTS": "CommandTTS",
		},
		LegacyASR: map[string]ASRConf{
			"OpenAIASR": {
				Type:  "openai",
				Model: "whisper-1",
			},
		},
		LegacyTTS: map[string]TTSConf{
			"CommandTTS": {
				Type:    "command",
				Command: "tts-helper",
			},
		},
		LegacyDeleteAudio: &deleteAudio,
	}

	conf.Normalize()

	if conf.ASR.Type != "openai" {
		t.Fatalf("selected asr provider not applied: %+v", conf.ASR)
	}
	if !conf.ASR.DeleteAudio {
		t.Fatal("legacy delete_audio=true should apply to selected asr provider")
	}
	if conf.TTS.Type != "command" {
		t.Fatalf("selected tts provider not applied: %+v", conf.TTS)
	}
	if conf.TTS.DeleteAudio == nil || !*conf.TTS.DeleteAudio {
		t.Fatalf("legacy delete_audio=true should apply to selected tts provider: %v", conf.TTS.DeleteAudio)
	}
}

func TestNormalizeKeepsExplicitGroupedFields(t *testing.T) {
	legacyGreeting := false
	groupedGreeting := true
	conf := BizConf{
		Xiaozhi: XiaozhiConf{
			Format:        "opus",
			Transport:     "websocket",
			SampleRate:    24000,
			Channels:      2,
			FrameDuration: 40,
			AudioParams: &XiaozhiAudioParams{
				Format:        "pcm",
				SampleRate:    16000,
				Channels:      1,
				FrameDuration: 60,
			},
		},
		Intent: IntentConf{
			ExitCommands:   []string{"拜拜"},
			WakeupWords:    []string{"小智"},
			EnableGreeting: &groupedGreeting,
		},
		Session: AudioSessionConf{NoVoiceCloseSeconds: 30},
		AudioFlow: AudioFlowConf{
			SendRateMultiplier: 0.8,
		},
		SentenceDelay: SentenceDelayConf{
			BaseDelayMs:         100,
			LengthThreshold:     10,
			LongSentenceExtraMs: 50,
		},
		TTS:                              TTSConf{TimeoutSeconds: 5},
		Local:                            LocalProviderConf{Prompt: "grouped prompt"},
		LegacyPrompt:                     "python prompt",
		LegacyCMDExit:                    []string{"退出"},
		LegacyWakeupWords:                []string{"你好小智"},
		LegacyEnableGreeting:             &legacyGreeting,
		LegacyCloseConnectionNoVoiceTime: 120,
		LegacyTTSTimeout:                 10,
		LegacyAudioSendRateMultiplier:    1.2,
		LegacySentenceIntervalDelay:      200,
		LegacySentenceLengthThreshold:    30,
		LegacyLongSentenceExtraDelay:     300,
	}

	conf.Normalize()

	if conf.Xiaozhi.Format != "opus" || conf.Xiaozhi.SampleRate != 24000 ||
		conf.Xiaozhi.Channels != 2 || conf.Xiaozhi.FrameDuration != 40 {
		t.Fatalf("explicit xiaozhi fields overwritten: %+v", conf.Xiaozhi)
	}
	if conf.Local.Prompt != "grouped prompt" {
		t.Fatalf("explicit local prompt overwritten: %q", conf.Local.Prompt)
	}
	if got, want := conf.Intent.ExitCommands, []string{"拜拜"}; !equalStrings(got, want) {
		t.Fatalf("exit commands = %v, want %v", got, want)
	}
	if got, want := conf.Intent.WakeupWords, []string{"小智"}; !equalStrings(got, want) {
		t.Fatalf("wakeup words = %v, want %v", got, want)
	}
	if conf.Intent.EnableGreeting == nil || !*conf.Intent.EnableGreeting {
		t.Fatalf("enable greeting overwritten: %v", conf.Intent.EnableGreeting)
	}
	if conf.Session.NoVoiceCloseSeconds != 30 {
		t.Fatalf("no voice close seconds = %d, want 30", conf.Session.NoVoiceCloseSeconds)
	}
	if conf.AudioFlow.SendRateMultiplier != 0.8 {
		t.Fatalf("send rate multiplier = %v, want 0.8", conf.AudioFlow.SendRateMultiplier)
	}
	if conf.TTS.TimeoutSeconds != 5 {
		t.Fatalf("explicit tts timeout overwritten: %d", conf.TTS.TimeoutSeconds)
	}
	if conf.SentenceDelay.BaseDelayMs != 100 || conf.SentenceDelay.LengthThreshold != 10 ||
		conf.SentenceDelay.LongSentenceExtraMs != 50 {
		t.Fatalf("sentence delay explicit fields overwritten: %+v", conf.SentenceDelay)
	}
}

func TestNormalizeLLMLegacyOpenAIFields(t *testing.T) {
	conf := BizConf{
		LLM: LLMConf{
			Type:            "openai",
			LegacyURL:       "https://legacy.example/v1",
			LegacyModelName: "legacy-model",
		},
	}
	conf.Normalize()
	if conf.LLM.BaseURL != "https://legacy.example/v1" || conf.LLM.Model != "legacy-model" {
		t.Fatalf("legacy llm fields not normalized: %+v", conf.LLM)
	}

	conf = BizConf{
		LLM: LLMConf{
			Type:            "openai",
			BaseURL:         "https://new.example/v1",
			Model:           "new-model",
			LegacyURL:       "https://legacy.example/v1",
			LegacyModelName: "legacy-model",
		},
	}
	conf.Normalize()
	if conf.LLM.BaseURL != "https://new.example/v1" || conf.LLM.Model != "new-model" {
		t.Fatalf("explicit llm fields overwritten: %+v", conf.LLM)
	}
}

func TestNormalizeLegacyOpenAICompatibleLLMTypes(t *testing.T) {
	tests := []struct {
		name    string
		llmType string
		baseURL string
		model   string
		wantURL string
	}{
		{
			name:    "ollama",
			llmType: "ollama",
			baseURL: "http://localhost:11434",
			model:   "qwen2.5",
			wantURL: "http://localhost:11434/v1",
		},
		{
			name:    "xinference",
			llmType: "xinference",
			baseURL: "http://localhost:9997/",
			model:   "qwen2.5:72b-AWQ",
			wantURL: "http://localhost:9997/v1",
		},
		{
			name:    "already v1",
			llmType: "ollama",
			baseURL: "http://localhost:11434/v1",
			model:   "qwen2.5",
			wantURL: "http://localhost:11434/v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := BizConf{
				LLM: LLMConf{
					Type:            tt.llmType,
					BaseURL:         tt.baseURL,
					LegacyModelName: tt.model,
				},
			}

			conf.Normalize()

			if conf.LLM.Type != "openai" || conf.LLM.BaseURL != tt.wantURL || conf.LLM.Model != tt.model {
				t.Fatalf("llm not normalized: %+v", conf.LLM)
			}
		})
	}
}

func TestNormalizeLegacySelectedOpenAICompatibleLLMProvider(t *testing.T) {
	tests := []struct {
		name    string
		llmName string
		llm     LLMConf
		wantURL string
	}{
		{
			name:    "ollama selected module",
			llmName: "OllamaLLM",
			llm: LLMConf{
				Type:            "ollama",
				BaseURL:         "http://localhost:11434",
				LegacyModelName: "qwen2.5",
			},
			wantURL: "http://localhost:11434/v1",
		},
		{
			name:    "xinference selected module",
			llmName: "XinferenceLLM",
			llm: LLMConf{
				Type:            "xinference",
				BaseURL:         "http://localhost:9997",
				LegacyModelName: "qwen2.5:72b-AWQ",
			},
			wantURL: "http://localhost:9997/v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := BizConf{
				LLM: LLMConf{Type: "echo"},
				LegacySelectedModule: map[string]string{
					"LLM": tt.llmName,
				},
				LegacyLLM: map[string]LLMConf{
					tt.llmName: tt.llm,
				},
			}

			conf.Normalize()

			if conf.LLM.Type != "openai" || conf.LLM.BaseURL != tt.wantURL || conf.LLM.Model != tt.llm.LegacyModelName {
				t.Fatalf("legacy selected llm not normalized: %+v", conf.LLM)
			}
		})
	}
}

func TestNormalizeTTSLegacyURL(t *testing.T) {
	conf := BizConf{
		TTS: TTSConf{
			Type:      "openai",
			LegacyURL: "https://legacy.example/audio/speech",
		},
	}
	conf.Normalize()
	if conf.TTS.APIURL != "https://legacy.example/audio/speech" {
		t.Fatalf("legacy tts url not normalized: %+v", conf.TTS)
	}

	conf = BizConf{
		TTS: TTSConf{
			Type:      "openai",
			APIURL:    "https://new.example/audio/speech",
			LegacyURL: "https://legacy.example/audio/speech",
		},
	}
	conf.Normalize()
	if conf.TTS.APIURL != "https://new.example/audio/speech" {
		t.Fatalf("explicit tts api_url overwritten: %+v", conf.TTS)
	}
}

func TestNormalizeLegacySelectedOpenAIProviders(t *testing.T) {
	conf := BizConf{
		LegacySelectedModule: map[string]string{
			"LLM": "ChatGLMLLM",
			"TTS": "OpenAITTS",
			"ASR": "OpenAIASR",
			"VAD": "EnergyVAD",
		},
		LegacyLLM: map[string]LLMConf{
			"ChatGLMLLM": {
				Type:            "openai",
				LegacyURL:       "https://open.bigmodel.cn/api/paas/v4/",
				LegacyModelName: "glm-4-flash",
				APIKey:          "llm-key",
			},
		},
		LegacyTTS: map[string]TTSConf{
			"OpenAITTS": {
				Type:      "openai",
				LegacyURL: "https://api.example/audio/speech",
				Model:     "tts-1",
				Voice:     "alloy",
			},
		},
		LegacyASR: map[string]ASRConf{
			"OpenAIASR": {
				Type:  "openai",
				Model: "whisper-1",
			},
		},
		LegacyVAD: map[string]VADConf{
			"EnergyVAD": {
				Type:            "energy",
				EnergyThreshold: 900,
			},
		},
	}

	conf.Normalize()

	if conf.LLM.Type != "openai" || conf.LLM.BaseURL != "https://open.bigmodel.cn/api/paas/v4/" ||
		conf.LLM.Model != "glm-4-flash" {
		t.Fatalf("legacy selected llm not normalized: %+v", conf.LLM)
	}
	if conf.TTS.Type != "openai" || conf.TTS.APIURL != "https://api.example/audio/speech" ||
		conf.TTS.Model != "tts-1" || conf.TTS.Voice != "alloy" {
		t.Fatalf("legacy selected tts not normalized: %+v", conf.TTS)
	}
	if conf.ASR.Type != "openai" || conf.ASR.Model != "whisper-1" {
		t.Fatalf("legacy selected asr not normalized: %+v", conf.ASR)
	}
	if conf.Session.VAD.Type != "energy" || conf.Session.VAD.EnergyThreshold != 900 {
		t.Fatalf("legacy selected vad not normalized: %+v", conf.Session.VAD)
	}
}

func TestNormalizeLegacySelectedInfersMissingProviderTypes(t *testing.T) {
	conf := BizConf{
		LLM: LLMConf{Type: "echo"},
		TTS: TTSConf{Type: "stub"},
		ASR: ASRConf{Type: "file_stub"},
		Session: AudioSessionConf{
			VAD: VADConf{Type: "energy"},
		},
		LegacySelectedModule: map[string]string{
			"LLM": "ChatGLMLLM",
			"TTS": "OpenAITTS",
			"ASR": "OpenAIASR",
			"VAD": "EnergyVAD",
		},
		LegacyLLM: map[string]LLMConf{
			"ChatGLMLLM": {
				LegacyURL:       "https://open.bigmodel.cn/api/paas/v4/",
				LegacyModelName: "glm-4-flash",
			},
		},
		LegacyTTS: map[string]TTSConf{
			"OpenAITTS": {
				LegacyURL: "https://api.example/audio/speech",
				Model:     "tts-1",
				Voice:     "alloy",
			},
		},
		LegacyASR: map[string]ASRConf{
			"OpenAIASR": {
				Model: "whisper-1",
			},
		},
		LegacyVAD: map[string]VADConf{
			"EnergyVAD": {
				EnergyThreshold: 900,
			},
		},
	}

	conf.Normalize()

	if conf.LLM.Type != "openai" || conf.LLM.BaseURL != "https://open.bigmodel.cn/api/paas/v4/" ||
		conf.LLM.Model != "glm-4-flash" {
		t.Fatalf("legacy selected llm type not inferred: %+v", conf.LLM)
	}
	if conf.TTS.Type != "openai" || conf.TTS.APIURL != "https://api.example/audio/speech" ||
		conf.TTS.Model != "tts-1" || conf.TTS.Voice != "alloy" {
		t.Fatalf("legacy selected tts type not inferred: %+v", conf.TTS)
	}
	if conf.ASR.Type != "openai" || conf.ASR.Model != "whisper-1" {
		t.Fatalf("legacy selected asr type not inferred: %+v", conf.ASR)
	}
	if conf.Session.VAD.Type != "energy" || conf.Session.VAD.EnergyThreshold != 900 {
		t.Fatalf("legacy selected vad type not inferred: %+v", conf.Session.VAD)
	}
}

func TestNormalizeLegacySelectedInfersMissingOpenAICompatibleLLMTypes(t *testing.T) {
	tests := []struct {
		name    string
		llmName string
		llm     LLMConf
		wantURL string
	}{
		{
			name:    "ollama",
			llmName: "OllamaLLM",
			llm: LLMConf{
				BaseURL:         "http://localhost:11434",
				LegacyModelName: "qwen2.5",
			},
			wantURL: "http://localhost:11434/v1",
		},
		{
			name:    "xinference",
			llmName: "XinferenceLLM",
			llm: LLMConf{
				BaseURL:         "http://localhost:9997",
				LegacyModelName: "qwen2.5:72b-AWQ",
			},
			wantURL: "http://localhost:9997/v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := BizConf{
				LLM: LLMConf{Type: "echo"},
				LegacySelectedModule: map[string]string{
					"LLM": tt.llmName,
				},
				LegacyLLM: map[string]LLMConf{
					tt.llmName: tt.llm,
				},
			}

			conf.Normalize()

			if conf.LLM.Type != "openai" || conf.LLM.BaseURL != tt.wantURL || conf.LLM.Model != tt.llm.LegacyModelName {
				t.Fatalf("legacy selected llm type not inferred: %+v", conf.LLM)
			}
		})
	}
}

func TestNormalizeLegacySelectedCustomTTSProvider(t *testing.T) {
	conf := BizConf{
		TTS: TTSConf{Type: "stub"},
		LegacySelectedModule: map[string]string{
			"TTS": "CustomTTS",
		},
		LegacyTTS: map[string]TTSConf{
			"CustomTTS": {
				Type:      "custom",
				LegacyURL: "http://127.0.0.1:9880/tts",
				Format:    "mp3",
				Params: map[string]any{
					"text":    "{prompt_text}",
					"speaker": "jok",
				},
				Headers: map[string]string{
					"Authorization": "Bearer token",
				},
			},
		},
	}

	conf.Normalize()

	if conf.TTS.Type != "custom" || conf.TTS.LegacyURL != "http://127.0.0.1:9880/tts" || conf.TTS.Format != "mp3" {
		t.Fatalf("legacy selected custom tts not normalized: %+v", conf.TTS)
	}
	if conf.TTS.Params["text"] != "{prompt_text}" || conf.TTS.Headers["Authorization"] != "Bearer token" {
		t.Fatalf("custom tts params/headers not preserved: %+v", conf.TTS)
	}
}

func TestNormalizeLegacySelectedCommandASRProvider(t *testing.T) {
	conf := BizConf{
		ASR: ASRConf{Type: "file_stub"},
		LegacySelectedModule: map[string]string{
			"ASR": "FunASRCommand",
		},
		LegacyASR: map[string]ASRConf{
			"FunASRCommand": {
				Type:    "command",
				Command: "python3",
				Args:    []string{"funasr_bridge.py", "{file}", "{session_id}"},
				Env:     map[string]string{"FUNASR_MODEL": "iic/SenseVoiceSmall"},
			},
		},
	}

	conf.Normalize()

	if conf.ASR.Type != "command" || conf.ASR.Command != "python3" {
		t.Fatalf("legacy selected command asr not normalized: %+v", conf.ASR)
	}
	if len(conf.ASR.Args) != 3 || conf.ASR.Args[1] != "{file}" || conf.ASR.Env["FUNASR_MODEL"] != "iic/SenseVoiceSmall" {
		t.Fatalf("command asr args/env not preserved: %+v", conf.ASR)
	}
}

func TestNormalizeLegacySelectedCommandTTSProvider(t *testing.T) {
	conf := BizConf{
		TTS: TTSConf{Type: "stub"},
		LegacySelectedModule: map[string]string{
			"TTS": "EdgeTTSCommand",
		},
		LegacyTTS: map[string]TTSConf{
			"EdgeTTSCommand": {
				Type:    "command",
				Command: "python3",
				Args:    []string{"edge_tts_bridge.py", "--text", "{text}", "--output", "{output}"},
				Env:     map[string]string{"EDGE_TTS_VOICE": "zh-CN-XiaoxiaoNeural"},
				Format:  "mp3",
			},
		},
	}

	conf.Normalize()

	if conf.TTS.Type != "command" || conf.TTS.Command != "python3" || conf.TTS.Format != "mp3" {
		t.Fatalf("legacy selected command tts not normalized: %+v", conf.TTS)
	}
	if len(conf.TTS.Args) != 5 || conf.TTS.Args[2] != "{text}" || conf.TTS.Env["EDGE_TTS_VOICE"] != "zh-CN-XiaoxiaoNeural" {
		t.Fatalf("command tts args/env not preserved: %+v", conf.TTS)
	}
}

func TestNormalizeLegacySelectedCommandVADProvider(t *testing.T) {
	conf := BizConf{
		Session: AudioSessionConf{
			VAD: VADConf{Type: "energy"},
		},
		LegacySelectedModule: map[string]string{
			"VAD": "SileroVADCommand",
		},
		LegacyVAD: map[string]VADConf{
			"SileroVADCommand": {
				Type:      "command",
				Command:   "python3",
				Args:      []string{"silero_vad_bridge.py", "{file}"},
				Env:       map[string]string{"SILERO_MODEL_DIR": "models/snakers4_silero-vad"},
				Threshold: 0.6,
			},
		},
	}

	conf.Normalize()

	if conf.Session.VAD.Type != "command" || conf.Session.VAD.Command != "python3" || conf.Session.VAD.Threshold != 0.6 {
		t.Fatalf("legacy selected command vad not normalized: %+v", conf.Session.VAD)
	}
	if len(conf.Session.VAD.Args) != 2 || conf.Session.VAD.Args[1] != "{file}" ||
		conf.Session.VAD.Env["SILERO_MODEL_DIR"] != "models/snakers4_silero-vad" {
		t.Fatalf("command vad args/env not preserved: %+v", conf.Session.VAD)
	}
}

func TestNormalizeLegacySelectedInfersMissingCustomAndCommandProviderTypes(t *testing.T) {
	conf := BizConf{
		ASR: ASRConf{Type: "file_stub"},
		TTS: TTSConf{Type: "stub"},
		Session: AudioSessionConf{
			VAD: VADConf{Type: "energy"},
		},
		LegacySelectedModule: map[string]string{
			"TTS": "CustomTTS",
			"ASR": "FunASRCommand",
			"VAD": "SileroVADCommand",
		},
		LegacyTTS: map[string]TTSConf{
			"CustomTTS": {
				LegacyURL: "http://127.0.0.1:9880/tts",
				Format:    "mp3",
				Params: map[string]any{
					"text": "{prompt_text}",
				},
			},
		},
		LegacyASR: map[string]ASRConf{
			"FunASRCommand": {
				Command: "python3",
				Args:    []string{"funasr_bridge.py", "{file}"},
			},
		},
		LegacyVAD: map[string]VADConf{
			"SileroVADCommand": {
				Command:   "python3",
				Args:      []string{"silero_vad_bridge.py", "{file}"},
				Threshold: 0.6,
			},
		},
	}

	conf.Normalize()

	if conf.TTS.Type != "custom" || conf.TTS.LegacyURL != "http://127.0.0.1:9880/tts" {
		t.Fatalf("legacy selected custom tts type not inferred: %+v", conf.TTS)
	}
	if conf.ASR.Type != "command" || conf.ASR.Command != "python3" {
		t.Fatalf("legacy selected command asr type not inferred: %+v", conf.ASR)
	}
	if conf.Session.VAD.Type != "command" || conf.Session.VAD.Command != "python3" || conf.Session.VAD.Threshold != 0.6 {
		t.Fatalf("legacy selected command vad type not inferred: %+v", conf.Session.VAD)
	}
}

func TestNormalizeLegacySelectedKeepsExplicitProviders(t *testing.T) {
	conf := BizConf{
		LLM: LLMConf{
			Type:    "openai",
			BaseURL: "https://new.example/v1",
			Model:   "new-model",
		},
		TTS: TTSConf{
			Type:   "openai",
			APIURL: "https://new.example/audio/speech",
			Model:  "new-tts",
			Voice:  "nova",
		},
		ASR: ASRConf{
			Type:  "openai",
			Model: "new-asr",
		},
		Session: AudioSessionConf{
			VAD: VADConf{Type: "energy", EnergyThreshold: 400},
		},
		LegacySelectedModule: map[string]string{
			"LLM": "LegacyLLM",
			"TTS": "LegacyTTS",
			"ASR": "LegacyASR",
			"VAD": "LegacyVAD",
		},
		LegacyLLM: map[string]LLMConf{
			"LegacyLLM": {
				Type:    "openai",
				BaseURL: "https://legacy.example/v1",
				Model:   "legacy-model",
			},
		},
		LegacyTTS: map[string]TTSConf{
			"LegacyTTS": {
				Type:  "openai",
				Model: "legacy-tts",
				Voice: "alloy",
			},
		},
		LegacyASR: map[string]ASRConf{
			"LegacyASR": {
				Type:  "openai",
				Model: "legacy-asr",
			},
		},
		LegacyVAD: map[string]VADConf{
			"LegacyVAD": {
				Type:            "energy",
				EnergyThreshold: 1000,
			},
		},
	}

	conf.Normalize()

	if conf.LLM.BaseURL != "https://new.example/v1" || conf.LLM.Model != "new-model" {
		t.Fatalf("explicit llm overwritten: %+v", conf.LLM)
	}
	if conf.TTS.APIURL != "https://new.example/audio/speech" || conf.TTS.Model != "new-tts" {
		t.Fatalf("explicit tts overwritten: %+v", conf.TTS)
	}
	if conf.ASR.Model != "new-asr" {
		t.Fatalf("explicit asr overwritten: %+v", conf.ASR)
	}
	if conf.Session.VAD.EnergyThreshold != 400 {
		t.Fatalf("explicit vad overwritten: %+v", conf.Session.VAD)
	}
}

func TestNormalizeLegacySelectedUnsupportedProvidersKeepGoDefaults(t *testing.T) {
	conf := BizConf{
		ASR: ASRConf{Type: "file_stub"},
		LLM: LLMConf{Type: "echo"},
		TTS: TTSConf{Type: "stub"},
		Memory: MemoryConf{
			Type: "none",
		},
		Session: AudioSessionConf{
			VAD: VADConf{Type: "energy", EnergyThreshold: 300},
		},
		LegacySelectedModule: map[string]string{
			"LLM":    "UnsupportedLLM",
			"TTS":    "EdgeTTS",
			"ASR":    "FunASR",
			"VAD":    "SileroVAD",
			"Memory": "mem0ai",
		},
		LegacyLLM: map[string]LLMConf{
			"UnsupportedLLM": {Type: "unsupported"},
		},
		LegacyTTS: map[string]TTSConf{
			"EdgeTTS": {Type: "edge", Voice: "zh-CN-XiaoxiaoNeural"},
		},
		LegacyASR: map[string]ASRConf{
			"FunASR": {Type: "fun_local", Model: "iic/SenseVoiceSmall"},
		},
		LegacyVAD: map[string]VADConf{
			"SileroVAD": {Type: "silero", EnergyThreshold: 1000},
		},
		LegacyMemory: map[string]MemoryConf{
			"mem0ai": {Type: "mem0ai"},
		},
	}

	conf.Normalize()

	if conf.LLM.Type != "echo" {
		t.Fatalf("unsupported legacy llm should keep echo: %+v", conf.LLM)
	}
	if conf.TTS.Type != "stub" {
		t.Fatalf("unsupported legacy tts should keep stub: %+v", conf.TTS)
	}
	if conf.ASR.Type != "file_stub" {
		t.Fatalf("unsupported legacy asr should keep file_stub: %+v", conf.ASR)
	}
	if conf.Session.VAD.Type != "energy" || conf.Session.VAD.EnergyThreshold != 300 {
		t.Fatalf("unsupported legacy vad should keep energy defaults: %+v", conf.Session.VAD)
	}
	if conf.Memory.Type != "none" {
		t.Fatalf("unsupported legacy memory should keep none: %+v", conf.Memory)
	}
}

func TestNormalizeLegacySelectedUnsupportedProvidersWithoutTypeKeepGoDefaults(t *testing.T) {
	conf := BizConf{
		ASR: ASRConf{Type: "file_stub"},
		LLM: LLMConf{Type: "echo"},
		TTS: TTSConf{Type: "stub"},
		Session: AudioSessionConf{
			VAD: VADConf{Type: "energy", EnergyThreshold: 300},
		},
		LegacySelectedModule: map[string]string{
			"LLM": "CozeLLM",
			"TTS": "EdgeTTS",
			"ASR": "FunASR",
			"VAD": "SileroVAD",
		},
		LegacyLLM: map[string]LLMConf{
			"CozeLLM": {
				BaseURL:         "https://api.coze.cn/open_api/v2/chat",
				LegacyModelName: "bot-id",
			},
		},
		LegacyTTS: map[string]TTSConf{
			"EdgeTTS": {
				Voice: "zh-CN-XiaoxiaoNeural",
			},
		},
		LegacyASR: map[string]ASRConf{
			"FunASR": {
				Model: "iic/SenseVoiceSmall",
			},
		},
		LegacyVAD: map[string]VADConf{
			"SileroVAD": {
				EnergyThreshold: 1000,
			},
		},
	}

	conf.Normalize()

	if conf.LLM.Type != "echo" {
		t.Fatalf("unsupported legacy llm without type should keep echo: %+v", conf.LLM)
	}
	if conf.TTS.Type != "stub" {
		t.Fatalf("unsupported legacy tts without type should keep stub: %+v", conf.TTS)
	}
	if conf.ASR.Type != "file_stub" {
		t.Fatalf("unsupported legacy asr without type should keep file_stub: %+v", conf.ASR)
	}
	if conf.Session.VAD.Type != "energy" || conf.Session.VAD.EnergyThreshold != 300 {
		t.Fatalf("unsupported legacy vad without type should keep energy defaults: %+v", conf.Session.VAD)
	}
}

func TestNormalizeLegacySelectedLocalMemory(t *testing.T) {
	conf := BizConf{
		Memory: MemoryConf{Type: "none"},
		LegacySelectedModule: map[string]string{
			"Memory": "mem_local_short",
		},
		LegacyMemory: map[string]MemoryConf{
			"mem_local_short": {Type: "mem_local_short"},
		},
	}

	conf.Normalize()

	if conf.Memory.Type != "local_short" {
		t.Fatalf("legacy local memory not normalized: %+v", conf.Memory)
	}
}

func TestNormalizeLegacySelectedIntentModeAndFunctions(t *testing.T) {
	conf := BizConf{
		LegacySelectedModule: map[string]string{
			"Intent": "function_call",
		},
		LegacyIntent: map[string]LegacyIntentModuleConf{
			"function_call": {
				Type:      "function_call",
				Functions: []string{"get_weather", "get_news"},
			},
		},
	}

	conf.Normalize()

	if conf.Intent.Mode != "function_call" {
		t.Fatalf("legacy selected intent mode got %q", conf.Intent.Mode)
	}
	if got, want := conf.Intent.Functions, []string{"get_weather", "get_news"}; !equalStrings(got, want) {
		t.Fatalf("legacy intent functions got %v want %v", got, want)
	}
}

func TestNormalizeLegacyIntentLLMUsesFunctionCallFunctionsLikePython(t *testing.T) {
	conf := BizConf{
		LegacySelectedModule: map[string]string{
			"Intent": "intent_llm",
		},
		LegacyIntent: map[string]LegacyIntentModuleConf{
			"intent_llm": {
				Type: "intent_llm",
				LLM:  "ChatGLMLLM",
			},
			"function_call": {
				Type:      "function_call",
				Functions: []string{"change_role", "get_weather", "get_news"},
			},
		},
	}

	conf.Normalize()

	if conf.Intent.Mode != "intent_llm" {
		t.Fatalf("legacy selected intent mode got %q", conf.Intent.Mode)
	}
	if got, want := conf.Intent.Functions, []string{"change_role", "get_weather", "get_news"}; !equalStrings(got, want) {
		t.Fatalf("legacy intent_llm functions got %v want %v", got, want)
	}
}

func TestApplyLegacySelectedProvidersOverrideIntent(t *testing.T) {
	conf := BizConf{
		Intent: IntentConf{
			Mode:      "function_call",
			Functions: []string{"explicit"},
		},
		LegacySelectedModule: map[string]string{
			"Intent": "intent_llm",
		},
		LegacyIntent: map[string]LegacyIntentModuleConf{
			"intent_llm": {
				Type: "intent_llm",
			},
			"function_call": {
				Type:      "function_call",
				Functions: []string{"change_role", "get_weather"},
			},
		},
	}

	ApplyLegacySelectedProvidersOverride(&conf)

	if conf.Intent.Mode != "intent_llm" {
		t.Fatalf("override intent mode got %q", conf.Intent.Mode)
	}
	if got, want := conf.Intent.Functions, []string{"change_role", "get_weather"}; !equalStrings(got, want) {
		t.Fatalf("override intent functions got %v want %v", got, want)
	}
}

func TestNormalizeKeepsExplicitIntentModeAndFunctions(t *testing.T) {
	conf := BizConf{
		Intent: IntentConf{
			Mode:      "nointent",
			Functions: []string{"explicit"},
		},
		LegacySelectedModule: map[string]string{
			"Intent": "function_call",
		},
		LegacyIntent: map[string]LegacyIntentModuleConf{
			"function_call": {
				Type:      "function_call",
				Functions: []string{"legacy"},
			},
		},
	}

	conf.Normalize()

	if conf.Intent.Mode != "nointent" {
		t.Fatalf("explicit intent mode overwritten: %q", conf.Intent.Mode)
	}
	if got, want := conf.Intent.Functions, []string{"explicit"}; !equalStrings(got, want) {
		t.Fatalf("explicit intent functions overwritten: %v", got)
	}
}

func TestNormalizeMemoryLegacyTypes(t *testing.T) {
	conf := BizConf{Memory: MemoryConf{Type: "mem_local_short"}}
	conf.Normalize()
	if conf.Memory.Type != "local_short" {
		t.Fatalf("mem_local_short not normalized: %+v", conf.Memory)
	}

	conf = BizConf{Memory: MemoryConf{Type: "nomem"}}
	conf.Normalize()
	if conf.Memory.Type != "none" {
		t.Fatalf("nomem not normalized: %+v", conf.Memory)
	}
}

func TestRuntimeDirectoriesIncludePythonOutputDirs(t *testing.T) {
	conf := BizConf{
		ASR: ASRConf{OutputDir: "tmp/asr"},
		TTS: TTSConf{OutputDir: "tmp/tts"},
		Session: AudioSessionConf{
			VAD: VADConf{OutputDir: "tmp/vad"},
		},
		LegacyASR: map[string]ASRConf{
			"FunASR": {OutputDir: "tmp/asr"},
			"Other":  {OutputDir: "/var/tmp/asr"},
		},
		LegacyTTS: map[string]TTSConf{
			"EdgeTTS": {OutputDir: "tmp/edge"},
		},
		LegacyVAD: map[string]VADConf{
			"SileroVAD": {OutputDir: "tmp/silero"},
		},
	}

	got := conf.runtimeDirectories(filepath.Join("data", ".config.yaml"))
	want := []string{
		"tmp",
		filepath.Clean("tmp/asr"),
		filepath.Clean("tmp/tts"),
		filepath.Clean("tmp/vad"),
		filepath.Clean("/var/tmp/asr"),
		filepath.Clean("tmp/edge"),
		filepath.Clean("tmp/silero"),
	}
	if !sameStringSet(got, want) {
		t.Fatalf("runtime directories got %v want %v", got, want)
	}
}

func TestEnsureRuntimeDirectoriesUsesMkdirAll(t *testing.T) {
	conf := BizConf{
		ASR: ASRConf{OutputDir: "tmp/asr"},
		TTS: TTSConf{OutputDir: "tmp/tts"},
	}
	var made []string

	warnings := conf.EnsureRuntimeDirectories("conf/biz.yaml", func(path string, mode os.FileMode) error {
		if mode != 0o755 {
			t.Fatalf("mkdir mode = %o, want 755", mode)
		}
		made = append(made, path)
		return nil
	})
	if len(warnings) != 0 {
		t.Fatalf("ensure runtime dirs warnings = %v, want none", warnings)
	}

	want := []string{"tmp", filepath.Clean("tmp/asr"), filepath.Clean("tmp/tts")}
	if !equalStrings(made, want) {
		t.Fatalf("mkdir dirs got %v want %v", made, want)
	}
}

func TestEnsureRuntimeDirectoriesReturnsWarnings(t *testing.T) {
	conf := BizConf{ASR: ASRConf{OutputDir: "tmp/asr"}}
	warnings := conf.EnsureRuntimeDirectories("conf/biz.yaml", func(path string, _ os.FileMode) error {
		if path == "tmp" {
			return fmt.Errorf("permission denied")
		}
		return nil
	})
	if len(warnings) != 1 {
		t.Fatalf("warnings got %v want one", warnings)
	}
	if !strings.Contains(warnings[0].Error(), `create runtime directory "tmp"`) {
		t.Fatalf("unexpected warning: %v", warnings[0])
	}
}

func TestRuntimeBaseDirUsesPythonProjectRoot(t *testing.T) {
	tests := []struct {
		name       string
		configFile string
		want       string
	}{
		{name: "private config", configFile: filepath.Join("data", ".config.yaml"), want: "."},
		{name: "go conf", configFile: filepath.Join("conf", "biz.yaml"), want: "."},
		{name: "absolute conf", configFile: filepath.Join(string(filepath.Separator), "repo", "conf", "biz.yaml"), want: filepath.Join(string(filepath.Separator), "repo")},
		{name: "root config", configFile: "config.yaml", want: "."},
		{name: "custom nested", configFile: filepath.Join("custom", "config.yaml"), want: "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runtimeBaseDir(tt.configFile); got != filepath.Clean(tt.want) {
				t.Fatalf("runtimeBaseDir(%q) = %q, want %q", tt.configFile, got, filepath.Clean(tt.want))
			}
		})
	}
}

func TestReloadConfigCreatesRuntimeDirectories(t *testing.T) {
	old := conf
	oldPath := configFileUsed
	defer func() {
		conf = old
		configFileUsed = oldPath
	}()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
server:
  port: 8109
xiaozhi:
  format: opus
  transport: websocket
llm:
  type: echo
session:
  vad:
    type: command
    command: python3
    output_dir: tmp/vad
selected_module:
  ASR: FunASR
  TTS: EdgeTTS
ASR:
  FunASR:
    type: command
    command: python3
    output_dir: tmp/legacy_asr
TTS:
  EdgeTTS:
    type: command
    command: python3
    output_dir: tmp/legacy_tts
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	withWorkingDir(t, dir, func() {
		if err := ReloadConfig(); err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if conf.ASR.OutputDir != "tmp/legacy_asr" || conf.TTS.OutputDir != "tmp/legacy_tts" || conf.Session.VAD.OutputDir != "tmp/vad" {
			t.Fatalf("output dirs not decoded: asr=%q tts=%q vad=%q warnings=%v", conf.ASR.OutputDir, conf.TTS.OutputDir, conf.Session.VAD.OutputDir, RuntimeWarnings())
		}
		for _, path := range []string{
			"tmp",
			filepath.Join("tmp", "vad"),
			filepath.Join("tmp", "legacy_asr"),
			filepath.Join("tmp", "legacy_tts"),
		} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat runtime dir %q: %v", path, err)
			}
			if !info.IsDir() {
				t.Fatalf("runtime path %q is not a directory", path)
			}
		}
	})
}

func TestNormalizePlayMusicDefaults(t *testing.T) {
	conf := BizConf{}
	conf.Normalize()
	if conf.Plugins.PlayMusic.MusicDir != "./music" {
		t.Fatalf("music dir default = %q", conf.Plugins.PlayMusic.MusicDir)
	}
	if got, want := conf.Plugins.PlayMusic.MusicExt, []string{".mp3", ".wav", ".p3"}; !equalStrings(got, want) {
		t.Fatalf("music ext default got %v want %v", got, want)
	}
	if conf.Plugins.PlayMusic.RefreshTime != 60 {
		t.Fatalf("refresh time default = %d", conf.Plugins.PlayMusic.RefreshTime)
	}
	if conf.Plugins.GetNews.DefaultRSSURL != "https://www.chinanews.com.cn/rss/society.xml" {
		t.Fatalf("news default rss = %q", conf.Plugins.GetNews.DefaultRSSURL)
	}
	if conf.Plugins.GetNews.CategoryURLs["finance"] == "" {
		t.Fatalf("news category urls not initialized: %+v", conf.Plugins.GetNews.CategoryURLs)
	}
	if conf.Plugins.GetWeather.DefaultLocation != "广州" {
		t.Fatalf("weather default location = %q", conf.Plugins.GetWeather.DefaultLocation)
	}
	if conf.Plugins.GetWeather.BaseURL != "https://restapi.amap.com" {
		t.Fatalf("weather base url = %q", conf.Plugins.GetWeather.BaseURL)
	}
	if conf.Plugins.BaiduSearch.SearchURL != "https://www.baidu.com/s" {
		t.Fatalf("baidu search url = %q", conf.Plugins.BaiduSearch.SearchURL)
	}
	if conf.Plugins.BaiduSearch.MaxResults != 10 {
		t.Fatalf("baidu max results = %d", conf.Plugins.BaiduSearch.MaxResults)
	}
	if conf.Plugins.BaiduSearch.MaxExtractPages != 3 {
		t.Fatalf("baidu max extract pages = %d", conf.Plugins.BaiduSearch.MaxExtractPages)
	}
	if conf.Plugins.BaiduSearch.MaxContentLength != 1000 {
		t.Fatalf("baidu max content length = %d", conf.Plugins.BaiduSearch.MaxContentLength)
	}
	if conf.MCP.Path != "data/.mcp_server_settings.json" {
		t.Fatalf("mcp path = %q", conf.MCP.Path)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()
	fn()
}

func minimalConfigYAML(port int, prompt string) []byte {
	return []byte(fmt.Sprintf(`
server:
  port: %d
xiaozhi:
  format: opus
  transport: websocket
asr:
  type: file_stub
llm:
  type: echo
tts:
  type: stub
local:
  prompt: %q
`, port, prompt))
}
