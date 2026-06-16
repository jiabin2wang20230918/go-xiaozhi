package local

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
)

func TestSignalDoneClosesDoneChannelOnce(t *testing.T) {
	w := &ConnWrapper{done: make(chan struct{})}

	w.signalDone()
	w.signalDone()

	select {
	case <-w.done:
	default:
		t.Fatal("done channel should be closed")
	}
}

func TestWriteLoopSignalsDoneOnWriteFailure(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	defer func() { _ = h.Close(context.Background()) }()
	<-h.Recv(context.Background())
	w := &ConnWrapper{
		ctx:     context.Background(),
		handler: h,
		done:    make(chan struct{}),
		writeMessageFunc: func(messageType int, data []byte) error {
			return errors.New("write failed")
		},
	}
	go w.WriteLoop(context.Background())

	if err := h.write(&xiaozhiapi.ServerEventTTS{
		ServerEventBase: xiaozhiapi.ServerEventBase{
			Type:      xiaozhiapi.ServerEventTypeTTS,
			SessionId: "session-1",
		},
		State: xiaozhiapi.ServerTTSStateStop,
	}); err != nil {
		t.Fatalf("queue event: %v", err)
	}

	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("write failure should close done channel")
	}
}

func TestWriteMessageReturnsErrorWhenConnectionMissing(t *testing.T) {
	w := &ConnWrapper{}
	if err := w.writeMessage(websocket.TextMessage, []byte("hello")); err == nil {
		t.Fatal("expected missing connection error")
	}
}

func TestClientInfoFromRequestUsesRemoteHostLikePython(t *testing.T) {
	tests := map[string]struct {
		remoteAddr string
		want       string
	}{
		"ipv4 host port": {
			remoteAddr: "192.168.1.5:45678",
			want:       "192.168.1.5",
		},
		"ipv6 host port": {
			remoteAddr: "[2001:db8::1]:45678",
			want:       "2001:db8::1",
		},
		"missing port": {
			remoteAddr: "192.168.1.5",
			want:       "192.168.1.5",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set("Device-Id", "device-1")
			req.Header.Set("Client-Id", "client-1")

			info := ClientInfoFromRequest(req)
			if info.RemoteIP != tt.want {
				t.Fatalf("RemoteIP got %q want %q", info.RemoteIP, tt.want)
			}
			if info.DeviceID != "device-1" || info.ClientID != "client-1" {
				t.Fatalf("client headers not preserved: %+v", info)
			}
		})
	}
}
