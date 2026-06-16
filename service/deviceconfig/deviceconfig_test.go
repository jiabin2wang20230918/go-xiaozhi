package deviceconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"gopkg.in/yaml.v3"
)

func TestLoadOrCreateCreatesDefaultDeviceConfig(t *testing.T) {
	defaults := testDefaults()
	path := filepath.Join(t.TempDir(), ".private_config.yaml")
	store := NewStore(path, &defaults)

	runtime, err := store.LoadOrCreate(context.Background(), "device-1")
	if err != nil {
		t.Fatalf("load or create: %v", err)
	}
	if !runtime.Enabled {
		t.Fatal("runtime should be marked enabled")
	}
	if runtime.Local.Prompt != defaults.Local.Prompt || runtime.LLM.Type != defaults.LLM.Type {
		t.Fatalf("runtime did not inherit defaults: %+v", runtime)
	}
	if runtime.SentenceDelay.BaseDelayMs != defaults.SentenceDelay.BaseDelayMs {
		t.Fatalf("runtime did not inherit sentence delay: %+v", runtime.SentenceDelay)
	}
	if runtime.Device.AuthCode == "" || len(runtime.Device.AuthCode) != 6 {
		t.Fatalf("auth code not generated: %q", runtime.Device.AuthCode)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read private config: %v", err)
	}
	if !strings.Contains(string(data), "device-1") {
		t.Fatalf("private config did not include device: %s", data)
	}
	created := map[string]config.DevicePrivateConf{}
	if err := yaml.Unmarshal(data, &created); err != nil {
		t.Fatalf("unmarshal created private config: %v", err)
	}
	device := created["device-1"]
	if got := device.SelectedModule["LLM"]; got != "default" {
		t.Fatalf("selected_module LLM got %q want default", got)
	}
	if got := device.SelectedModule["Intent"]; got != "default" {
		t.Fatalf("selected_module Intent got %q want default", got)
	}
	if device.LegacyLLM["default"].Type != defaults.LLM.Type ||
		device.LegacyTTS["default"].Type != defaults.TTS.Type ||
		device.LegacyASR["default"].Type != defaults.ASR.Type ||
		device.LegacyVAD["default"].Type != defaults.Session.VAD.Type {
		t.Fatalf("python legacy provider tables not written: %+v", device)
	}
	if device.LegacyIntent["default"].Type != defaults.Intent.Mode ||
		!equalStrings(device.LegacyIntent["default"].Functions, defaults.Intent.Functions) {
		t.Fatalf("python legacy intent table not written: %+v", device.LegacyIntent)
	}
	if device.Local != nil || device.LLM != nil || device.TTS != nil || device.ASR != nil || device.Intent != nil {
		t.Fatalf("new private config should keep Python legacy shape, got Go patch fields: %+v", device)
	}
}

func TestLoadOrCreateUsesPythonSelectedModuleNames(t *testing.T) {
	defaults := testDefaults()
	defaults.LegacySelectedModule = map[string]string{
		"LLM":    "ChatGLMLLM",
		"TTS":    "EdgeTTS",
		"ASR":    "FunASR",
		"VAD":    "SileroVAD",
		"Intent": "function_call",
	}
	defaults.LegacyLLM = map[string]config.LLMConf{
		"ChatGLMLLM": {Type: "openai", BaseURL: "http://llm"},
	}
	defaults.LegacyTTS = map[string]config.TTSConf{
		"EdgeTTS": {Type: "command", Voice: "zh-CN-XiaoxiaoNeural"},
	}
	defaults.LegacyASR = map[string]config.ASRConf{
		"FunASR": {Type: "command", Model: "iic/SenseVoiceSmall"},
	}
	defaults.LegacyVAD = map[string]config.VADConf{
		"SileroVAD": {Type: "command", EnergyThreshold: 0.5},
	}
	defaults.Intent.Mode = "function_call"
	defaults.Intent.Functions = []string{"get_weather", "get_news"}

	path := filepath.Join(t.TempDir(), ".private_config.yaml")
	store := NewStore(path, &defaults)
	if _, err := store.LoadOrCreate(context.Background(), "device-1"); err != nil {
		t.Fatalf("load or create: %v", err)
	}

	created := map[string]config.DevicePrivateConf{}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read private config: %v", err)
	}
	if err := yaml.Unmarshal(data, &created); err != nil {
		t.Fatalf("unmarshal created private config: %v", err)
	}
	device := created["device-1"]
	if got, want := device.SelectedModule["LLM"], "ChatGLMLLM"; got != want {
		t.Fatalf("selected_module LLM got %q want %q", got, want)
	}
	if got, want := device.SelectedModule["TTS"], "EdgeTTS"; got != want {
		t.Fatalf("selected_module TTS got %q want %q", got, want)
	}
	if got, want := device.SelectedModule["ASR"], "FunASR"; got != want {
		t.Fatalf("selected_module ASR got %q want %q", got, want)
	}
	if got, want := device.SelectedModule["VAD"], "SileroVAD"; got != want {
		t.Fatalf("selected_module VAD got %q want %q", got, want)
	}
	if got, want := device.SelectedModule["Intent"], "function_call"; got != want {
		t.Fatalf("selected_module Intent got %q want %q", got, want)
	}
	if _, ok := device.LegacyLLM["ChatGLMLLM"]; !ok {
		t.Fatalf("legacy LLM table did not use selected provider name: %+v", device.LegacyLLM)
	}
	if _, ok := device.LegacyTTS["EdgeTTS"]; !ok {
		t.Fatalf("legacy TTS table did not use selected provider name: %+v", device.LegacyTTS)
	}
	if _, ok := device.LegacyASR["FunASR"]; !ok {
		t.Fatalf("legacy ASR table did not use selected provider name: %+v", device.LegacyASR)
	}
	if _, ok := device.LegacyVAD["SileroVAD"]; !ok {
		t.Fatalf("legacy VAD table did not use selected provider name: %+v", device.LegacyVAD)
	}
	if got := device.LegacyIntent["function_call"].Functions; !equalStrings(got, defaults.Intent.Functions) {
		t.Fatalf("legacy intent functions got %v want %v", got, defaults.Intent.Functions)
	}
}

