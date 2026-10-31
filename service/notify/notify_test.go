package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
)

type fakeDevice struct {
	mu     sync.Mutex
	pushed []*Notification
	sink   func(ctx context.Context, ack *xiaozhiapi.ClientEventNotificationAck)
	fail   bool
}

func (f *fakeDevice) PushNotification(ctx context.Context, n *Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return os.ErrClosed
	}
	cp := *n
	f.pushed = append(f.pushed, &cp)
	return nil
}

func (f *fakeDevice) SetAckSink(fn func(ctx context.Context, ack *xiaozhiapi.ClientEventNotificationAck)) {
	f.sink = fn
}

func (f *fakeDevice) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pushed)
}

func (f *fakeDevice) ack(id, action string) {
	if f.sink != nil {
		f.sink(context.Background(), &xiaozhiapi.ClientEventNotificationAck{ID: id, Action: xiaozhiapi.NotificationAckAction(action)})
	}
}

func newTestService(t *testing.T) (*Service, *fakeDevice, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "notify_state.json")
	svc := NewService(NewRegistry(), path, 24*time.Hour, 7*24*time.Hour)
	dev := &fakeDevice{}
	return svc, dev, path
}

func testNotification(id string) *Notification {
	return &Notification{
		DeviceID: "dev-1",
		ID:       id,
		Category: "reminder",
		Title:    "项目会议",
		Content:  "15分钟后开始项目会议。",
		Speak:    true,
	}
}

func TestDeliverOfflineGoesPending(t *testing.T) {
	svc, _, path := newTestService(t)

	state, err := svc.Deliver(context.Background(), testNotification("n-1"))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if state != StatePending {
		t.Fatalf("state got %q want %q", state, StatePending)
	}
	st := svc.Status("dev-1", "n-1")
	if st.State != StatePending {
		t.Fatalf("status got %q want pending", st.State)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not persisted: %v", err)
	}
}

func TestDeliverOnlineIsDelivered(t *testing.T) {
	svc, dev, _ := newTestService(t)
	svc.Bind("dev-1", dev)
	t.Cleanup(func() { svc.Unbind("dev-1", dev) })

	state, err := svc.Deliver(context.Background(), testNotification("n-1"))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if state != StateDelivered {
		t.Fatalf("state got %q want delivered", state)
	}
	if got := dev.count(); got != 1 {
		t.Fatalf("pushed count got %d want 1", got)
	}
}

func TestAckedIDIsNotResent(t *testing.T) {
	svc, dev, _ := newTestService(t)
	svc.Bind("dev-1", dev)
	t.Cleanup(func() { svc.Unbind("dev-1", dev) })

	if _, err := svc.Deliver(context.Background(), testNotification("n-1")); err != nil {
		t.Fatalf("first deliver: %v", err)
	}
	dev.ack("n-1", "ack")

	state, err := svc.Deliver(context.Background(), testNotification("n-1"))
	if err != nil {
		t.Fatalf("second deliver: %v", err)
	}
	if state != StateAcked {
		t.Fatalf("second deliver state got %q want acked", state)
	}
	if got := dev.count(); got != 1 {
		t.Fatalf("acked id must not be re-pushed: count got %d want 1", got)
	}
	st := svc.Status("dev-1", "n-1")
	if st.State != StateAcked || st.Action != "ack" {
		t.Fatalf("status got %+v want acked/ack", st)
	}
}

func TestBindFlushesPendingOnReconnect(t *testing.T) {
	svc, dev, _ := newTestService(t)

	// 离线期间入库两条。
	if _, err := svc.Deliver(context.Background(), testNotification("n-1")); err != nil {
		t.Fatalf("deliver n-1: %v", err)
	}
	if _, err := svc.Deliver(context.Background(), testNotification("n-2")); err != nil {
		t.Fatalf("deliver n-2: %v", err)
	}

	// 重连：Bind 应补发全部待投递项。
	svc.Bind("dev-1", dev)
	t.Cleanup(func() { svc.Unbind("dev-1", dev) })
	if got := dev.count(); got != 2 {
		t.Fatalf("flush pushed count got %d want 2", got)
	}
	if st := svc.Status("dev-1", "n-1"); st.State != StateDelivered {
		t.Fatalf("status after flush got %q want delivered", st.State)
	}

	// 回执后再次重连（模拟重启）不应再发该条。
	dev.ack("n-1", "ack")
	dev2 := &fakeDevice{}
	svc.Bind("dev-1", dev2)
	t.Cleanup(func() { svc.Unbind("dev-1", dev2) })
	if got := dev2.count(); got != 1 {
		t.Fatalf("re-flush pushed count got %d want 1 (only un-acked)", got)
	}
}

func TestPendingTTLExpiry(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(NewRegistry(), filepath.Join(dir, "s.json"), 24*time.Hour, 7*24*time.Hour)
	svc.now = func() time.Time { return time.Unix(1000, 0) }
	if _, err := svc.Deliver(context.Background(), testNotification("n-1")); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	// 越过 pending TTL。
	svc.now = func() time.Time { return time.Unix(1000+25*3600, 0) }
	if st := svc.Status("dev-1", "n-1"); st.State != StateNotFound {
		t.Fatalf("status after TTL got %q want not_found", st.State)
	}
	// 过期的 pending 不再补发。
	dev := &fakeDevice{}
	svc.Bind("dev-1", dev)
	if got := dev.count(); got != 0 {
		t.Fatalf("expired pending must not flush: got %d", got)
	}
}

