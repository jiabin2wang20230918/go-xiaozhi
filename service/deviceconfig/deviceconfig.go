package deviceconfig

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"gopkg.in/yaml.v3"
)

type Runtime struct {
	Local         config.LocalProviderConf
	LLM           config.LLMConf
	TTS           config.TTSConf
	ASR           config.ASRConf
	Session       config.AudioSessionConf
	Intent        config.IntentConf
	SentenceDelay config.SentenceDelayConf
	Plugins       config.PluginsConf
	MCP           config.MCPConf
	Device        config.DevicePrivateConf
	Enabled       bool
}

type Store struct {
	Path    string
	Default *config.BizConf

	mu sync.Mutex
}

func NewStore(path string, defaults *config.BizConf) *Store {
	return &Store{Path: path, Default: defaults}
}

func DisabledRuntime(defaults *config.BizConf) Runtime {
	return Runtime{
		Local:         defaults.Local,
		LLM:           defaults.LLM,
		TTS:           defaults.TTS,
		ASR:           defaults.ASR,
		Session:       defaults.Session,
		Intent:        defaults.Intent,
		SentenceDelay: defaults.SentenceDelay,
		Plugins:       defaults.Plugins,
		MCP:           defaults.MCP,
	}
}

func (s *Store) LoadOrCreate(ctx context.Context, deviceID string) (Runtime, error) {
	if strings.TrimSpace(deviceID) == "" {
		return DisabledRuntime(s.Default), nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := s.loadAll()
	if err != nil {
		return Runtime{}, err
	}
	device, ok := all[deviceID]
	if !ok {
		device = defaultDeviceConfig(s.Default)
		device.AuthCode = generateAuthCode(6)
		all[deviceID] = device
		if err := s.saveAll(all); err != nil {
			return Runtime{}, err
		}
	}
	runtime := Merge(s.Default, device)
	runtime.Enabled = true
	return runtime, nil
}

func (s *Store) UpdateLastChatTime(ctx context.Context, deviceID string, ts time.Time) error {
	if strings.TrimSpace(deviceID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := s.loadAll()
	if err != nil {
		return err
	}
	device, ok := all[deviceID]
	if !ok {
		return fmt.Errorf("private config for device %q not found", deviceID)
	}
	device.LastChatTime = ts.Unix()
	all[deviceID] = device
	return s.saveAll(all)
}

func Merge(defaults *config.BizConf, device config.DevicePrivateConf) Runtime {
	runtime := DisabledRuntime(defaults)
	runtime.Device = device
	if strings.TrimSpace(device.Prompt) != "" {
		runtime.Local.Prompt = device.Prompt
	}
	applyLegacySelectedProviders(&runtime, device)
	if device.Local != nil {
		runtime.Local = mergeLocal(runtime.Local, *device.Local)
	}
	if device.LLM != nil {
		runtime.LLM = *device.LLM
	}
	if device.TTS != nil {
		runtime.TTS = *device.TTS
	}
	if device.ASR != nil {
		runtime.ASR = *device.ASR
	}
	if device.Session != nil {
		runtime.Session = mergeSession(runtime.Session, *device.Session)
	}
	if device.Intent != nil {
		runtime.Intent = mergeIntent(runtime.Intent, *device.Intent)
	}
	runtime.LLM.Normalize()
	runtime.TTS.Normalize()
	return runtime
}

func applyLegacySelectedProviders(runtime *Runtime, device config.DevicePrivateConf) {
	if runtime == nil || len(device.SelectedModule) == 0 {
		return
	}
	legacy := config.BizConf{
		LLM:                  runtime.LLM,
		TTS:                  runtime.TTS,
		ASR:                  runtime.ASR,
		Session:              runtime.Session,
		Memory:               config.MemoryConf{Type: "local_short"},
		LegacySelectedModule: device.SelectedModule,
		LegacyLLM:            device.LegacyLLM,
		LegacyTTS:            device.LegacyTTS,
		LegacyASR:            device.LegacyASR,
		LegacyVAD:            device.LegacyVAD,
		LegacyIntent:         device.LegacyIntent,
	}
	config.ApplyLegacySelectedProvidersOverride(&legacy)
	config.ApplyLegacySelectedProviders(&legacy)
	runtime.LLM = legacy.LLM
	runtime.TTS = legacy.TTS
	runtime.ASR = legacy.ASR
	runtime.Session.VAD = legacy.Session.VAD
	runtime.Intent = legacy.Intent
}

func mergeLocal(base config.LocalProviderConf, patch config.LocalProviderConf) config.LocalProviderConf {
	if strings.TrimSpace(patch.Prompt) != "" {
		base.Prompt = patch.Prompt
	}
	if strings.TrimSpace(patch.WelcomeMessage) != "" {
		base.WelcomeMessage = patch.WelcomeMessage
	}
	if patch.EchoTranscripts {
		base.EchoTranscripts = patch.EchoTranscripts
	}
	if strings.TrimSpace(patch.NoVoicePrompt) != "" {
		base.NoVoicePrompt = patch.NoVoicePrompt
	}
	return base
}

func mergeSession(base config.AudioSessionConf, patch config.AudioSessionConf) config.AudioSessionConf {
	if patch.MinFrames > 0 {
		base.MinFrames = patch.MinFrames
	}
	if patch.PreBufferFrames > 0 {
		base.PreBufferFrames = patch.PreBufferFrames
	}
	if patch.NoVoiceCloseSeconds > 0 {
		base.NoVoiceCloseSeconds = patch.NoVoiceCloseSeconds
	}
	if patch.SilenceStopMs > 0 {
		base.SilenceStopMs = patch.SilenceStopMs
	}
	if patch.OpusSampleRate > 0 {
		base.OpusSampleRate = patch.OpusSampleRate
	}
	if patch.OpusChannels > 0 {
		base.OpusChannels = patch.OpusChannels
	}
	if patch.OpusFrameSize > 0 {
		base.OpusFrameSize = patch.OpusFrameSize
	}
	if patch.OpusGain > 0 {
		base.OpusGain = patch.OpusGain
	}
	base.VAD = mergeVAD(base.VAD, patch.VAD)
	return base
}

func mergeVAD(base config.VADConf, patch config.VADConf) config.VADConf {
	if strings.TrimSpace(patch.Type) != "" {
		base.Type = patch.Type
	}
	if patch.EnergyThreshold > 0 {
		base.EnergyThreshold = patch.EnergyThreshold
	}
	return base
}

func mergeIntent(base config.IntentConf, patch config.IntentConf) config.IntentConf {
	if len(patch.ExitCommands) > 0 {
		base.ExitCommands = append([]string(nil), patch.ExitCommands...)
	}
	if len(patch.WakeupWords) > 0 {
		base.WakeupWords = append([]string(nil), patch.WakeupWords...)
	}
	if patch.EnableGreeting != nil {
		enabled := *patch.EnableGreeting
		base.EnableGreeting = &enabled
	}
	if patch.WakeupResponseCache {
		base.WakeupResponseCache = patch.WakeupResponseCache
	}
	if strings.TrimSpace(patch.WakeupResponseCacheDir) != "" {
		base.WakeupResponseCacheDir = patch.WakeupResponseCacheDir
	}
	if strings.TrimSpace(patch.WakeupResponseCacheText) != "" {
		base.WakeupResponseCacheText = patch.WakeupResponseCacheText
	}
	if patch.WakeupResponseCacheMinSize > 0 {
		base.WakeupResponseCacheMinSize = patch.WakeupResponseCacheMinSize
	}
	if strings.TrimSpace(patch.Mode) != "" {
		base.Mode = patch.Mode
	}
	if len(patch.Functions) > 0 {
		base.Functions = append([]string(nil), patch.Functions...)
	}
	if strings.TrimSpace(patch.LunarCommand) != "" {
		base.LunarCommand = patch.LunarCommand
	}
	if len(patch.LunarArgs) > 0 {
		base.LunarArgs = append([]string(nil), patch.LunarArgs...)
	}
	return base
}

func (s *Store) loadAll() (map[string]config.DevicePrivateConf, error) {
	data, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]config.DevicePrivateConf{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]config.DevicePrivateConf{}, nil
	}
	all := map[string]config.DevicePrivateConf{}
	if err := yaml.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	return all, nil
}

