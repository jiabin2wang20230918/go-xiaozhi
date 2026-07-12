package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bingoml/mlat"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/voice"
)

// MLATStore adapts the standalone github.com/bingoml/mlat memory library to
// go-xiaozhi's Store interface. Each roleID (typically the device ID) gets its
// own workspace directory and SQLite database, isolating per-device memory and
// avoiding cross-connection write contention.
type MLATStore struct {
	root        string
	searchLimit int
	maxLen      int
	schedule    bool

	mu      sync.Mutex
	manager *mlat.MemoryManager
	roleID  string
}

// NewMLATStore builds an uninitialized MLATStore. The manager is created on
// Init once the roleID (workspace) is known.
func NewMLATStore(conf config.MemoryConf) *MLATStore {
	root := strings.TrimSpace(conf.Path)
	if root == "" {
		root = filepath.Join("data", "memory")
	}
	searchLimit := conf.SearchLimit
	if searchLimit <= 0 {
		searchLimit = 5
	}
	return &MLATStore{
		root:        root,
		searchLimit: searchLimit,
		maxLen:      conf.MaxLen,
		schedule:    conf.Schedule,
	}
}

func (s *MLATStore) Init(ctx context.Context, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Close any previously-open manager (e.g. on repeated Init).
	if s.manager != nil {
		_ = s.manager.Close()
		s.manager = nil
	}

	s.roleID = roleID
	workspace := filepath.Join(s.root, sanitizeRoleID(roleID))

	// mlat's NewMemoryCore opens the SQLite file but does not create its parent
	// directory, so ensure the per-device workspace exists first.
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}

	opts := []mlat.Option{mlat.WithExtractor(mlat.NewRegexExtractor())}
	if s.schedule {
		opts = append(opts, mlat.WithScheduledTasks())
	}

	manager, err := mlat.NewManager(workspace, opts...)
	if err != nil {
		return err
	}
	s.manager = manager
	return nil
}

func (s *MLATStore) Query(ctx context.Context, query string) (string, error) {
	s.mu.Lock()
	manager := s.manager
	s.mu.Unlock()
	if manager == nil || strings.TrimSpace(query) == "" {
		return "", nil
	}

	results, err := manager.Search(query, s.searchLimit)
	if err != nil {
		return "", err
	}

	var lines []string
	for _, r := range results {
		// Skip low-confidence noise; it would pollute the prompt.
		if r.Confidence < 0.3 {
			continue
		}
		content := strings.TrimSpace(r.Content)
		if content == "" {
			continue
		}
		lines = append(lines, "- "+content)
	}
	return strings.Join(lines, "\n"), nil
}

func (s *MLATStore) Save(ctx context.Context, messages []voice.Message) (string, error) {
	s.mu.Lock()
	manager := s.manager
	roleID := s.roleID
	s.mu.Unlock()
	if manager == nil || len(messages) == 0 {
		return "", nil
	}

	content := messagesToContent(messages)
	if strings.TrimSpace(content) == "" {
		return "", nil
	}

	sourceKey := "session:" + roleID + ":" + time.Now().Format("20060102-150405")
	if err := manager.SedimentKnowledgeWithType(content, sourceKey, mlat.MemoryTypeConversationSummary); err != nil {
		return "", err
	}

	return limitString(content, s.effectiveMaxLen()), nil
}

func (s *MLATStore) Close(ctx context.Context) error {
	s.mu.Lock()
	manager := s.manager
	s.manager = nil
	s.mu.Unlock()
	if manager == nil {
		return nil
	}
	return manager.Close()
}

// effectiveMaxLen returns the configured max length, defaulting to 4000.
func (s *MLATStore) effectiveMaxLen() int {
	if s.maxLen <= 0 {
		return 4000
	}
	return s.maxLen
}

// messagesToContent flattens a conversation into "Role: Content" lines, keeping
// only user and assistant turns (the ones worth remembering).
func messagesToContent(messages []voice.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(content)
		b.WriteByte('\n')
	}
	return b.String()
}

// sanitizeRoleID replaces path separators and other filesystem-hostile
// characters so the roleID can be safely used as a directory name. roleID comes
// from device/client IDs and may contain ':' or '/'.
func sanitizeRoleID(roleID string) string {
	roleID = strings.TrimSpace(roleID)
	if roleID == "" {
		return "anonymous"
	}
	return strings.NewReplacer(
		string(filepath.Separator), "_",
		"/", "_",
		"\\", "_",
		":", "_",
	).Replace(roleID)
}
