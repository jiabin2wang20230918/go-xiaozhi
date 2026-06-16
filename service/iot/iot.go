package iot

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
	"github.com/xdimtech/go-xiaozhi/service/voice"
)

type Tool struct {
	Name        string
	Description string
	DeviceName  string
	Kind        ToolKind
	Property    string
	Method      string
	Parameters  map[string]xiaozhiapi.IotPropertySchema
	Required    []string
}

type ToolKind string

const (
	ToolKindQuery   ToolKind = "query"
	ToolKindControl ToolKind = "control"
)

type Result struct {
	OK      bool
	Data    string
	Message string
	Command *xiaozhiapi.ServerEventIotCommand
}

type Registry struct {
	mu          sync.RWMutex
	descriptors map[string]xiaozhiapi.IotDescriptor
	states      map[string]map[string]any
	tools       map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{
		descriptors: make(map[string]xiaozhiapi.IotDescriptor),
		states:      make(map[string]map[string]any),
		tools:       make(map[string]Tool),
	}
}

func (r *Registry) AddDescriptors(descriptors []xiaozhiapi.IotDescriptor) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, descriptor := range descriptors {
		if strings.TrimSpace(descriptor.Name) == "" {
			continue
		}
		r.descriptors[descriptor.Name] = descriptor
		if _, ok := r.states[descriptor.Name]; !ok {
			r.states[descriptor.Name] = make(map[string]any)
		}
		for name, property := range descriptor.Properties {
			if _, exists := r.states[descriptor.Name][name]; !exists {
				r.states[descriptor.Name][name] = defaultPropertyValue(property.Type)
			}
		}
		for name, tool := range toolsForDescriptor(descriptor) {
			r.tools[name] = tool
		}
	}
}

func (r *Registry) UpdateStates(states []xiaozhiapi.IotState) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, state := range states {
		if strings.TrimSpace(state.Name) == "" {
			continue
		}
		if _, ok := r.states[state.Name]; !ok {
			continue
		}
		for k, v := range state.State {
			r.states[state.Name][k] = v
		}
	}
}

func defaultPropertyValue(propertyType string) any {
	switch propertyType {
	case "number":
		return float64(0)
	case "boolean":
		return false
	default:
		return ""
	}
}

func (r *Registry) Tools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	tools := make([]Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, r.tools[name])
	}
	return tools
}

func (r *Registry) VoiceTools() []voice.Tool {
	tools := r.Tools()
	out := make([]voice.Tool, 0, len(tools))
	for _, tool := range tools {
		properties := make(map[string]voice.ToolProperty, len(tool.Parameters))
		for name, param := range tool.Parameters {
			properties[name] = voice.ToolProperty{
				Type:        param.Type,
				Description: param.Description,
			}
		}
		out = append(out, voice.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters: voice.ToolParameters{
				Type:       "object",
				Properties: properties,
				Required:   append([]string(nil), tool.Required...),
			},
		})
	}
	return out
}

func (r *Registry) Invoke(name string, args map[string]any) (Result, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	if !ok {
		r.mu.RUnlock()
		return Result{}, fmt.Errorf("iot tool %q not found", name)
	}

	switch tool.Kind {
	case ToolKindQuery:
		value, ok := r.states[tool.DeviceName][tool.Property]
		r.mu.RUnlock()
		if !ok {
			return Result{OK: false, Message: responseFailure(args, "操作失败")}, nil
		}
		return Result{
			OK:      true,
			Data:    fmt.Sprint(value),
			Message: replaceValue(responseSuccess(args, fmt.Sprintf("%s为{value}", tool.Property)), value),
		}, nil
	case ToolKindControl:
		if _, ok := r.descriptors[tool.DeviceName]; !ok {
			r.mu.RUnlock()
			return Result{}, fmt.Errorf("iot device %q not found", tool.DeviceName)
		}
		params := commandParameters(args)
		command := xiaozhiapi.ServerEventIotCommand{
			Name:       tool.DeviceName,
			Method:     tool.Method,
			Parameters: params,
		}
		r.mu.RUnlock()
		return Result{
			OK:      true,
			Data:    fmt.Sprintf("%s的%s操作执行成功", tool.DeviceName, tool.Method),
			Message: formatControlResponse(responseSuccess(args, "操作成功"), params),
			Command: &command,
		}, nil
	default:
		r.mu.RUnlock()
		return Result{}, fmt.Errorf("unsupported iot tool kind %q", tool.Kind)
	}
}