func TestMergeSupportsLegacySelectedModuleShape(t *testing.T) {
	defaults := testDefaults()
	device := config.DevicePrivateConf{
		SelectedModule: map[string]string{
			"LLM": "openai-main",
			"TTS": "stub-private",
			"ASR": "asr-private",
			"VAD": "vad-private",
		},
		Prompt: "private prompt",
		LegacyLLM: map[string]config.LLMConf{
			"openai-main": {Type: "openai", LegacyURL: "http://llm", LegacyModelName: "m1"},
		},
		LegacyTTS: map[string]config.TTSConf{
			"stub-private": {Type: "openai", LegacyURL: "http://tts", Model: "tts-1", Voice: "alloy"},
		},
		LegacyASR: map[string]config.ASRConf{
			"asr-private": {Type: "file_stub", StubTranscript: "private transcript"},
		},
		LegacyVAD: map[string]config.VADConf{
			"vad-private": {Type: "energy", EnergyThreshold: 0.9},
		},
	}

	runtime := Merge(&defaults, device)
	if runtime.Local.Prompt != "private prompt" {
		t.Fatalf("prompt got %q", runtime.Local.Prompt)
	}
	if runtime.LLM.Type != "openai" || runtime.LLM.BaseURL != "http://llm" || runtime.LLM.Model != "m1" {
		t.Fatalf("llm not merged: %+v", runtime.LLM)
	}
	if runtime.TTS.Type != "openai" || runtime.TTS.APIURL != "http://tts" {
		t.Fatalf("tts not merged: %+v", runtime.TTS)
	}
	if runtime.ASR.StubTranscript != "private transcript" {
		t.Fatalf("asr not merged: %+v", runtime.ASR)
	}
	if runtime.Session.VAD.EnergyThreshold != 0.9 {
		t.Fatalf("vad not merged: %+v", runtime.Session.VAD)
	}
}

func TestMergeInfersPrivateLegacySelectedProviderTypesWhenMissing(t *testing.T) {
	defaults := testDefaults()
	device := config.DevicePrivateConf{
		SelectedModule: map[string]string{
			"LLM": "ChatGLMLLM",
			"TTS": "OpenAITTS",
			"ASR": "OpenAIASR",
			"VAD": "EnergyVAD",
		},
		LegacyLLM: map[string]config.LLMConf{
			"ChatGLMLLM": {
				LegacyURL:       "https://open.bigmodel.cn/api/paas/v4/",
				LegacyModelName: "glm-4-flash",
			},
		},
		LegacyTTS: map[string]config.TTSConf{
			"OpenAITTS": {
				LegacyURL: "https://api.example/audio/speech",
				Model:     "tts-1",
				Voice:     "alloy",
			},
		},
		LegacyASR: map[string]config.ASRConf{
			"OpenAIASR": {
				Model: "whisper-1",
			},
		},
		LegacyVAD: map[string]config.VADConf{
			"EnergyVAD": {
				EnergyThreshold: 0.9,
			},
		},
	}

	runtime := Merge(&defaults, device)
	if runtime.LLM.Type != "openai" || runtime.LLM.BaseURL != "https://open.bigmodel.cn/api/paas/v4/" ||
		runtime.LLM.Model != "glm-4-flash" {
		t.Fatalf("private legacy llm type not inferred: %+v", runtime.LLM)
	}
	if runtime.TTS.Type != "openai" || runtime.TTS.APIURL != "https://api.example/audio/speech" ||
		runtime.TTS.Model != "tts-1" || runtime.TTS.Voice != "alloy" {
		t.Fatalf("private legacy tts type not inferred: %+v", runtime.TTS)
	}
	if runtime.ASR.Type != "openai" || runtime.ASR.Model != "whisper-1" {
		t.Fatalf("private legacy asr type not inferred: %+v", runtime.ASR)
	}
	if runtime.Session.VAD.Type != "energy" || runtime.Session.VAD.EnergyThreshold != 0.9 {
		t.Fatalf("private legacy vad type not inferred: %+v", runtime.Session.VAD)
	}
}

