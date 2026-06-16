package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/voice"
	"gopkg.in/yaml.v3"
)

type Store interface {
	Init(ctx context.Context, roleID string) error
	Query(ctx context.Context, query string) (string, error)
	Save(ctx context.Context, messages []voice.Message) (string, error)
}

func New(conf config.MemoryConf) Store {
	switch strings.TrimSpace(conf.Type) {
	case "local_short", "mem_local_short":
		return &LocalShortStore{
			Path:   conf.Path,
			MaxLen: conf.MaxLen,
		}
	default:
		return NoopStore{}
	}
}

type NoopStore struct{}

func (NoopStore) Init(ctx context.Context, roleID string) error {
	return nil
}

func (NoopStore) Query(ctx context.Context, query string) (string, error) {
	return "", nil
}

func (NoopStore) Save(ctx context.Context, messages []voice.Message) (string, error) {
	return "", nil
}

type LocalShortStore struct {
	Path   string
	MaxLen int

	mu     sync.Mutex
	roleID string
	memory string
}

func (s *LocalShortStore) Init(ctx context.Context, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.roleID = strings.TrimSpace(roleID)
	if s.roleID == "" {
		s.roleID = "anonymous"
	}
	all, err := s.loadAll()
	if err != nil {
		return err
	}
	s.memory = all[s.roleID]
	return nil
}

func (s *LocalShortStore) Query(ctx context.Context, query string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memory, nil
}

func (s *LocalShortStore) Save(ctx context.Context, messages []voice.Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(messages) < 2 {
		return s.memory, nil
	}
	summary := summarize(messages, s.memory, time.Now())
	summary = limitString(summary, s.maxLen())
	if summary == "" {
		return s.memory, nil
	}
	s.memory = summary

	all, err := s.loadAll()
	if err != nil {
		return "", err
	}
	all[s.roleIDOrDefault()] = s.memory
	if err := os.MkdirAll(filepath.Dir(s.path()), 0o755); err != nil {
		return "", err
	}
	data, err := yaml.Marshal(all)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(s.path(), data, 0o644); err != nil {
		return "", err
	}
	return s.memory, nil
}

func (s *LocalShortStore) loadAll() (map[string]string, error) {
	data, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	all := map[string]string{}
	if len(data) == 0 {
		return all, nil
	}
	if err := yaml.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	return all, nil
}

func (s *LocalShortStore) path() string {
	path := strings.TrimSpace(s.Path)
	if path == "" {
		return filepath.Join("data", ".memory.yaml")
	}
	return path
}

func (s *LocalShortStore) maxLen() int {
	if s.MaxLen <= 0 {
		return 4000
	}
	return s.MaxLen
}

func (s *LocalShortStore) roleIDOrDefault() string {
	if strings.TrimSpace(s.roleID) == "" {
		return "anonymous"
	}
	return s.roleID
}

func summarize(messages []voice.Message, previous string, now time.Time) string {
	var lines []string
	if strings.TrimSpace(previous) != "" {
		lines = append(lines, strings.TrimSpace(previous))
	}
	var current []string
	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		switch msg.Role {
		case "user":
			if content != "" {
				current = append(current, "User: "+content)
			}
		case "assistant":
			if content != "" {
				current = append(current, "Assistant: "+content)
			}
		}
	}
	if len(current) == 0 {
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "更新时间: "+now.Format("2006-01-02 15:04:05"))
	lines = append(lines, current...)
	return strings.Join(lines, "\n")
}

func limitString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[len(runes)-maxLen:])
}
