package voice

import (
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

// 本文件实现模型实例的"预加载 + 跨会话共享"。
//
// 背景：sherpa ASR(229M)/TTS(311M) 模型加载耗时约 2-4s。若每次 WebSocket 连接
// 都重新加载（NewPipelineFromConfig → NewASR/NewTTS → NewSherpaASR/NewSherpaTTS），
// 会造成连接延迟与 CPU 尖峰（ESP32 等设备频繁重连时尤甚）。
//
// 由于推理本身是只读的（SherpaASR/SherpaTTS 已加 mu 保护推理串行化），
// 同一份配置下的模型实例可被所有会话安全共享。这里按配置内容缓存 ASR/TTS，
// LLM 是无状态 HTTP 客户端（加载零成本，不缓存），VAD 有会话级状态（不在此缓存）。
//
// 私有配置（private_config.enabled=true）下不同设备可能用不同 ASR/TTS 配置，
// 故缓存键是配置内容而非"全局唯一"——同一配置命中缓存，不同配置各自加载。

var (
	sharedModelsOnce sync.Once
	sharedASR        ASR
	sharedTTS        TTS
	sharedKey        string
	sharedMu         sync.RWMutex
)

// cacheKey 汇总影响 sherpa ASR/TTS 模型加载的配置字段，作为缓存键。
// 仅当这些字段变化时才需要重新加载模型。
func sherpaCacheKey(asr config.ASRConf, tts config.TTSConf) string {
	return asr.Type + "|" + asr.ModelDir + "|" + asr.Language + "||" +
		tts.Type + "|" + tts.ModelDir + "|" + tts.Voice + "|" +
		strconv.Itoa(tts.NumThreads)
}

// sharedModels 返回当前配置对应的共享 ASR/TTS（首次调用时加载并缓存）。
// 若配置 key 与已缓存的不同（私有配置切设备），按新 key 重新加载。
// 不会返回 nil：回退实现（FileASR/StubTTS）也参与缓存。
func sharedModels(asrConf config.ASRConf, ttsConf config.TTSConf) (ASR, TTS) {
	key := sherpaCacheKey(asrConf, ttsConf)

	sharedMu.RLock()
	if sharedKey == key && sharedASR != nil && sharedTTS != nil {
		asr, tts := sharedASR, sharedTTS
		sharedMu.RUnlock()
		return asr, tts
	}
	sharedMu.RUnlock()

	sharedMu.Lock()
	defer sharedMu.Unlock()
	// 双检：可能在等锁期间另一 goroutine 已加载好。
	if sharedKey == key && sharedASR != nil && sharedTTS != nil {
		return sharedASR, sharedTTS
	}
	log.Printf("loading shared ASR/TTS models (key=%s)...", key)
	asr := NewASR(asrConf)
	tts := NewTTS(ttsConf)
	sharedASR = asr
	sharedTTS = tts
	sharedKey = key
	log.Printf("shared ASR/TTS models ready")
	return asr, tts
}

// NewPipelineWithSharedModels 用共享的 ASR/TTS（按配置缓存）+ 新建 LLM 构造 Pipeline。
// 签名与 NewPipelineFromConfig 对齐，便于直接替换。VAD 不在此处理（每会话独立、有状态）。
// memory 由调用方 WithMemory 附加。相比 NewPipelineFromConfig，复用已加载的 sherpa 模型。
func NewPipelineWithSharedModels(asr config.ASRConf, llm config.LLMConf, tts config.TTSConf) *Pipeline {
	sharedASR, sharedTTS := sharedModels(asr, tts)
	p := &Pipeline{
		asr:           sharedASR,
		llm:           NewLLM(llm),
		tts:           sharedTTS,
		splitMaxRunes: tts.SplitMaxChars,
	}
	if tts.TimeoutSeconds > 0 {
		p.ttsTimeout = time.Duration(tts.TimeoutSeconds) * time.Second
	}
	return p
}

// PreloadSharedModels 在服务启动时预加载共享模型（阻塞至加载完成），
// 使首个连接不必等待模型加载。多次调用安全（sync.Once 语义由 sharedModels 保证）。
func PreloadSharedModels(asr config.ASRConf, tts config.TTSConf) {
	sharedModelsOnce.Do(func() {
		sharedModels(asr, tts)
	})
}
