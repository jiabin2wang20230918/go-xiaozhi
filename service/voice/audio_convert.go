package voice

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
