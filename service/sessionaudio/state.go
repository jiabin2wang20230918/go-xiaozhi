package sessionaudio

import (
	"context"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/voice"
)

type Result struct {
	Ready          bool
	NoVoiceTimeout bool
	Frames         []voice.AudioFrame
}

type State struct {
	vad             voice.VAD
	decoder         Decoder
	mode            string
	haveVoice       bool
	voiceStopped    bool
	lastVoiceAt     time.Time
	asrReceiving    bool
	noVoiceSince    time.Time
	noVoiceTimeout  time.Duration
	silenceStop     time.Duration
	preBufferFrames int
	minFrames       int
	now             func() time.Time
	frames          []voice.AudioFrame
}

type Decoder interface {
	Decode(packet []byte) (audio.PCMFrame, error)
}

func New(vad voice.VAD, conf config.AudioSessionConf) *State {
	if vad == nil {
		vad = voice.NonEmptyVAD{}
	}
	if conf.PreBufferFrames <= 0 {
		conf.PreBufferFrames = 10
	}
	if conf.MinFrames <= 0 {
		conf.MinFrames = 15
	}
	if conf.NoVoiceCloseSeconds < 0 {
		conf.NoVoiceCloseSeconds = 0
	}
	if conf.SilenceStopMs <= 0 {
		conf.SilenceStopMs = 700
	}
	if conf.OpusSampleRate <= 0 {
		conf.OpusSampleRate = 16000
	}
	if conf.OpusChannels <= 0 {
		conf.OpusChannels = 1
	}
	if conf.OpusFrameSize <= 0 {
		conf.OpusFrameSize = conf.OpusSampleRate * 60 / 1000
	}
	decoder, _ := audio.NewOpusDecoder(audio.OpusDecoderConfig{
		SampleRate: conf.OpusSampleRate,
		Channels:   conf.OpusChannels,
		FrameSize:  conf.OpusFrameSize,
		Gain:       conf.OpusGain,
	})
	return &State{
		vad:             vad,
		decoder:         decoder,
		mode:            "auto",
		asrReceiving:    true,
		noVoiceTimeout:  time.Duration(conf.NoVoiceCloseSeconds) * time.Second,
		silenceStop:     time.Duration(conf.SilenceStopMs) * time.Millisecond,
		preBufferFrames: conf.PreBufferFrames,
		minFrames:       conf.MinFrames,
		now:             time.Now,
	}
}

func NewWithDecoder(vad voice.VAD, decoder Decoder, conf config.AudioSessionConf) *State {
	state := New(vad, conf)
	state.decoder = decoder
	return state
}

func (s *State) SetMode(mode string) {
	if mode != "" {
		s.mode = mode
	}
}

func (s *State) Start() {
	s.haveVoice = true
	s.voiceStopped = false
	s.lastVoiceAt = s.currentTime()
	s.noVoiceSince = time.Time{}
	s.trimPreBuffer()
}

func (s *State) Stop() Result {
	s.haveVoice = true
	s.voiceStopped = true
	return s.finishIfReady()
}

func (s *State) Detect() {
	s.asrReceiving = false
	s.haveVoice = false
	s.voiceStopped = false
	s.lastVoiceAt = time.Time{}
	s.frames = s.frames[:0]
}

func (s *State) Resume() {
	s.asrReceiving = true
}

func (s *State) Abort() {
	s.frames = nil
	s.haveVoice = false
	s.voiceStopped = false
	s.lastVoiceAt = time.Time{}
	s.asrReceiving = true
}

func (s *State) Push(ctx context.Context, frame []byte) (Result, error) {
	if !s.asrReceiving {
		return Result{}, nil
	}
	if len(frame) == 0 {
		return s.finishIfReady(), nil
	}

	audioFrame, err := s.decodeFrame(frame)
	if err != nil {
		return Result{}, err
	}

	haveVoice := s.haveVoice
	if s.mode == "auto" {
		haveVoice, err = s.vad.HasVoice(ctx, audioFrame)
		if err != nil {
			return Result{}, err
		}
	}

	if !haveVoice && !s.haveVoice {
		now := s.currentTime()
		if s.noVoiceSince.IsZero() {
			s.noVoiceSince = now
		} else if s.noVoiceTimeout > 0 && now.Sub(s.noVoiceSince) > s.noVoiceTimeout {
			s.asrReceiving = false
			s.frames = s.frames[:0]
			return Result{NoVoiceTimeout: true}, nil
		}
		s.appendFrame(audioFrame)
		s.trimPreBuffer()
		return Result{}, nil
	}

	s.noVoiceSince = time.Time{}
	if haveVoice {
		s.haveVoice = true
		s.lastVoiceAt = s.currentTime()
	} else if s.haveVoice && s.silenceStop > 0 && !s.lastVoiceAt.IsZero() &&
		s.currentTime().Sub(s.lastVoiceAt) >= s.silenceStop {
		s.voiceStopped = true
	}
	s.appendFrame(audioFrame)
	if s.voiceStopped {
		return s.finishIfReady(), nil
	}
	return Result{}, nil
}

func (s *State) currentTime() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

func (s *State) decodeFrame(frame []byte) (voice.AudioFrame, error) {
	result := voice.AudioFrame{Opus: append([]byte(nil), frame...)}
	if s.decoder == nil {
		return result, nil
	}
	pcm, err := s.decoder.Decode(frame)
	if err != nil {
		return voice.AudioFrame{}, err
	}
	result.PCM = &pcm
	return result, nil
}

func (s *State) appendFrame(frame voice.AudioFrame) {
	if frame.Opus != nil {
		frame.Opus = append([]byte(nil), frame.Opus...)
	}
	if frame.PCM != nil {
		pcm := *frame.PCM
		pcm.Samples = append([]int16(nil), frame.PCM.Samples...)
		frame.PCM = &pcm
	}
	s.frames = append(s.frames, frame)
}

func (s *State) trimPreBuffer() {
	if len(s.frames) > s.preBufferFrames {
		s.frames = append([]voice.AudioFrame(nil), s.frames[len(s.frames)-s.preBufferFrames:]...)
	}
}

func (s *State) finishIfReady() Result {
	if len(s.frames) < s.minFrames {
		s.frames = s.frames[:0]
		s.voiceStopped = false
		s.asrReceiving = true
		return Result{}
	}
	frames := append([]voice.AudioFrame(nil), s.frames...)
	s.frames = s.frames[:0]
	s.haveVoice = false
	s.voiceStopped = false
	s.asrReceiving = false
	return Result{Ready: true, Frames: frames}
}
