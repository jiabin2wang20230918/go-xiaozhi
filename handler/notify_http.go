package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/notify"
)

// notifyHTTP 挂载 /v1/notify 相关端点（docs 方案 §8.2）。
// 鉴权复用 server.auth（Bearer token / allowed_devices）。
type notifyHTTP struct {
	auth    authenticator
	service *notify.Service
	enabled bool
}

// authenticator 收敛鉴权依赖，便于测试注入。
type authenticator interface {
	Authenticate(r *http.Request) error
}

// notifyStatePath 把相对状态文件路径解析到运行时目录（与配置同基准）。
func notifyStatePath(conf config.ServerNotifyConf, configDir string) string {
	path := strings.TrimSpace(conf.StatePath)
	if path == "" {
		path = filepath.Join("data", "notify_state.json")
	}
	if filepath.IsAbs(path) {
		return path
	}
	if configDir != "" {
		return filepath.Join(configDir, path)
	}
	return path
}

func (s *WebSocketServer) notifyService() *notify.Service {
	return s.notify.service
}

// newNotifyHTTP 构造 notify 端点；conf.Enabled=false 时端点返回 503。
func newNotifyHTTP(auth authenticator, conf config.ServerNotifyConf, configDir string) *notifyHTTP {
	pendingTTL, err := time.ParseDuration(conf.PendingTTL)
	if err != nil || pendingTTL <= 0 {
		pendingTTL = 24 * time.Hour
	}
	ackedTTL, err := time.ParseDuration(conf.AckedTTL)
	if err != nil || ackedTTL <= 0 {
		ackedTTL = 7 * 24 * time.Hour
	}
	nh := &notifyHTTP{
		auth:    auth,
		enabled: conf.Enabled,
	}
	if nh.enabled {
		nh.service = notify.NewService(notify.NewRegistry(), notifyStatePath(conf, configDir), pendingTTL, ackedTTL)
	}
	return nh
}

// BindSession 在 WebSocket 会话建立时绑定设备（登记在线 + 注入回执回调 + 补发离线待投递项）。
func (s *WebSocketServer) BindSession(deviceID string, d notify.Device) {
	if s.notify == nil || s.notify.service == nil || deviceID == "" {
		return
	}
	s.notify.service.Bind(deviceID, d)
}

// UnbindSession 在 WebSocket 会话结束时解绑。
func (s *WebSocketServer) UnbindSession(deviceID string, d notify.Device) {
	if s.notify == nil || s.notify.service == nil || deviceID == "" {
		return
	}
	s.notify.service.Unbind(deviceID, d)
}

// notify 端点：POST /v1/notify
//
//	Authorization: Bearer <token>
//
//	{"device_id":"...","id":"...","content":"...","speak":true,...}
//
// 返回：200 {"state":"delivered"}（在线）/ {"state":"pending"}（离线）/
// {"state":"acked"}（已回执，幂等去重）；400 参数错误；401 鉴权失败；503 未启用。
func (n *notifyHTTP) handleNotify(w http.ResponseWriter, r *http.Request) {
	if !n.enabled || n.service == nil {
		writeNotifyJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "notify disabled"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeNotifyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if err := n.auth.Authenticate(r); err != nil {
		writeNotifyJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	var ntfy notify.Notification
	if err := json.NewDecoder(r.Body).Decode(&ntfy); err != nil {
		writeNotifyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json body: " + err.Error()})
		return
	}
	state, err := n.service.Deliver(context.Background(), &ntfy)
	if err != nil {
		log.Printf("notify deliver device_id=%s id=%s error=%v", ntfy.DeviceID, ntfy.ID, err)
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, notify.ErrInvalidNotification):
			code = http.StatusBadRequest
		// 下发失败但已记入 pending（重连补发）：对调用方仍视为已受理。
		case state == notify.StatePending:
			code = http.StatusOK
		}
		writeNotifyJSON(w, code, map[string]any{"state": state, "error": err.Error()})
		return
	}
	log.Printf("notify delivered device_id=%s id=%s state=%s", ntfy.DeviceID, ntfy.ID, state)
	writeNotifyJSON(w, http.StatusOK, map[string]any{"state": state})
}

// status 端点：GET /v1/devices/{device_id}/notify/{notify_id}
//
// 供 Grok Bot 轮询设备回执（snooze v1 回路，见 docs 方案 §7.4/§11）。
// 路径参数从尾部按固定两段解析，device_id 中不允许含 '/'。
func (n *notifyHTTP) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !n.enabled || n.service == nil {
		writeNotifyJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "notify disabled"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeNotifyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if err := n.auth.Authenticate(r); err != nil {
		writeNotifyJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	// 路径形如 /v1/devices/{device_id}/notify/{notify_id}；device_id 不允许含 '/'。
	path := strings.TrimPrefix(r.URL.Path, "/v1/devices/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "notify" || parts[2] == "" {
		writeNotifyJSON(w, http.StatusNotFound, map[string]any{"error": "expected /v1/devices/{device_id}/notify/{notify_id}"})
		return
	}
	deviceID, notifyID := parts[0], parts[2]
	status := n.service.Status(deviceID, notifyID)
	code := http.StatusOK
	if status.State == notify.StateNotFound {
		code = http.StatusNotFound
	}
	writeNotifyJSON(w, code, status)
}

func writeNotifyJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
