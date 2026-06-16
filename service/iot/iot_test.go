package iot

import (
	"strings"
	"testing"

	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
)

func TestRegistryBuildsToolsFromDescriptor(t *testing.T) {
	reg := NewRegistry()
	reg.AddDescriptors([]xiaozhiapi.IotDescriptor{lampDescriptor()})

	tools := reg.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d: %+v", len(tools), tools)
	}
	if tools[0].Name != "get_lamp_power" || tools[1].Name != "lamp_setpower" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if tools[1].Parameters["value"].Type != "boolean" {
		t.Fatalf("method parameters not preserved: %+v", tools[1].Parameters)
	}
}

func TestRegistryVoiceToolsMatchPythonResponseParameters(t *testing.T) {
	reg := NewRegistry()
	reg.AddDescriptors([]xiaozhiapi.IotDescriptor{lampDescriptor()})

	tools := reg.VoiceTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 voice tools, got %d: %+v", len(tools), tools)
	}
	query := tools[0]
	if query.Name != "get_lamp_power" {
		t.Fatalf("unexpected first tool: %+v", query)
	}
	if got := query.Parameters.Properties["response_success"].Description; !strings.Contains(got, "{value}") {
		t.Fatalf("query response_success should require value placeholder, got %q", got)
	}
	if got, want := query.Parameters.Required, []string{"response_success", "response_failure"}; !equalStrings(got, want) {
		t.Fatalf("query required got %v want %v", got, want)
	}

	control := tools[1]
	if control.Name != "lamp_setpower" {
		t.Fatalf("unexpected second tool: %+v", control)
	}
	if _, ok := control.Parameters.Properties["response_success"]; !ok {
		t.Fatalf("control tool missing response_success: %+v", control.Parameters.Properties)
	}
	if _, ok := control.Parameters.Properties["response_failure"]; !ok {
		t.Fatalf("control tool missing response_failure: %+v", control.Parameters.Properties)
	}
}

func TestRegistryQueryToolReadsLatestState(t *testing.T) {
	reg := NewRegistry()
	reg.AddDescriptors([]xiaozhiapi.IotDescriptor{lampDescriptor()})
	reg.UpdateStates([]xiaozhiapi.IotState{{Name: "Lamp", State: map[string]any{"power": true}}})

	result, err := reg.Invoke("get_lamp_power", map[string]any{
		"response_success": "灯的状态是{value}",
		"response_failure": "查不到灯",
	})
	if err != nil {
		t.Fatalf("invoke query: %v", err)
	}
	if !result.OK || result.Data != "true" || result.Message != "灯的状态是true" {
		t.Fatalf("unexpected query result: %+v", result)
	}
}

func TestRegistryInitializesDescriptorDefaultStateLikePython(t *testing.T) {
	reg := NewRegistry()
	reg.AddDescriptors([]xiaozhiapi.IotDescriptor{{
		Name:        "Speaker",
		Description: "扬声器",
		Properties: map[string]xiaozhiapi.IotPropertySchema{
			"enabled": {Type: "boolean", Description: "开关"},
			"name":    {Type: "string", Description: "名称"},
			"volume":  {Type: "number", Description: "音量"},
		},
	}})

	tests := []struct {
		tool string
		want string
	}{
		{tool: "get_speaker_enabled", want: "false"},
		{tool: "get_speaker_name", want: ""},
		{tool: "get_speaker_volume", want: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			result, err := reg.Invoke(tt.tool, map[string]any{
				"response_success": "值是{value}",
				"response_failure": "查询失败",
			})
			if err != nil {
				t.Fatalf("invoke query: %v", err)
			}
			if !result.OK || result.Data != tt.want || result.Message != "值是"+tt.want {
				t.Fatalf("unexpected default state query result: %+v", result)
			}
		})
	}
}

func TestRegistryAcceptsStateValuesWithoutDescriptorTypeFilteringLikePython(t *testing.T) {
	reg := NewRegistry()
	reg.AddDescriptors([]xiaozhiapi.IotDescriptor{lampDescriptor()})
	reg.UpdateStates([]xiaozhiapi.IotState{{Name: "Lamp", State: map[string]any{"power": true}}})
	reg.UpdateStates([]xiaozhiapi.IotState{{Name: "Lamp", State: map[string]any{"power": "false"}}})

	result, err := reg.Invoke("get_lamp_power", map[string]any{
		"response_success": "灯的状态是{value}",
		"response_failure": "查不到灯",
	})
	if err != nil {
		t.Fatalf("invoke query: %v", err)
	}
	if result.Data != "false" || result.Message != "灯的状态是false" {
		t.Fatalf("state update should preserve latest reported value like Python, got %+v", result)
	}
}

func TestRegistryIgnoresUnknownDeviceStatesLikePython(t *testing.T) {
	reg := NewRegistry()
	reg.UpdateStates([]xiaozhiapi.IotState{{Name: "Unknown", State: map[string]any{"power": true}}})

	if _, ok := reg.GetState("Unknown", "power"); ok {
		t.Fatal("unknown device state should not be created before descriptor")
	}
}

func TestRegistryAcceptsNumberStateTypes(t *testing.T) {
	reg := NewRegistry()
	reg.AddDescriptors([]xiaozhiapi.IotDescriptor{{
		Name:        "Speaker",
		Description: "音箱",
		Properties: map[string]xiaozhiapi.IotPropertySchema{
			"volume": {Type: "number", Description: "音量"},
		},
	}})
	reg.UpdateStates([]xiaozhiapi.IotState{{Name: "Speaker", State: map[string]any{"volume": float64(42)}}})

	result, err := reg.Invoke("get_speaker_volume", map[string]any{
		"response_success": "音量是{value}",
		"response_failure": "查不到音量",
	})
	if err != nil {
		t.Fatalf("invoke query: %v", err)
	}
	if result.Data != "42" || result.Message != "音量是42" {
		t.Fatalf("unexpected number query result: %+v", result)
	}
}

func TestRegistryControlToolBuildsDeviceCommand(t *testing.T) {
	reg := NewRegistry()
	reg.AddDescriptors([]xiaozhiapi.IotDescriptor{lampDescriptor()})

	result, err := reg.Invoke("lamp_setpower", map[string]any{
		"value":            false,
		"response_success": "已设置为{value}",
		"response_failure": "设置失败",
	})
	if err != nil {
		t.Fatalf("invoke control: %v", err)
	}
	if !result.OK || result.Command == nil {
		t.Fatalf("expected command result, got %+v", result)
	}
	if result.Command.Name != "Lamp" || result.Command.Method != "SetPower" {
		t.Fatalf("unexpected command: %+v", result.Command)
	}
	if got := result.Command.Parameters["value"]; got != false {
		t.Fatalf("unexpected command parameter: %#v", got)
	}
	if _, ok := result.Command.Parameters["response_success"]; ok {
		t.Fatalf("response fields must not be sent to device: %+v", result.Command.Parameters)
	}
	if result.Message != "已设置为false" {
		t.Fatalf("unexpected response message: %q", result.Message)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func lampDescriptor() xiaozhiapi.IotDescriptor {
	return xiaozhiapi.IotDescriptor{
		Name:        "Lamp",
		Description: "台灯",
		Properties: map[string]xiaozhiapi.IotPropertySchema{
			"power": {Type: "boolean", Description: "开关"},
		},
		Methods: map[string]xiaozhiapi.IotMethodSchema{
			"SetPower": {
				Description: "设置开关",
				Parameters: map[string]xiaozhiapi.IotPropertySchema{
					"value": {Type: "boolean", Description: "目标状态"},
				},
			},
		},
	}
}
