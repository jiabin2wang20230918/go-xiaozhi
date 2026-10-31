package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
)

// newNotifyTestServer 构造启用了 notify 通道的 WebSocketServer，
// 状态文件落在临时目录（绝对路径），鉴权默认关闭。
func newNotifyTestServer(t *testing.T, authConf config.AuthConf) *WebSocketServer {
	t.Helper()
	defaults := *config.Get()
	defaults.Server.Auth = authConf
	defaults.Server.Notify = config.ServerNotifyConf{
		Enabled:   true,
		StatePath: t.TempDir() + "/notify_state.json",
	}
	// 关闭 Dashboard 天气推送：这些用例只验证通知通道，不应触发外部 HTTP。
	defaults.Plugins.GetWeather = config.GetWeatherConf{DefaultLocation: "深圳"}
	restore := config.Replace(defaults)
	t.Cleanup(restore)
	return NewWebSocketServer()
}

// notifyTestServer 起一个真实 HTTP 服务承载 WebSocket 升级，返回 ws 地址。
func notifyTestServer(t *testing.T, s *WebSocketServer) string {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return fmt.Sprintf("ws://%s/xiaozhi/v1/", u.Host)
}

// testWebSocket 是测试端 WebSocket 客户端，缓冲收到的文本消息。
type testWebSocket struct {
	t    *testing.T
	conn *websocket.Conn
}

func newTestWebSocket(t *testing.T, s *WebSocketServer, deviceID string) *testWebSocket {
	t.Helper()
	header := http.Header{}
	header.Set("Device-Id", deviceID)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.DialContext(t.Context(), notifyTestServer(t, s), header)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	w := &testWebSocket{t: t, conn: conn}
	t.Cleanup(func() { _ = conn.Close() })
	// 注意：不要在这里起后台读 goroutine——断言（waitForServerEvent）需要独占读。
	// 服务端 ping 由 gorilla 在读路径自动回 pong；写队列（1024）足够测试事件量。
	return w
}

// SendText 发送一条文本帧。
func (w *testWebSocket) SendText(t *testing.T, text string) error {
	t.Helper()
	if err := w.conn.WriteMessage(websocket.TextMessage, []byte(text)); err != nil {
		return err
	}
	return nil
}

func (w *testWebSocket) Close() error {
	return w.conn.Close()
}

func doNotify(t *testing.T, s *WebSocketServer, token string, body any) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatalf("encode body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/notify", &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	data, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(data)
}

func getNotifyStatus(t *testing.T, s *WebSocketServer, token, deviceID, notifyID string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/devices/"+deviceID+"/notify/"+notifyID, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	data, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(data)
}

func TestNotifyEndpointRequiresAuth(t *testing.T) {
	s := newNotifyTestServer(t, config.AuthConf{
		Enabled: true,
		Tokens:  []config.AuthToken{{Token: "secret", Name: "grok"}},
	})
	if code, body := doNotify(t, s, "", map[string]any{"device_id": "dev-1", "id": "n-1", "content": "c"}); code != http.StatusUnauthorized {
		t.Fatalf("no token: status got %d want 401, body=%s", code, body)
	}
	if code, body := doNotify(t, s, "wrong", map[string]any{"device_id": "dev-1", "id": "n-1", "content": "c"}); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status got %d want 401, body=%s", code, body)
	}
	// 正确 token：受理（设备离线 → pending）。
	if code, body := doNotify(t, s, "secret", map[string]any{"device_id": "dev-1", "id": "n-1", "content": "c"}); code != http.StatusOK {
		t.Fatalf("valid token: status got %d want 200, body=%s", code, body)
	}
}

