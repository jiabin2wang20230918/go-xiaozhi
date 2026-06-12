package main

import (
	"log"

	"github.com/xdimtech/go-xiaozhi/handler"
	_ "github.com/xdimtech/go-xiaozhi/pkg/config"
)

func main() {
	server := handler.NewWebSocketServer()
	if err := server.Start(":80"); err != nil {
		log.Fatal("Error starting server:", err)
	}
}
