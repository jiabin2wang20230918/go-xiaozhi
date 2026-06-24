package voice

import "runtime"

// defaultNumThreads 是 sherpa 适配器（VAD/ASR/TTS）的默认推理线程数。
// 实测 Kokoro fp32 在 4 线程下 RTF≈0.53（近 2 倍实时），是单会话速度与
// 多会话并发容量的平衡点。配置项 num_threads 可覆盖。
const defaultNumThreads = 4

// resolveNumThreads 把配置的线程数规范化：<=0 时取默认值，>0 时按 CPU 核数上限
// 截断，避免误配过大线程池拖累并发。
func resolveNumThreads(n int) int {
	if n <= 0 {
		return defaultNumThreads
	}
	if max := runtime.NumCPU(); n > max {
		return max
	}
	return n
}

// pcmInt16ToFloat32 converts 16-bit PCM samples to normalized float32 in the
// range [-1, 1], as required by sherpa-onnx (SileroVAD / offline recognizers).
func pcmInt16ToFloat32(samples []int16) []float32 {
	if len(samples) == 0 {
		return nil
	}
	out := make([]float32, len(samples))
	const scale = 1.0 / 32768.0
	for i, s := range samples {
		out[i] = float32(s) * scale
	}
	return out
}