func TestMergeSupportsLegacySelectedCustomTTS(t *testing.T) {
	defaults := testDefaults()
	device := config.DevicePrivateConf{
		SelectedModule: map[string]string{
			"TTS": "CustomTTS",
		},
		LegacyTTS: map[string]config.TTSConf{
			"CustomTTS": {
				Type:      "custom",
				LegacyURL: "http://127.0.0.1:9880/tts",
				Format:    "wav",
				Params: map[string]any{
					"text": "{prompt_text}",
				},
			},
		},
	}

	runtime := Merge(&defaults, device)
	if runtime.TTS.Type != "custom" || runtime.TTS.LegacyURL != "http://127.0.0.1:9880/tts" {
		t.Fatalf("custom tts not merged: %+v", runtime.TTS)
	}
	if runtime.TTS.Params["text"] != "{prompt_text}" {
		t.Fatalf("custom tts params not preserved: %+v", runtime.TTS.Params)
	}
}

func TestMergeSupportsLegacySelectedIntentLLMShape(t *testing.T) {
	defaults := testDefaults()
	defaults.Intent.Mode = "function_call"
	defaults.Intent.Functions = []string{"play_music"}
	device := config.DevicePrivateConf{
		SelectedModule: map[string]string{
			"Intent": "intent_llm",
		},
		LegacyIntent: map[string]config.LegacyIntentModuleConf{
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

	runtime := Merge(&defaults, device)
	if runtime.Intent.Mode != "intent_llm" {
		t.Fatalf("legacy selected intent mode got %q", runtime.Intent.Mode)
	}
	if got, want := runtime.Intent.Functions, []string{"change_role", "get_weather", "get_news"}; !equalStrings(got, want) {
		t.Fatalf("legacy selected intent functions got %v want %v", got, want)
	}
}

func TestMergeExplicitPatchSectionsOverrideLegacySelectedModules(t *testing.T) {
	defaults := testDefaults()
	device := config.DevicePrivateConf{
		SelectedModule: map[string]string{
			"LLM": "legacy-llm",
			"TTS": "legacy-tts",
			"ASR": "legacy-asr",
		},
		LegacyLLM: map[string]config.LLMConf{
			"legacy-llm": {Type: "openai", BaseURL: "http://legacy-llm", Model: "legacy-model"},
		},
		LegacyTTS: map[string]config.TTSConf{
			"legacy-tts": {Type: "openai", APIURL: "http://legacy-tts", Model: "tts-1", Voice: "alloy"},
		},
		LegacyASR: map[string]config.ASRConf{
			"legacy-asr": {Type: "openai", Model: "legacy-asr"},
		},
		LLM: &config.LLMConf{Type: "openai", BaseURL: "http://patch-llm", Model: "patch-model"},
		TTS: &config.TTSConf{Type: "stub", DurationMs: 240},
		ASR: &config.ASRConf{Type: "file_stub", StubTranscript: "patch transcript"},
	}

	runtime := Merge(&defaults, device)
	if runtime.LLM.BaseURL != "http://patch-llm" || runtime.LLM.Model != "patch-model" {
		t.Fatalf("explicit llm patch did not override legacy selected module: %+v", runtime.LLM)
	}
	if runtime.TTS.Type != "stub" || runtime.TTS.DurationMs != 240 {
		t.Fatalf("explicit tts patch did not override legacy selected module: %+v", runtime.TTS)
	}
	if runtime.ASR.StubTranscript != "patch transcript" {
		t.Fatalf("explicit asr patch did not override legacy selected module: %+v", runtime.ASR)
	}
}

func TestMergePrivateSectionsPatchDefaults(t *testing.T) {
	defaults := testDefaults()
	enableGreeting := false
	device := config.DevicePrivateConf{
		Local: &config.LocalProviderConf{
			NoVoicePrompt: "private no voice",
		},
		Session: &config.AudioSessionConf{
			NoVoiceCloseSeconds: 30,
		},
		Intent: &config.IntentConf{
			EnableGreeting: &enableGreeting,
			Mode:           "function_call",
			Functions:      []string{"get_weather"},
			LunarCommand:   "python3",
			LunarArgs:      []string{"scripts/lunar.py", "{query}"},
		},
	}

	runtime := Merge(&defaults, device)
	if runtime.Local.Prompt != defaults.Local.Prompt {
		t.Fatalf("local prompt should keep default, got %q", runtime.Local.Prompt)
	}
	if runtime.Local.NoVoicePrompt != "private no voice" {
		t.Fatalf("local no voice prompt not patched: %+v", runtime.Local)
	}
	if runtime.Session.MinFrames != defaults.Session.MinFrames ||
		runtime.Session.PreBufferFrames != defaults.Session.PreBufferFrames ||
		runtime.Session.OpusSampleRate != defaults.Session.OpusSampleRate ||
		runtime.Session.VAD.EnergyThreshold != defaults.Session.VAD.EnergyThreshold {
		t.Fatalf("session defaults lost during patch merge: %+v", runtime.Session)
	}
	if runtime.Session.NoVoiceCloseSeconds != 30 {
		t.Fatalf("session no voice close not patched: %+v", runtime.Session)
	}
	if got, want := runtime.Intent.ExitCommands, defaults.Intent.ExitCommands; !equalStrings(got, want) {
		t.Fatalf("intent exit commands got %v want %v", got, want)
	}
	if runtime.Intent.EnableGreeting == nil || *runtime.Intent.EnableGreeting {
		t.Fatalf("intent enable greeting not patched: %+v", runtime.Intent.EnableGreeting)
	}
	if !runtime.Intent.WakeupResponseCache || runtime.Intent.WakeupResponseCacheDir != defaults.Intent.WakeupResponseCacheDir {
		t.Fatalf("intent defaults lost during patch merge: %+v", runtime.Intent)
	}
	if runtime.Intent.Mode != "function_call" {
		t.Fatalf("intent mode not patched: %+v", runtime.Intent)
	}
	if got, want := runtime.Intent.Functions, []string{"get_weather"}; !equalStrings(got, want) {
		t.Fatalf("intent functions not patched: %v", got)
	}
	if runtime.Intent.LunarCommand != "python3" || !equalStrings(runtime.Intent.LunarArgs, []string{"scripts/lunar.py", "{query}"}) {
		t.Fatalf("intent lunar command not patched: %+v", runtime.Intent)
	}
}

func TestUpdateLastChatTime(t *testing.T) {
	defaults := testDefaults()
	path := filepath.Join(t.TempDir(), ".private_config.yaml")
	all := map[string]config.DevicePrivateConf{
		"device-1": {Owner: "alice", Prompt: "private"},
	}
	data, err := yaml.Marshal(all)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	store := NewStore(path, &defaults)
	ts := time.Unix(12345, 0)
	if err := store.UpdateLastChatTime(context.Background(), "device-1", ts); err != nil {
		t.Fatalf("update last chat time: %v", err)
	}
	loaded, err := store.loadAll()
	if err != nil {
		t.Fatalf("load updated config: %v", err)
	}
	if loaded["device-1"].LastChatTime != 12345 {
		t.Fatalf("last chat time got %d", loaded["device-1"].LastChatTime)
	}
}

func testDefaults() config.BizConf {
	enableGreeting := true
	return config.BizConf{
		Local: config.LocalProviderConf{Prompt: "default prompt", WelcomeMessage: "welcome", NoVoicePrompt: "default no voice"},
		LLM:   config.LLMConf{Type: "echo", Echo: config.EchoLLMConf{WelcomeMessage: "hi"}},
		TTS:   config.TTSConf{Type: "stub", SampleRate: 16000, Channels: 1},
		ASR:   config.ASRConf{Type: "file_stub", StubTranscript: "default transcript"},
		Session: config.AudioSessionConf{
			MinFrames:           15,
			PreBufferFrames:     10,
			NoVoiceCloseSeconds: 120,
			SilenceStopMs:       700,
			OpusSampleRate:      16000,
			OpusChannels:        1,
			OpusFrameSize:       960,
			VAD:                 config.VADConf{Type: "energy", EnergyThreshold: 0.01},
		},
		Intent: config.IntentConf{
			ExitCommands:               []string{"退出"},
			WakeupWords:                []string{"小智"},
			EnableGreeting:             &enableGreeting,
			WakeupResponseCache:        true,
			WakeupResponseCacheDir:     "config/assets",
			WakeupResponseCacheMinSize: 15360,
		},
		SentenceDelay: config.SentenceDelayConf{
			Enabled:             true,
			BaseDelayMs:         200,
			Dynamic:             true,
			LengthThreshold:     30,
			LongSentenceExtraMs: 300,
		},
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
