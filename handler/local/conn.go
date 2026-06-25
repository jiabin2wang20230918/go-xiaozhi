package local

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
	"github.com/xdimtech/go-xiaozhi/service/memory"
)

const pingInterval = time.Second

var errInvalidEventFormat = errors.New("invalid event format")

type ConnWrapper struct {
	ctx              context.Context
	conn             *websocket.Conn
	handler          *Handler
	done             chan struct{}
	doneOnce         sync.Once
	writeMessageFunc func(messageType int, data []byte) error
}

type ClientInfo struct {
	DeviceID string
	ClientID string
	RemoteIP string
	Headers  http.Header
}

func ClientInfoFromRequest(r *http.Request) ClientInfo {
	return ClientInfo{
		DeviceID: r.Header.Get("Device-Id"),
		ClientID: r.Header.Get("Client-Id"),
		RemoteIP: remoteIPFromAddr(r.RemoteAddr),
		Headers:  r.Header.Clone(),
	}
}

func remoteIPFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return addr
	}
	return host
}

func NewConnWrapper(ctx context.Context, conn *websocket.Conn, info ClientInfo) (*ConnWrapper, error) {
	handler, err := NewHandlerWithMemory(ctx, info, memory.New(config.Get().Memory))
	if err != nil {
		return nil, err
	}
	w := &ConnWrapper{
		ctx:              ctx,
		conn:             conn,
		handler:          handler,
		done:             make(chan struct{}),
		writeMessageFunc: conn.WriteMessage,
	}
	conn.SetPingHandler(w.Pong)
	go w.WriteLoop(ctx)
	go w.WatchIdle(ctx)
	return w, nil
}

func (w *ConnWrapper) Ping(data string) error {
	if w.conn == nil {
		return nil
	}
	return w.conn.WriteControl(websocket.PingMessage, []byte(data), time.Now().Add(time.Second))
}

func (w *ConnWrapper) Pong(data string) error {
	if w.conn == nil {
		return nil
	}
	return w.conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
}

func (w *ConnWrapper) Close() error {
	if w.conn != nil {
		_ = w.conn.Close()
	}
	return w.handler.Close(w.ctx)
}

func (w *ConnWrapper) WriteLoop(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	audioFlow := newAudioFlowController(config.Get().AudioFlow)

	for {
		select {
		case event, ok := <-w.handler.Recv(ctx):
			if !ok {
				return
			}
			if _, ok := xiaozhiapi.IsServerEvent(event); ok {
				// 每段语音开始时重置流控，避免 position/sent 跨段累积导致后续段节流失效
				// （否则第二段起音频瞬间灌入设备缓冲，播放断续）。
				if tts, ok := event.(*xiaozhiapi.ServerEventTTS); ok && tts.State == xiaozhiapi.ServerTTSStateSentenceStart {
					audioFlow.reset()
				}
				buf, err := w.handler.MarshalServerEvent(event)
				if err == nil {
					if err := w.writeMessage(websocket.TextMessage, buf); err != nil {
						w.signalDone()
						return
					}
				}
				continue
			}
			if frame, ok := event.([]byte); ok {
				audioFlow.beforeBinaryFrame()
				if err := w.writeMessage(websocket.BinaryMessage, frame); err != nil {
					w.signalDone()
					return
				}
				continue
			}
			if text, ok := event.(string); ok {
				if err := w.writeMessage(websocket.TextMessage, []byte(text)); err != nil {
					w.signalDone()
					return
				}
			}
		case <-w.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Ping("done"); err != nil {
				w.signalDone()
				return
			}
		}
	}
}

func (w *ConnWrapper) writeMessage(messageType int, data []byte) error {
	if w.writeMessageFunc != nil {
		return w.writeMessageFunc(messageType, data)
	}
	if w.conn == nil {
		return errors.New("websocket connection is nil")
	}
	return w.conn.WriteMessage(messageType, data)
}

func (w *ConnWrapper) ReadLoop(ctx context.Context) error {
	defer func() { _ = w.Close() }()

	for {
		msgType, msg, err := w.conn.ReadMessage()
		if err != nil {
			w.signalDone()
			return err
		}

		var event any
		switch msgType {
		case websocket.TextMessage:
			event, err = w.handler.UnmarshalClientTextEvent(msg)
		case websocket.BinaryMessage:
			event, err = w.handler.UnmarshalClientBinEvent(msg)
		default:
			continue
		}
		if err != nil {
			continue
		}

		err, quit := w.handler.DispatchClientEvent(ctx, event)
		if err != nil {
			continue
		}
		if quit {
			w.signalDone()
			return err
		}
	}
}

func (w *ConnWrapper) WatchIdle(ctx context.Context) {
	<-w.handler.Done()
	w.signalDone()
}

func (w *ConnWrapper) signalDone() {
	w.doneOnce.Do(func() {
		close(w.done)
	})
}
