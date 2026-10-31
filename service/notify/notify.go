// Package notify 实现 go-xiaozhi 的"推送通道"职责（见 docs 方案 §8.2）：
//
//   - 提醒的 SoR 与调度在 Grok Bot，本包**不**存提醒日历、不做 cron；
//   - 本包只提供受控的短期待投递/回执状态：设备在线时经现有写队列下发
//     notification，离线时记入短期 pending，重连补发，已 ack 的不再重播；
//   - 投递状态持久化为小型 JSON（data/notify_state.json），带 TTL 自动清理。
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
)

// ErrInvalidNotification 表示投递载荷不合法（缺 id / device_id）。
// HTTP 层据此返回 400，而非 500。
var ErrInvalidNotification = errors.New("invalid notification")

// Device 是"可投递通知的在线会话"抽象，由 local.Handler 实现。
// 定义在本包（而非 local）以避免 import 环：notify 只依赖本接口，
// local 依赖 notify，handler 同时依赖两者。
type Device interface {
	// PushNotification 经现有写队列下发一条 notification 事件；
	// 当 n.Speak 为真时，由实现方按约定的打断/等待策略播报内容。
	PushNotification(ctx context.Context, n *Notification) error
	// SetAckSink 注入设备回执（notification_ack）的回调。
	SetAckSink(fn func(ctx context.Context, ack *xiaozhiapi.ClientEventNotificationAck))
}

// Notification 是 /v1/notify 的投递载荷（字段与协议 §7.2 对齐）。
type Notification struct {
	DeviceID       string `json:"device_id"`
	ID             string `json:"id"`
	Category       string `json:"category"`
	Title          string `json:"title,omitempty"`
	Content        string `json:"content"`
	TriggerAt      int64  `json:"trigger_at,omitempty"`
	Priority       string `json:"priority,omitempty"`
	Speak          bool   `json:"speak"`
	DisplaySeconds int    `json:"display_seconds,omitempty"`
}

// 投递状态（Status.State）。
const (
	StatePending   = "pending"   // 已入库，尚未投递（设备离线）
	StateDelivered = "delivered" // 已下发，等待设备回执
	StateAcked     = "acked"     // 设备已回执
	StateExpired   = "expired"   // 超过 pending TTL
	StateNotFound  = "not_found" // 无记录
)

// AckRecord 是某条通知的回执记录（按通知 id 幂等去重）。
type AckRecord struct {
	DeviceID  string `json:"device_id"`
	Action    string `json:"action"`
	Timestamp int64  `json:"timestamp,omitempty"`
	At        int64  `json:"at"` // 服务端记录时刻（epoch 秒）
}

// PendingEntry 是待投递/待回执的一条通知。
type PendingEntry struct {
	Notification `json:",inline"`
	QueuedAt     int64 `json:"queued_at"`
	ExpiresAt    int64 `json:"expires_at"`
	Delivered    bool  `json:"delivered"` // 是否已至少下发过一次
	Attempts     int   `json:"attempts"`
}

type stateJSON struct {
	Pending map[string][]*PendingEntry `json:"pending"`
	Acked   map[string]*AckRecord      `json:"acked"`
}

// Registry 维护 deviceID → 在线会话（last-writer-wins）。
type Registry struct {
	mu      sync.RWMutex
	devices map[string]Device
}

func NewRegistry() *Registry {
	return &Registry{devices: make(map[string]Device)}
}

func (r *Registry) Register(deviceID string, d Device) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" || d == nil {
		return
	}
	r.mu.Lock()
	r.devices[deviceID] = d
	r.mu.Unlock()
}

// Unregister 移除会话；仅当当前注册的就是 d 时才移除，
// 避免旧连接断开的延迟 Unbind 误删新连接。
func (r *Registry) Unregister(deviceID string, d Device) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return
	}
	r.mu.Lock()
	if r.devices[deviceID] == d {
		delete(r.devices, deviceID)
	}
	r.mu.Unlock()
}

func (r *Registry) Lookup(deviceID string) (Device, bool) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, false
	}
	r.mu.RLock()
	d, ok := r.devices[deviceID]
	r.mu.RUnlock()
	return d, ok
}

// Service 负责投递、回执、补发与状态持久化。
type Service struct {
	registry   *Registry
	mu         sync.Mutex // 保护 state 与持久化
	state      stateJSON
	statePath  string
	pendingTTL time.Duration
	ackedTTL   time.Duration
	now        func() time.Time
}

