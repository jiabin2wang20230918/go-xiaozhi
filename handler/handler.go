package handler

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/xdimtech/go-xiaozhi/handler/local"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/auth"

	"github.com/gorilla/websocket"
	"github.com/xdimtech/go-xiaozhi/pkg/utils"
)

type WebSocketServer struct {
	requestCounter atomic.Int64
	auth           *auth.Authenticator
	mux            *http.ServeMux
}

func NewWebSocketServer() *WebSocketServer {
	server := &WebSocketServer{
		auth: auth.New(config.Get().Server.Auth),
	}
	server.mux = http.NewServeMux()
	server.mux.HandleFunc("/xiaozhi/v1/", server.RealTime)
	return server
}

func (s *WebSocketServer) Handler() http.Handler {
	if s.mux != nil {
		return s.mux
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/xiaozhi/v1/", s.RealTime)
	return mux
}

func (s *WebSocketServer) Start(addr string) error {
	if addr == "" {
		addr = fmt.Sprintf("%s:%d", config.Server().IP, config.Server().Port)
	}
	conf := config.Get()
	log.Printf("Config loaded from: %s", config.GetConfigFilePath())
	log.Printf("Providers: asr=%s llm=%s tts=%s vad=%s intent=%s", conf.ASR.Type, conf.LLM.Type, conf.TTS.Type, conf.Session.VAD.Type, conf.Intent.Mode)
	log.Printf("Server started at local: %s\n", websocketURL("127.0.0.1", addr))
	ip, _ := utils.GetLocalIP()
	log.Printf("Server started at public: %s\n", websocketURL(ip, addr))
	server := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
	}
	return server.ListenAndServe()
}

func websocketURL(host string, listenAddr string) string {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return fmt.Sprintf("ws://%s/xiaozhi/v1/", strings.TrimRight(host, "/"))
	}
	return fmt.Sprintf("ws://%s:%s/xiaozhi/v1/", host, port)
}

func (s *WebSocketServer) wsConnect(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	upgrader := &websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			return true
			// 开发环境下允许所有来源
			// 生产环境建议使用更严格的检查:
			// origin := r.Header.Get("Origin")
			// return origin == "http://localhost:8080" ||
			//        origin == "http://your-allowed-domain.com"
		},
	}
	conn, err := upgrader.Upgrade(w, r, w.Header())
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (s *WebSocketServer) RealTime(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Authenticate(r); err != nil {
		log.Printf("authentication failed remote=%s device-id=%s error=%v", r.RemoteAddr, r.Header.Get("Device-Id"), err)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var err error
	conn, err := s.wsConnect(w, r)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	log.Printf("websocket connected remote=%s device-id=%s client-id=%s", r.RemoteAddr, r.Header.Get("Device-Id"), r.Header.Get("Client-Id"))

	defer func() {
		_ = conn.Close()
		conn = nil
	}()

	ctx := r.Context()
	connWrapper, err := local.NewConnWrapper(ctx, conn, local.ClientInfoFromRequest(r))
	if err != nil {
		log.Printf("connection initialization failed: %v", err)
		_ = conn.Close()
		return
	}

	_ = connWrapper.ReadLoop(ctx)
}