func TestAckedTTLExpiry(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(NewRegistry(), filepath.Join(dir, "s.json"), 24*time.Hour, 7*24*time.Hour)
	svc.now = func() time.Time { return time.Unix(1000, 0) }
	dev := &fakeDevice{}
	svc.Bind("dev-1", dev)
	svc.MarkAck("dev-1", "n-1", "ack", 0)
	// 越过 acked TTL 后，同 id 可再次投递。
	svc.now = func() time.Time { return time.Unix(1000+8*24*3600, 0) }
	state, err := svc.Deliver(context.Background(), testNotification("n-1"))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if state != StateDelivered {
		t.Fatalf("state got %q want delivered", state)
	}
}

func TestStatePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notify_state.json")
	svc := NewService(NewRegistry(), path, 24*time.Hour, 7*24*time.Hour)
	if _, err := svc.Deliver(context.Background(), testNotification("n-1")); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	dev := &fakeDevice{}
	svc.Bind("dev-1", dev)
	dev.ack("n-1", "snooze")

	// 模拟重启：同路径重新加载。
	svc2 := NewService(NewRegistry(), path, 24*time.Hour, 7*24*time.Hour)
	st := svc2.Status("dev-1", "n-1")
	if st.State != StateAcked || st.Action != "snooze" {
		t.Fatalf("reloaded status got %+v want acked/snooze", st)
	}
}

func TestUnbindOnlyRemovesSameDevice(t *testing.T) {
	svc, oldDev, _ := newTestService(t)
	svc.Bind("dev-1", oldDev)

	newDev := &fakeDevice{}
	svc.Bind("dev-1", newDev) // last-writer-wins

	// 旧连接延迟断开：不得误删新连接。
	svc.Unbind("dev-1", oldDev)

	state, err := svc.Deliver(context.Background(), testNotification("n-1"))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if state != StateDelivered {
		t.Fatalf("state got %q want delivered (new session still online)", state)
	}
	if got := newDev.count(); got != 1 {
		t.Fatalf("new session pushed count got %d want 1", got)
	}
	if got := oldDev.count(); got != 0 {
		t.Fatalf("old session must not receive: got %d", got)
	}
}

func TestDeliverValidation(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, err := svc.Deliver(context.Background(), &Notification{DeviceID: "dev-1"}); err == nil {
		t.Fatal("missing id must fail")
	}
	if _, err := svc.Deliver(context.Background(), &Notification{ID: "n-1"}); err == nil {
		t.Fatal("missing device_id must fail")
	}
}
func TestDeliverValidationIsErrInvalidNotification(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := svc.Deliver(context.Background(), &Notification{DeviceID: "dev-1"})
	if !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("missing id: got %v want ErrInvalidNotification", err)
	}
	_, err = svc.Deliver(context.Background(), &Notification{ID: "n-1"})
	if !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("missing device_id: got %v want ErrInvalidNotification", err)
	}
}

func TestMarkAckIgnoresForeignDeviceIDs(t *testing.T) {
	svc, devA, _ := newTestService(t)
	devB := &fakeDevice{}
	svc.Bind("dev-a", devA)
	t.Cleanup(func() { svc.Unbind("dev-a", devA) })
	svc.Bind("dev-b", devB)
	t.Cleanup(func() { svc.Unbind("dev-b", devB) })

	// dev-a 的提醒。
	if _, err := svc.Deliver(context.Background(), &Notification{DeviceID: "dev-a", ID: "n-a", Content: "c"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	// dev-b 试图确认 dev-a 的提醒：必须被忽略。
	devB.ack("n-a", "ack")
	if st := svc.Status("dev-a", "n-a"); st.State == StateAcked {
		t.Fatalf("foreign ack must be ignored, got %+v", st)
	}

	// dev-a 自己确认：生效。
	devA.ack("n-a", "ack")
	if st := svc.Status("dev-a", "n-a"); st.State != StateAcked || st.Action != "ack" {
		t.Fatalf("owner ack got %+v want acked/ack", st)
	}

	// 后续重复回执（shown → ack）仍可更新同一设备的记录。
	devA.ack("n-a", "snooze")
	if st := svc.Status("dev-a", "n-a"); st.State != StateAcked || st.Action != "snooze" {
		t.Fatalf("repeat ack got %+v want acked/snooze", st)
	}
}

func TestPersistFailureKeepsInMemoryState(t *testing.T) {
	// 指向不可写路径（父级是普通文件），持久化必然失败。
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	svc := NewService(NewRegistry(), filepath.Join(blocker, "notify_state.json"), time.Hour, time.Hour)

	state, err := svc.Deliver(context.Background(), testNotification("n-1"))
	if err != nil {
		t.Fatalf("deliver must succeed even if persist fails: %v", err)
	}
	if state != StatePending {
		t.Fatalf("state got %q want pending", state)
	}
	// 内存态仍可查询、仍会补发。
	if st := svc.Status("dev-1", "n-1"); st.State != StatePending {
		t.Fatalf("in-memory status got %q want pending", st.State)
	}
	dev := &fakeDevice{}
	svc.Bind("dev-1", dev)
	if dev.count() != 1 {
		t.Fatalf("flush after persist failure got %d want 1", dev.count())
	}
}
