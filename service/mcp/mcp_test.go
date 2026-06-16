package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestManagerInitializesToolsAndCallsServer(t *testing.T) {
	if os.Getenv("GO_XIAOZHI_MCP_HELPER") == "1" {
		runMCPHelper()
		return
	}
	manager := NewManager(config.MCPConf{
		Enabled: true,
		Servers: map[string]config.MCPServerConf{
			"test": {
				Command: os.Args[0],
				Args:    []string{"-test.run=TestManagerInitializesToolsAndCallsServer"},
				Env:     map[string]string{"GO_XIAOZHI_MCP_HELPER": "1"},
			},
		},
	})
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize mcp: %v", err)
	}
	defer manager.Close()

	tools := manager.VoiceTools()
	if len(tools) != 1 || tools[0].Name != "mcp_echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if got := tools[0].Parameters.Properties["text"].Type; got != "string" {
		t.Fatalf("tool text type = %q", got)
	}
	result, err := manager.Call(context.Background(), "mcp_echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("call mcp: %v", err)
	}
	if result.Content != "echo: hello" {
		t.Fatalf("unexpected mcp result: %q", result.Content)
	}
	result, err = manager.Call(context.Background(), "mcp_echo", map[string]any{"text": "fail"})
	if err != nil {
		t.Fatalf("mcp call error should be returned as tool content: %v", err)
	}
	if result.Content != "Error calling tool echo: boom" {
		t.Fatalf("unexpected mcp error content: %q", result.Content)
	}
}

func runMCPHelper() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &req)
		switch req.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"protocolVersion": "2024-11-05"},
			})
		case "tools/list":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "echo",
						"description": "Echo text",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"text": map[string]any{"type": "string", "description": "Text to echo"},
							},
							"required": []string{"text"},
						},
					}},
				},
			})
		case "tools/call":
			args, _ := req.Params["arguments"].(map[string]any)
			text, _ := args["text"].(string)
			if text == "fail" {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error":   map[string]any{"message": "boom"},
				})
				continue
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("echo: %s", text)}},
				},
			})
		}
	}
	os.Exit(0)
}
