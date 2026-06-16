package main

import (
	"log"

	"github.com/xdimtech/go-xiaozhi/handler"
	_ "github.com/xdimtech/go-xiaozhi/pkg/config"
)

func main() {
	if err := runPreflightChecks(); err != nil {
		log.Fatal(err)
	}

	server := handler.NewWebSocketServer()
	if err := server.Start(""); err != nil {
		log.Fatal("Error starting server:", err)
	}
}
