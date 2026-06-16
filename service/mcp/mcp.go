package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/voice"
)

type Manager struct {
	conf    config.MCPConf
	clients []*Client
	tools   map[string]*Client
}

type ToolResult struct {
	Content string
}

func NewManager(conf config.MCPConf) *Manager {
	conf.Normalize()
	return &Manager{conf: conf, tools: map[string]*Client{}}
}

func (m *Manager) Initialize(ctx context.Context) error {
	if !m.conf.Enabled {
		return nil
	}
	servers, err := loadServers(m.conf)
	if err != nil {
		return err
	}
	for name, server := range servers {
		if strings.TrimSpace(server.Command) == "" {
			continue
		}
		client := NewClient(name, server)
		if err := client.Start(ctx); err != nil {
			continue
		}
		m.clients = append(m.clients, client)
		for _, tool := range client.Tools() {
			m.tools["mcp_"+tool.Name] = client
		}
	}
	return nil
}

func (m *Manager) VoiceTools() []voice.Tool {
	tools := make([]voice.Tool, 0)
	for _, client := range m.clients {
		for _, tool := range client.Tools() {
			tools = append(tools, voice.Tool{
				Name:        "mcp_" + tool.Name,
				Description: tool.Description,
				Parameters:  schemaToParameters(tool.InputSchema),
			})
		}
	}
	return tools
}

func (m *Manager) Call(ctx context.Context, prefixedName string, args map[string]any) (ToolResult, error) {
	client := m.tools[prefixedName]
	if client == nil {
		return ToolResult{}, fmt.Errorf("Tool %s not found in any MCP server", prefixedName)
	}
	name := strings.TrimPrefix(prefixedName, "mcp_")
	text, err := client.CallTool(ctx, name, args)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error calling tool %s: %v", name, err)}, nil
	}
	return ToolResult{Content: text}, nil
}

func (m *Manager) Close() error {
	var first error
	for _, client := range m.clients {
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func loadServers(conf config.MCPConf) (map[string]config.MCPServerConf, error) {
	if len(conf.Servers) > 0 {
		return conf.Servers, nil
	}
	path := strings.TrimSpace(conf.Path)
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		if repoRoot, ok := repoRootFromCaller(); ok {
			alt := filepath.Join(repoRoot, path)
			if _, altErr := os.Stat(alt); altErr == nil {
				path = alt
			} else {
				return nil, nil
			}
		} else {
			return nil, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Servers map[string]config.MCPServerConf `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	return file.Servers, nil
}

func repoRootFromCaller() (string, bool) {
	wd, err := os.Getwd()
	if err == nil {
		return wd, true
	}
	return "", false
}

type Client struct {
	name   string
	conf   config.MCPServerConf
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	mu     sync.Mutex
	nextID atomic.Int64
	tools  []Tool
}

type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

func NewClient(name string, conf config.MCPServerConf) *Client {
	return &Client{name: name, conf: conf}
}

func (c *Client) Start(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, c.conf.Command, c.conf.Args...)
	cmd.Env = os.Environ()
	for key, value := range c.conf.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	c.cmd = cmd
	c.stdin = stdin
	c.reader = bufio.NewReader(stdout)
	if _, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "go-xiaozhi", "version": "0.1.0"},
	}); err != nil {
		_ = c.Close()
		return err
	}
	resp, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		_ = c.Close()
		return err
	}
	c.tools = parseTools(resp)
	return nil
}

func (c *Client) Tools() []Tool {
	return append([]Tool(nil), c.tools...)
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	resp, err := c.request(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}
	return parseToolContent(resp), nil
}

func (c *Client) request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID.Add(1)
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if _, err := c.stdin.Write(data); err != nil {
		return nil, err
	}
	type response struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("mcp request timeout: %s", method)
		default:
		}
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var resp response
		if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
			continue
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, errors.New(resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *Client) Close() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		return c.cmd.Wait()
	}
	return nil
}

func parseTools(data json.RawMessage) []Tool {
	var response struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(data, &response)
	tools := make([]Tool, 0, len(response.Tools))
	for _, tool := range response.Tools {
		tools = append(tools, Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return tools
}

func parseToolContent(data json.RawMessage) string {
	var response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return string(data)
	}
	parts := make([]string, 0, len(response.Content))
	for _, item := range response.Content {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func schemaToParameters(schema map[string]any) voice.ToolParameters {
	params := voice.ToolParameters{Type: "object", Properties: map[string]voice.ToolProperty{}}
	if typ, _ := schema["type"].(string); typ != "" {
		params.Type = typ
	}
	if required, ok := schema["required"].([]any); ok {
		for _, item := range required {
			if text, ok := item.(string); ok {
				params.Required = append(params.Required, text)
			}
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range props {
			if prop, ok := raw.(map[string]any); ok {
				params.Properties[name] = schemaToProperty(prop)
			}
		}
	}
	return params
}

func schemaToProperty(schema map[string]any) voice.ToolProperty {
	prop := voice.ToolProperty{Type: "string"}
	if typ, _ := schema["type"].(string); typ != "" {
		prop.Type = typ
	}
	if desc, _ := schema["description"].(string); desc != "" {
		prop.Description = desc
	}
	if required, ok := schema["required"].([]any); ok {
		for _, item := range required {
			if text, ok := item.(string); ok {
				prop.Required = append(prop.Required, text)
			}
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		prop.Properties = map[string]voice.ToolProperty{}
		for name, raw := range props {
			if child, ok := raw.(map[string]any); ok {
				prop.Properties[name] = schemaToProperty(child)
			}
		}
	}
	return prop
}