func (r *Registry) GetState(deviceName string, property string) (any, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.states[deviceName]
	if !ok {
		return nil, false
	}
	value, ok := state[property]
	return value, ok
}

func (r *Registry) SendCommand(deviceName string, method string, params map[string]any) (Result, error) {
	r.mu.RLock()
	if _, ok := r.descriptors[deviceName]; !ok {
		r.mu.RUnlock()
		return Result{}, fmt.Errorf("iot device %q not found", deviceName)
	}
	command := xiaozhiapi.ServerEventIotCommand{
		Name:       deviceName,
		Method:     method,
		Parameters: params,
	}
	r.mu.RUnlock()
	return Result{
		OK:      true,
		Data:    fmt.Sprintf("%s的%s操作执行成功", deviceName, method),
		Command: &command,
	}, nil
}

func toolsForDescriptor(descriptor xiaozhiapi.IotDescriptor) map[string]Tool {
	tools := make(map[string]Tool)
	for propName, propInfo := range descriptor.Properties {
		name := fmt.Sprintf("get_%s_%s", normalizeName(descriptor.Name), normalizeName(propName))
		tools[name] = Tool{
			Name:        name,
			Description: fmt.Sprintf("查询%s的%s", descriptor.Description, propInfo.Description),
			DeviceName:  descriptor.Name,
			Kind:        ToolKindQuery,
			Property:    propName,
			Parameters: map[string]xiaozhiapi.IotPropertySchema{
				"response_success": {Type: "string", Description: "查询成功时的友好回复，必须使用{value}作为占位符表示查询到的值"},
				"response_failure": {Type: "string", Description: fmt.Sprintf("查询失败时的友好回复，例如：'无法获取%s的%s'", descriptor.Name, propInfo.Description)},
			},
			Required: []string{"response_success", "response_failure"},
		}
	}
	for methodName, methodInfo := range descriptor.Methods {
		name := fmt.Sprintf("%s_%s", normalizeName(descriptor.Name), normalizeName(methodName))
		parameters := make(map[string]xiaozhiapi.IotPropertySchema, len(methodInfo.Parameters)+2)
		required := make([]string, 0, len(methodInfo.Parameters)+2)
		for paramName, paramInfo := range methodInfo.Parameters {
			parameters[paramName] = paramInfo
			required = append(required, paramName)
		}
		sort.Strings(required)
		parameters["response_success"] = xiaozhiapi.IotPropertySchema{Type: "string", Description: "操作成功时的友好回复,关于该设备的操作结果，设备名称尽量使用description中的名称"}
		parameters["response_failure"] = xiaozhiapi.IotPropertySchema{Type: "string", Description: "操作失败时的友好回复,关于该设备的操作结果，设备名称尽量使用description中的名称"}
		required = append(required, "response_success", "response_failure")
		tools[name] = Tool{
			Name:        name,
			Description: fmt.Sprintf("%s - %s", descriptor.Description, methodInfo.Description),
			DeviceName:  descriptor.Name,
			Kind:        ToolKindControl,
			Method:      methodName,
			Parameters:  parameters,
			Required:    required,
		}
	}
	return tools
}

func normalizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	replacer := strings.NewReplacer(" ", "_", "-", "_", ".", "_", "/", "_")
	return replacer.Replace(name)
}

func commandParameters(args map[string]any) map[string]any {
	params := make(map[string]any)
	for k, v := range args {
		if k == "response_success" || k == "response_failure" {
			continue
		}
		params[k] = v
	}
	return params
}

func responseSuccess(args map[string]any, fallback string) string {
	if v, ok := args["response_success"].(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func responseFailure(args map[string]any, fallback string) string {
	if v, ok := args["response_failure"].(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func replaceValue(text string, value any) string {
	return strings.ReplaceAll(text, "{value}", fmt.Sprint(value))
}

func formatControlResponse(text string, params map[string]any) string {
	for name, value := range params {
		text = strings.ReplaceAll(text, "{"+name+"}", fmt.Sprint(value))
		if strings.Contains(text, "{value}") {
			text = strings.ReplaceAll(text, "{value}", fmt.Sprint(value))
		}
	}
	return text
}
