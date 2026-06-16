package xiaozhi

import "testing"

func TestUnmarshalClientEventReturnsRawTextForNonJSON(t *testing.T) {
	event, err := UnmarshalClientEvent([]byte("plain ping"))
	if err != nil {
		t.Fatalf("unmarshal raw text: %v", err)
	}
	raw, ok := event.(*ClientEventRawText)
	if !ok {
		t.Fatalf("expected raw text event, got %T", event)
	}
	if raw.Text != "plain ping" {
		t.Fatalf("unexpected raw text: %q", raw.Text)
	}
}

func TestUnmarshalClientEventReturnsRawTextForJSONInteger(t *testing.T) {
	event, err := UnmarshalClientEvent([]byte("123"))
	if err != nil {
		t.Fatalf("unmarshal json integer: %v", err)
	}
	raw, ok := event.(*ClientEventRawText)
	if !ok {
		t.Fatalf("expected raw text event, got %T", event)
	}
	if raw.Text != "123" {
		t.Fatalf("unexpected raw text: %q", raw.Text)
	}
}

func TestUnmarshalClientEventReturnsUnknownForJSONNonObject(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`"plain ping"`),
		[]byte(`true`),
		[]byte(`["ping"]`),
	} {
		event, err := UnmarshalClientEvent(input)
		if err != nil {
			t.Fatalf("unmarshal %s: %v", input, err)
		}
		if _, ok := event.(*ClientEventUnknown); !ok {
			t.Fatalf("expected unknown for %s, got %T", input, event)
		}
	}
}

func TestUnmarshalClientEventReturnsUnknownForUnknownJSONType(t *testing.T) {
	event, err := UnmarshalClientEvent([]byte(`{"type":"future","payload":1}`))
	if err != nil {
		t.Fatalf("unmarshal unknown event: %v", err)
	}
	unknown, ok := event.(*ClientEventUnknown)
	if !ok {
		t.Fatalf("expected unknown event, got %T", event)
	}
	if unknown.Type != "future" {
		t.Fatalf("unknown type got %q", unknown.Type)
	}
	if string(unknown.Raw) != `{"type":"future","payload":1}` {
		t.Fatalf("unknown raw got %q", unknown.Raw)
	}
}

func TestUnmarshalClientBinEventWrapsOpusBytesAsAppendBuffer(t *testing.T) {
	event, err := UnmarshalClientBinEvent([]byte{0x01, 0x02, 0x03})
	if err != nil {
		t.Fatalf("unmarshal binary event: %v", err)
	}
	audio, ok := event.(*ClientEventAppendBuffer)
	if !ok {
		t.Fatalf("expected append buffer event, got %T", event)
	}
	if audio.Type != ClientEventTypeAppendBuffer {
		t.Fatalf("binary event type got %q", audio.Type)
	}
	if string(audio.Bytes) != string([]byte{0x01, 0x02, 0x03}) {
		t.Fatalf("binary payload changed: %v", audio.Bytes)
	}
}

func TestUnmarshalIotDescriptorAcceptsArrayPropertiesAndMethods(t *testing.T) {
	event, err := UnmarshalClientEvent([]byte(`{
		"type": "iot",
		"descriptors": [{
			"name": "Lamp",
			"description": "台灯",
			"properties": [
				{"name": "power", "type": "boolean", "description": "开关"}
			],
			"methods": [
				{
					"name": "SetPower",
					"description": "设置开关",
					"parameters": {
						"value": {"type": "boolean", "description": "目标状态"}
					}
				}
			]
		}]
	}`))
	if err != nil {
		t.Fatalf("unmarshal iot descriptor: %v", err)
	}
	iot, ok := event.(*ClientEventIot)
	if !ok {
		t.Fatalf("expected iot event, got %T", event)
	}
	if len(iot.Descriptors) != 1 {
		t.Fatalf("descriptor count got %d", len(iot.Descriptors))
	}
	descriptor := iot.Descriptors[0]
	if descriptor.Properties["power"].Name != "power" || descriptor.Properties["power"].Type != "boolean" {
		t.Fatalf("property not normalized: %+v", descriptor.Properties)
	}
	method := descriptor.Methods["SetPower"]
	if method.Name != "SetPower" || method.Parameters["value"].Type != "boolean" {
		t.Fatalf("method not normalized: %+v", descriptor.Methods)
	}
}