// NewService 构造 Service 并加载既有状态文件。
// statePath 为空时状态只保存在内存；TTL <= 0 时使用默认值（pending 24h / acked 7d）。
func NewService(registry *Registry, statePath string, pendingTTL, ackedTTL time.Duration) *Service {
	if registry == nil {
		registry = NewRegistry()
	}
	if pendingTTL <= 0 {
		pendingTTL = 24 * time.Hour
	}
	if ackedTTL <= 0 {
		ackedTTL = 7 * 24 * time.Hour
	}
	s := &Service{
		registry:   registry,
		state:      stateJSON{Pending: map[string][]*PendingEntry{}, Acked: map[string]*AckRecord{}},
		statePath:  statePath,
		pendingTTL: pendingTTL,
		ackedTTL:   ackedTTL,
		now:        time.Now,
	}
	_ = s.load()
	return s
}

// Bind 在设备连接建立时调用：登记会话、注入回执回调，并补发离线期间积累的待投递项。
func (s *Service) Bind(deviceID string, d Device) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" || d == nil {
		return
	}
	s.registry.Register(deviceID, d)
	d.SetAckSink(s.ackSinkFor(deviceID))
	s.FlushPending(deviceID)
}

// Unbind 在设备断开时调用。
func (s *Service) Unbind(deviceID string, d Device) {
	s.registry.Unregister(deviceID, d)
}

// Lookup 返回设备当前的在线会话（若有）。
func (s *Service) Lookup(deviceID string) (Device, bool) {
	return s.registry.Lookup(deviceID)
}

func (s *Service) ackSinkFor(deviceID string) func(ctx context.Context, ack *xiaozhiapi.ClientEventNotificationAck) {
	return func(ctx context.Context, ack *xiaozhiapi.ClientEventNotificationAck) {
		if ack == nil {
			return
		}
		s.MarkAck(deviceID, ack.ID, string(ack.Action), ack.Timestamp)
	}
}

