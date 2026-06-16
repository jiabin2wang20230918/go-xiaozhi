package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/auth"
)

func TestRealTimeRejectsUnauthorizedRequestBeforeUpgrade(t *testing.T) {
	server := &WebSocketServer{
		auth: auth.New(config.AuthConf{
			Enabled: true,
			Tokens:  []config.AuthToken{{Token: "secret", Name: "device"}},
		}),
	}
	req := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	rec := httptest.NewRecorder()

	server.RealTime(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestNewWebSocketServerUsesPrivateMux(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("creating multiple websocket servers should not panic: %v", r)
		}
	}()
	first := NewWebSocketServer()
	second := NewWebSocketServer()

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	first.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("first private mux missing route got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec = httptest.NewRecorder()
	second.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second private mux missing route got %d", rec.Code)
	}
}

func TestWebSocketURLUsesPortFromListenAddress(t *testing.T) {
	tests := map[string]struct {
		host       string
		listenAddr string
		want       string
	}{
		"any host": {
			host:       "127.0.0.1",
			listenAddr: "0.0.0.0:8000",
			want:       "ws://127.0.0.1:8000/xiaozhi/v1/",
		},
		"empty host listen": {
			host:       "192.168.1.10",
			listenAddr: ":9000",
			want:       "ws://192.168.1.10:9000/xiaozhi/v1/",
		},
		"explicit loopback": {
			host:       "127.0.0.1",
			listenAddr: "127.0.0.1:7000",
			want:       "ws://127.0.0.1:7000/xiaozhi/v1/",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := websocketURL(tt.host, tt.listenAddr); got != tt.want {
				t.Fatalf("websocketURL(%q, %q) = %q, want %q", tt.host, tt.listenAddr, got, tt.want)
			}
		})
	}
}

func TestRealTimeHandlesUpgradeFailureWithoutPanic(t *testing.T) {
	server := &WebSocketServer{auth: auth.New(config.AuthConf{Enabled: false})}
	req := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	rec := httptest.NewRecorder()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RealTime should not panic on upgrade failure: %v", r)
		}
	}()
	server.RealTime(rec, req)
}
