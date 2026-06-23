package voice

import (
	"context"
	"log"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func NewVAD(conf config.VADConf) VAD {
	switch conf.Type {
	case "", "energy":
		return EnergyVAD{Threshold: vadThreshold(conf)}
	case "non_empty":
		return NonEmptyVAD{}
	case "command":
		return &CommandVAD{
			OutputDir:  conf.OutputDir,
			Command:    conf.Command,
			Args:       append([]string(nil), conf.Args...),
			Env:        cloneStringMap(conf.Env),
			Threshold:  vadThreshold(conf),
			SampleRate: conf.SampleRate,
			Channels:   conf.Channels,
		}
	case "silero":
		vad, err := NewSherpaVAD(conf)
		if err != nil {
			log.Printf("sherpa silero vad unavailable (%v); falling back to energy vad", err)
			return EnergyVAD{Threshold: vadThreshold(conf)}
		}
		return vad
	default:
		return EnergyVAD{Threshold: vadThreshold(conf)}
	}
}

func vadThreshold(conf config.VADConf) float64 {
	if conf.Threshold > 0 {
		return conf.Threshold
	}
	return conf.EnergyThreshold
}

type NonEmptyVAD struct{}

func (NonEmptyVAD) HasVoice(ctx context.Context, frame AudioFrame) (bool, error) {
	if frame.PCM != nil {
		return len(frame.PCM.Samples) > 0, nil
	}
	return len(frame.Opus) > 0, nil
}