func (s *Store) saveAll(all map[string]config.DevicePrivateConf) error {
	if err := os.MkdirAll(filepath.Dir(s.path()), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(all)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(), data, 0o644)
}

func (s *Store) path() string {
	path := strings.TrimSpace(s.Path)
	if path == "" {
		return filepath.Join("data", ".private_config.yaml")
	}
	return path
}

func defaultDeviceConfig(defaults *config.BizConf) config.DevicePrivateConf {
	llmName := selectedProviderName(defaults, "LLM")
	ttsName := selectedProviderName(defaults, "TTS")
	asrName := selectedProviderName(defaults, "ASR")
	vadName := selectedProviderName(defaults, "VAD")
	intentName := selectedProviderName(defaults, "Intent")

	return config.DevicePrivateConf{
		SelectedModule: map[string]string{
			"LLM":    llmName,
			"TTS":    ttsName,
			"ASR":    asrName,
			"VAD":    vadName,
			"Intent": intentName,
		},
		Prompt: defaults.Local.Prompt,
		LegacyLLM: map[string]config.LLMConf{
			llmName: selectedLLM(defaults, llmName),
		},
		LegacyTTS: map[string]config.TTSConf{
			ttsName: selectedTTS(defaults, ttsName),
		},
		LegacyASR: map[string]config.ASRConf{
			asrName: selectedASR(defaults, asrName),
		},
		LegacyVAD: map[string]config.VADConf{
			vadName: selectedVAD(defaults, vadName),
		},
		LegacyIntent: map[string]config.LegacyIntentModuleConf{
			intentName: {
				Type:      defaults.Intent.Mode,
				Functions: append([]string(nil), defaults.Intent.Functions...),
			},
		},
	}
}