// Deliver 投递一条通知。设备在线则立即下发（并记为已投递待回执），
// 离线则入库等待补发。已 ack 的 id 直接跳过（幂等去重）。
func (s *Service) Deliver(ctx context.Context, n *Notification) (string, error) {
	if n == nil {
		return StateNotFound, fmt.Errorf("%w: nil notification", ErrInvalidNotification)
	}
	if strings.TrimSpace(n.ID) == "" {
		return StateNotFound, fmt.Errorf("%w: notification id is required", ErrInvalidNotification)
	}
	if strings.TrimSpace(n.DeviceID) == "" {
		return StateNotFound, fmt.Errorf("%w: device_id is required", ErrInvalidNotification)
	}
	n.DeviceID = strings.TrimSpace(n.DeviceID)
	n.ID = strings.TrimSpace(n.ID)

	// 已回执的 id 幂等去重：不再向设备推送（"已 ack 的不再播"）。
	s.mu.Lock()
	s.pruneLocked()
	_, acked := s.state.Acked[n.ID]
	s.mu.Unlock()
	if acked {
		return StateAcked, nil
	}

	if device, online := s.registry.Lookup(n.DeviceID); online {
		if err := device.PushNotification(ctx, n); err != nil {
			// 下发失败（如写队列已满/连接已断）：仍入库 pending，重连补发。
			return s.enqueueLocked(n), err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if _, ok := s.state.Acked[n.ID]; ok {
		return StateAcked, nil // 并发窗口内刚被回执
	}

	now := s.now()
	entry := s.findPendingLocked(n.DeviceID, n.ID)
	if entry == nil {
		entry = &PendingEntry{
			Notification: *n,
			QueuedAt:     now.Unix(),
			ExpiresAt:    now.Add(s.pendingTTL).Unix(),
		}
		s.state.Pending[n.DeviceID] = append(s.state.Pending[n.DeviceID], entry)
	} else {
		// 同 id 重投（Grok Bot 重试）：last-writer-wins，不重复入库。
		entry.Notification = *n
	}
	if _, online := s.registry.Lookup(n.DeviceID); online {
		entry.Delivered = true
		entry.Attempts++
	}
	_ = s.persistLocked()
	if _, online := s.registry.Lookup(n.DeviceID); online {
		return StateDelivered, nil
	}
	return StatePending, nil
}

// MarkAck 记录设备回执：从 pending 移除该 id，写入 acked。
func (s *Service) MarkAck(deviceID, id, action string, timestamp int64) {
	deviceID = strings.TrimSpace(deviceID)
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	removed := false
	if entries, ok := s.state.Pending[deviceID]; ok {
		kept := entries[:0]
		for _, e := range entries {
			if e.ID == id {
				removed = true
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(s.state.Pending, deviceID)
		} else {
			s.state.Pending[deviceID] = kept
		}
	}

	// 只接受本设备相关通知的回执：要么它仍在待投递里，要么已是本设备的回执记录。
	// 否则忽略（防止设备 A 用设备 B 的 id 把 B 的提醒标记为已确认）。
	existing, hasExisting := s.state.Acked[id]
	if !removed && (!hasExisting || existing.DeviceID != deviceID) {
		return
	}
	s.state.Acked[id] = &AckRecord{
		DeviceID:  deviceID,
		Action:    action,
		Timestamp: timestamp,
		At:        s.now().Unix(),
	}
	_ = s.persistLocked()
}

// FlushPending 重连补发：向当前在线会话重发该设备所有未过期、未回执的待投递项。
// 已在 pending 中的项保持记录，等待回执或 TTL 到期（设备按 id 幂等去重）。
func (s *Service) FlushPending(deviceID string) int {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return 0
	}
	device, online := s.registry.Lookup(deviceID)
	if !online {
		return 0
	}

	ctx := context.Background()
	s.mu.Lock()
	s.pruneLocked()
	entries := make([]*PendingEntry, 0, len(s.state.Pending[deviceID]))
	entries = append(entries, s.state.Pending[deviceID]...)
	s.mu.Unlock()

	sent := 0
	for _, e := range entries {
		// 重连补发：无论是否下发过，只要未回执且未过期就再发一次；
		// 设备按 id 幂等去重，不会重复显示/播报。
		if err := device.PushNotification(ctx, &e.Notification); err == nil {
			s.noteAttempt(deviceID, e.ID)
			sent++
		}
	}
	return sent
}

func (s *Service) noteAttempt(deviceID, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.state.Pending[deviceID] {
		if e.ID == id {
			e.Delivered = true
			e.Attempts++
		}
	}
	_ = s.persistLocked()
}

// Status 查询某条通知的投递状态（供 GET /v1/devices/{id}/notify/{notify_id}）。
func (s *Service) Status(deviceID, id string) *Status {
	deviceID = strings.TrimSpace(deviceID)
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	if rec, ok := s.state.Acked[id]; ok {
		return &Status{ID: id, State: StateAcked, Action: rec.Action, At: rec.At}
	}
	for _, e := range s.state.Pending[deviceID] {
		if e.ID == id {
			state := StatePending
			if e.Delivered {
				state = StateDelivered
			}
			return &Status{ID: id, State: state, Attempts: e.Attempts}
		}
	}
	return &Status{ID: id, State: StateNotFound}
}

// Status 是通知投递状态的查询结果。
type Status struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Action   string `json:"action,omitempty"`
	At       int64  `json:"at,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
}

// enqueueLocked 把通知记入 pending 并持久化。调用方需持有 s.mu。
func (s *Service) enqueueLocked(n *Notification) string {
	s.pruneLocked()
	now := s.now()
	entry := s.findPendingLocked(n.DeviceID, n.ID)
	if entry == nil {
		entry = &PendingEntry{
			Notification: *n,
			QueuedAt:     now.Unix(),
			ExpiresAt:    now.Add(s.pendingTTL).Unix(),
		}
		s.state.Pending[n.DeviceID] = append(s.state.Pending[n.DeviceID], entry)
	}
	_ = s.persistLocked()
	return StatePending
}

func (s *Service) findPendingLocked(deviceID, id string) *PendingEntry {
	for _, e := range s.state.Pending[deviceID] {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// pruneLocked 清理过期的 pending 与 acked 记录。调用方需持有 s.mu。
func (s *Service) pruneLocked() {
	now := s.now().Unix()
	for deviceID, entries := range s.state.Pending {
		kept := entries[:0]
		for _, e := range entries {
			if e.ExpiresAt > now {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(s.state.Pending, deviceID)
		} else {
			s.state.Pending[deviceID] = kept
		}
	}
	ackedTTL := s.ackedTTL
	for id, rec := range s.state.Acked {
		if time.Duration(now-rec.At)*time.Second > ackedTTL {
			delete(s.state.Acked, id)
		}
	}
}

func (s *Service) load() error {
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var st stateJSON
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	if st.Pending == nil {
		st.Pending = map[string][]*PendingEntry{}
	}
	if st.Acked == nil {
		st.Acked = map[string]*AckRecord{}
	}
	s.state = st
	return nil
}

// persistLocked 原子写状态文件。调用方需持有 s.mu。
func (s *Service) persistLocked() error {
	if strings.TrimSpace(s.statePath) == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := s.writeStateFile(data); err != nil {
		// 持久化失败不影响内存态投递（仍会补发），但必须可见：
		// 常见原因是 state_path 所在目录不可写（例如 data/ 属主不对）。
		log.Printf("notify state persist failed path=%s error=%v", s.statePath, err)
		return err
	}
	return nil
}

func (s *Service) writeStateFile(data []byte) error {
	dir := filepath.Dir(s.statePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.statePath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 重命名成功后此删除是 no-op
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.statePath)
}

// DeviceIDs 返回当前有状态记录的 deviceID（测试/调试用，按字典序）。
func (s *Service) DeviceIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(s.state.Pending))
	for id := range s.state.Pending {
		seen[id] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
