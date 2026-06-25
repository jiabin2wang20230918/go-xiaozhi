package main

import (
	"log"

	"github.com/xdimtech/go-xiaozhi/handler"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/voice"
)

func main() {
	if err := runPreflightChecks(); err != nil {
		log.Fatal(err)
	}

	// 预加载共享 ASR/TTS 模型（sherpa 加载耗时数秒），避免首个连接等待、
	// 并消除设备重连时的 CPU 尖峰。配置缺失则内部回退（FileASR/StubTTS）。
	conf := config.Get()
	voice.PreloadSharedModels(conf.ASR, conf.TTS)

	server := handler.NewWebSocketServer()
	if err := server.Start(""); err != nil {
		log.Fatal("Error starting server:", err)
	}
}
