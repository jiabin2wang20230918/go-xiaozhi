package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/voice"
)

func TestNoopStore(t *testing.T) {
	store := New(config.MemoryConf{Type: "none"})
	if err := store.Init(context.Background(), "device-1"); err != nil {
		t.Fatalf("init noop: %v", err)
	}
	if got, err := store.Query(context.Background(), "hello"); err != nil || got != "" {
		t.Fatalf("query noop got %q err %v", got, err)
	}
	if got, err := store.Save(context.Background(), []voice.Message{{Role: "user", Content: "hello"}}); err != nil || got != "" {
		t.Fatalf("save noop got %q err %v", got, err)
	}
}

func TestLocalShortStoreSavesAndLoadsByRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".memory.yaml")
	store := New(config.MemoryConf{Type: "local_short", Path: path, MaxLen: 4000})
	if err := store.Init(context.Background(), "device-1"); err != nil {
		t.Fatalf("init local memory: %v", err)
	}

	saved, err := store.Save(context.Background(), []voice.Message{
		{Role: "system", Content: "ignored"},
		{Role: "user", Content: "我喜欢咖啡"},
		{Role: "assistant", Content: "记住了。"},
	})
	if err != nil {
		t.Fatalf("save local memory: %v", err)
	}
	if !strings.Contains(saved, "User: 我喜欢咖啡") || !strings.Contains(saved, "Assistant: 记住了。") {
		t.Fatalf("unexpected saved memory: %q", saved)
	}

	reloaded := New(config.MemoryConf{Type: "local_short", Path: path})
	if err := reloaded.Init(context.Background(), "device-1"); err != nil {
		t.Fatalf("reload local memory: %v", err)
	}
	got, err := reloaded.Query(context.Background(), "咖啡")
	if err != nil {
		t.Fatalf("query local memory: %v", err)
	}
	if got != saved {
		t.Fatalf("loaded memory got %q want %q", got, saved)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	if !strings.Contains(string(data), "device-1") {
		t.Fatalf("memory file does not contain role id: %s", data)
	}
}

func TestLocalShortStoreAcceptsPythonLegacyType(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".memory.yaml")
	store := New(config.MemoryConf{Type: "mem_local_short", Path: path})
	if err := store.Init(context.Background(), "device-1"); err != nil {
		t.Fatalf("init legacy local memory: %v", err)
	}
	saved, err := store.Save(context.Background(), []voice.Message{
		{Role: "user", Content: "记住我"},
		{Role: "assistant", Content: "好的"},
	})
	if err != nil {
		t.Fatalf("save legacy local memory: %v", err)
	}
	if !strings.Contains(saved, "User: 记住我") {
		t.Fatalf("legacy memory did not save dialogue: %q", saved)
	}
}

func TestLocalShortStoreSkipsShortDialogueLikePython(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".memory.yaml")
	store := New(config.MemoryConf{Type: "local_short", Path: path})
	if err := store.Init(context.Background(), "device-1"); err != nil {
		t.Fatalf("init local memory: %v", err)
	}
	saved, err := store.Save(context.Background(), []voice.Message{{Role: "user", Content: "只有一句"}})
	if err != nil {
		t.Fatalf("save short dialogue: %v", err)
	}
	if saved != "" {
		t.Fatalf("short dialogue should keep empty memory, got %q", saved)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("short dialogue should not create memory file, stat err=%v", err)
	}

	if _, err := store.Save(context.Background(), []voice.Message{
		{Role: "user", Content: "我喜欢咖啡"},
		{Role: "assistant", Content: "记住了"},
	}); err != nil {
		t.Fatalf("save full dialogue: %v", err)
	}
	before, err := store.Query(context.Background(), "")
	if err != nil {
		t.Fatalf("query memory: %v", err)
	}
	after, err := store.Save(context.Background(), []voice.Message{{Role: "user", Content: "单条不会更新"}})
	if err != nil {
		t.Fatalf("save second short dialogue: %v", err)
	}
	if after != before {
		t.Fatalf("short dialogue should keep previous memory, got %q want %q", after, before)
	}
}

func TestLocalShortStoreLimitsLength(t *testing.T) {
	store := New(config.MemoryConf{
		Type:   "local_short",
		Path:   filepath.Join(t.TempDir(), ".memory.yaml"),
		MaxLen: 20,
	})
	if err := store.Init(context.Background(), "device-1"); err != nil {
		t.Fatalf("init local memory: %v", err)
	}
	saved, err := store.Save(context.Background(), []voice.Message{
		{Role: "user", Content: strings.Repeat("长", 80)},
	})
	if err != nil {
		t.Fatalf("save local memory: %v", err)
	}
	if len([]rune(saved)) > 20 {
		t.Fatalf("saved memory length got %d want <= 20: %q", len([]rune(saved)), saved)
	}
}
