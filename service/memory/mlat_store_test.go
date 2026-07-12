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

func newTestMLATStore(t *testing.T) *MLATStore {
	t.Helper()
	conf := config.MemoryConf{
		Type:        "mlat",
		Path:        filepath.Join(t.TempDir(), "memory"),
		SearchLimit: 5,
	}
	return NewMLATStore(conf)
}

func TestMLATStore_QueryUsesQueryAndPersists(t *testing.T) {
	ctx := context.Background()
	store := newTestMLATStore(t)

	if err := store.Init(ctx, "device-1"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer store.Close(ctx)

	// Seed memory with a sedimentable conversation. RegexExtractor matches the
	// English preference pattern, so this content should become searchable.
	msgs := []voice.Message{
		{Role: "user", Content: "I prefer concise answers"},
		{Role: "assistant", Content: "Got it, keeping it brief."},
	}
	if _, err := store.Save(ctx, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Query must actually use the query term (improvement over LocalShortStore).
	got, err := store.Query(ctx, "concise")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !strings.Contains(strings.ToLower(got), "concise") {
		t.Fatalf("Query result = %q, want substring 'concise'", got)
	}

	// Empty query returns empty (promptWithMemory won't append an empty block).
	if g, _ := store.Query(ctx, "   "); g != "" {
		t.Fatalf("empty query = %q, want empty", g)
	}
}

func TestMLATStore_PersistsAcrossReinit(t *testing.T) {
	ctx := context.Background()
	store := newTestMLATStore(t)

	if err := store.Init(ctx, "device-1"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	msgs := []voice.Message{
		{Role: "user", Content: "User prefers terminal workflows"},
	}
	if _, err := store.Save(ctx, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open the same device's memory and confirm it survived.
	if err := store.Init(ctx, "device-1"); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	defer store.Close(ctx)

	got, err := store.Query(ctx, "terminal")
	if err != nil {
		t.Fatalf("Query after reinit: %v", err)
	}
	if !strings.Contains(strings.ToLower(got), "terminal") {
		t.Fatalf("Query after reinit = %q, want substring 'terminal'", got)
	}
}

func TestMLATStore_DeviceIsolation(t *testing.T) {
	ctx := context.Background()
	store := newTestMLATStore(t)

	// device-A learns something specific.
	if err := store.Init(ctx, "device-A"); err != nil {
		t.Fatalf("Init A: %v", err)
	}
	if _, err := store.Save(ctx, []voice.Message{
		{Role: "user", Content: "I prefer quantum physics explanations"},
	}); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := store.Close(ctx); err != nil {
		t.Fatalf("Close A: %v", err)
	}

	// device-B must not see device-A's memory.
	if err := store.Init(ctx, "device-B"); err != nil {
		t.Fatalf("Init B: %v", err)
	}
	defer store.Close(ctx)
	got, _ := store.Query(ctx, "quantum")
	if strings.Contains(strings.ToLower(got), "quantum") {
		t.Fatalf("device-B saw device-A memory: %q", got)
	}
}

func TestMLATStore_RoleIDSanitized(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	conf := config.MemoryConf{Type: "mlat", Path: root}
	store := NewMLATStore(conf)

	// A roleID with path separators must not escape the root directory.
	roleID := "../evil:device"
	if err := store.Init(context.Background(), roleID); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer store.Close(context.Background())

	// The sanitized name must contain no path separators (it may still contain
	// a harmless ".." substring once separators are gone, e.g. ".._evil").
	sanitized := sanitizeRoleID(roleID)
	if strings.ContainsAny(sanitized, `/\`+string(filepath.Separator)) {
		t.Fatalf("sanitized roleID = %q, still contains path separators", sanitized)
	}

	// The workspace must live under root, not above it.
	workspace := filepath.Join(root, sanitized)
	if _, err := os.Stat(filepath.Join(workspace, "memory.db")); err != nil {
		t.Fatalf("expected memory.db under sanitized workspace: %v", err)
	}
}