// TestNotifyEndToEnd 验证完整回路：离线入库 → 上线补发 → 在线直投 →
// 设备回执 → 已 ack 不再重播。
func TestNotifyEndToEnd(t *testing.T) {
	s := newNotifyTestServer(t, config.AuthConf{Enabled: false})

	// 1) 设备离线：POST → pending。
	code, body := doNotify(t, s, "", map[string]any{
		"device_id": "dev-1", "id": "n-1", "category": "reminder",
		"title": "项目会议", "content": "15分钟后开始项目会议。",
		"priority": "normal", "speak": false,
	})
	if code != http.StatusOK || !strings.Contains(body, `"state":"pending"`) {
		t.Fatalf("offline notify: %d %s want 200 pending", code, body)
	}

	// 2) 状态查询：pending；未知 id：404。
	code, body = getNotifyStatus(t, s, "", "dev-1", "n-1")
	if code != http.StatusOK || !strings.Contains(body, `"state":"pending"`) {
		t.Fatalf("status: %d %s want 200 pending", code, body)
	}
	if code, body = getNotifyStatus(t, s, "", "dev-1", "nope"); code != http.StatusNotFound {
		t.Fatalf("unknown id: %d %s want 404", code, body)
	}

	// 3) 设备上线：应补发离线 pending（notification 事件）。
	ws := newTestWebSocket(t, s, "dev-1")
	defer ws.Close()
	if _, err := waitForServerEvent(ws, xiaozhiapi.ServerEventTypeNotification, "n-1", 3*time.Second); err != nil {
		t.Fatalf("flushed notification: %v", err)
	}

	// 4) 在线直投：POST → delivered + 事件。
	if code, body = doNotify(t, s, "", map[string]any{"device_id": "dev-1", "id": "n-2", "content": "在线提醒", "speak": false}); code != http.StatusOK || !strings.Contains(body, `"state":"delivered"`) {
		t.Fatalf("online notify: %d %s want 200 delivered", code, body)
	}
	if _, err := waitForServerEvent(ws, xiaozhiapi.ServerEventTypeNotification, "n-2", 3*time.Second); err != nil {
		t.Fatalf("online notification: %v", err)
	}

	// 5) 设备回执 ack：状态查询转为 acked。
	if err := ws.SendText(t, `{"type":"notification_ack","id":"n-2","action":"ack","timestamp":1793334005}`); err != nil {
		t.Fatalf("send ack: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, body = getNotifyStatus(t, s, "", "dev-1", "n-2")
		if code == http.StatusOK && strings.Contains(body, `"state":"acked"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status after ack: %d %s want acked", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 6) 已 ack 的 id 再次 POST：返回 acked（幂等去重）。
	if code, body = doNotify(t, s, "", map[string]any{"device_id": "dev-1", "id": "n-2", "content": "重发", "speak": false}); code != http.StatusOK || !strings.Contains(body, `"state":"acked"`) {
		t.Fatalf("resend acked: %d %s want 200 acked", code, body)
	}
}

// TestNotifyReconnectResendsPending 验证：已下发未回执的 pending 在重连后补发，
// 回执后不再补发。
func TestNotifyReconnectResendsPending(t *testing.T) {
	s := newNotifyTestServer(t, config.AuthConf{Enabled: false})

	// 上线投递一条，不 ack。
	ws1 := newTestWebSocket(t, s, "dev-1")
	if _, err := waitForServerEvent(ws1, xiaozhiapi.ServerEventTypeHello, "", 3*time.Second); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if code, body := doNotify(t, s, "", map[string]any{"device_id": "dev-1", "id": "n-1", "content": "待回执提醒", "speak": false}); code != http.StatusOK || !strings.Contains(body, `"state":"delivered"`) {
		t.Fatalf("notify: %d %s", code, body)
	}
	if _, err := waitForServerEvent(ws1, xiaozhiapi.ServerEventTypeNotification, "n-1", 3*time.Second); err != nil {
		t.Fatalf("first notification: %v", err)
	}
	// 断开。
	_ = ws1.Close()
	// 等待服务端感知断开并解绑。
	deadline := time.Now().Add(3 * time.Second)
	for {
		if dev, ok := s.notifyService().Lookup("dev-1"); ok && dev != nil {
			if time.Now().After(deadline) {
				t.Fatal("session not unbound after close")
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		break
	}

	// 重连：未回执的 n-1 应补发。
	ws2 := newTestWebSocket(t, s, "dev-1")
	defer ws2.Close()
	if _, err := waitForServerEvent(ws2, xiaozhiapi.ServerEventTypeNotification, "n-1", 3*time.Second); err != nil {
		t.Fatalf("reconnect flush: %v", err)
	}

	// 回执后再次重连：不再补发。
	if err := ws2.SendText(t, `{"type":"notification_ack","id":"n-1","action":"ack"}`); err != nil {
		t.Fatalf("send ack: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		code, body := getNotifyStatus(t, s, "", "dev-1", "n-1")
		if code == http.StatusOK && strings.Contains(body, `"state":"acked"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status: %d %s want acked", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = ws2.Close()
	ws3 := newTestWebSocket(t, s, "dev-1")
	defer ws3.Close()
	if _, err := waitForServerEvent(ws3, xiaozhiapi.ServerEventTypeNotification, "n-1", 1500*time.Millisecond); err == nil {
		t.Fatal("acked notification must not be re-flushed after reconnect")
	}
}

// waitForServerEvent 在测试 WebSocket 上等待一条 type 匹配（且 id 匹配，id 可为空）
// 的服务端事件。返回该事件；超时返回错误。
func waitForServerEvent(ws *testWebSocket, typ xiaozhiapi.ServerEventType, id string, timeout time.Duration) (any, error) {
	ws.conn.SetReadDeadline(time.Now().Add(timeout))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, data, err := ws.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		var ev xiaozhiapi.ServerEventBase
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		if ev.Type != typ {
			continue
		}
		if id == "" {
			return data, nil
		}
		var n xiaozhiapi.ServerEventNotification
		if err := json.Unmarshal(data, &n); err != nil || n.ID != id {
			continue
		}
		return &n, nil
	}
	return nil, fmt.Errorf("timed out waiting for %s event id=%s", typ, id)
}

func TestNotifyDisabledReturns503(t *testing.T) {
	defaults := *config.Get()
	defaults.Server.Notify = config.ServerNotifyConf{Enabled: false}
	restore := config.Replace(defaults)
	t.Cleanup(restore)
	s := NewWebSocketServer()
	if code, body := doNotify(t, s, "", map[string]any{"device_id": "dev-1", "id": "n-1", "content": "c"}); code != http.StatusServiceUnavailable {
		t.Fatalf("disabled notify: %d %s want 503", code, body)
	}
	if code, _ := getNotifyStatus(t, s, "", "dev-1", "n-1"); code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status: %d want 503", code)
	}
}