func selectedProviderName(defaults *config.BizConf, module string) string {
	if defaults != nil {
		if name := strings.TrimSpace(defaults.LegacySelectedModule[module]); name != "" {
			return name
		}
	}
	return "default"
}

func selectedLLM(defaults *config.BizConf, name string) config.LLMConf {
	if defaults != nil {
		if candidate, ok := defaults.LegacyLLM[name]; ok {
			return candidate
		}
		return defaults.LLM
	}
	return config.LLMConf{}
}

func selectedTTS(defaults *config.BizConf, name string) config.TTSConf {
	if defaults != nil {
		if candidate, ok := defaults.LegacyTTS[name]; ok {
			return candidate
		}
		return defaults.TTS
	}
	return config.TTSConf{}
}

func selectedASR(defaults *config.BizConf, name string) config.ASRConf {
	if defaults != nil {
		if candidate, ok := defaults.LegacyASR[name]; ok {
			return candidate
		}
		return defaults.ASR
	}
	return config.ASRConf{}
}

func selectedVAD(defaults *config.BizConf, name string) config.VADConf {
	if defaults != nil {
		if candidate, ok := defaults.LegacyVAD[name]; ok {
			return candidate
		}
		return defaults.Session.VAD
	}
	return config.VADConf{}
}

func generateAuthCode(length int) string {
	if length <= 0 {
		return ""
	}
	var b strings.Builder
	for b.Len() < length {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return fallbackAuthCode(length)
		}
		b.WriteByte(byte('0' + n.Int64()))
	}
	return b.String()
}

func fallbackAuthCode(length int) string {
	now := time.Now().UnixNano()
	code := fmt.Sprintf("%0*d", length, now)
	if len(code) > length {
		return code[len(code)-length:]
	}
	return code
}
