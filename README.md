# go-xiaozhi

Go implementation of the local `xiaozhi-server` voice service. This branch is not an upstream proxy: devices connect to this process with the existing Xiaozhi WebSocket protocol, and the server runs ASR, LLM, TTS, memory, private device config, and IoT tool handling locally through Go interfaces.

## Protocol

- WebSocket path: `/xiaozhi/v1/`
- Server sends Python-compatible `hello` after connection, including `version`, `transport`, `audio_params`, and `session_id`.
- Client text events supported: `hello`, `listen`, `abort`, `iot`.
- Client audio is binary Opus frames.
- Server response events preserve the device-facing Python service shape: `hello`, `stt`, `llm`, `tts`, `iot`.
- TTS audio is sent as binary Opus frames between `tts sentence_start` and `tts sentence_end`.
- If enabled, stop notification audio is sent as binary Opus frames immediately before `tts stop`; the JSON protocol shape is unchanged.

## Run

Install `ffmpeg` and make sure it is available in `PATH`. The Go service checks this at startup, matching the Python server preflight, because custom audio assets and command/custom providers may need audio decoding.

```bash
go run cmd/main.go
```

To use an explicit config file, pass the Python-compatible startup argument:

```bash
go run cmd/main.go --config_path data/.config.yaml
```

Without `--config_path`, configuration is loaded from the first existing file in this order: `data/.config.yaml`, `conf/biz.yaml`, `biz.yaml`, then `config.yaml`. This keeps the Python server's private `data/.config.yaml` and root `config.yaml` workflows while preserving the Go default in `conf/biz.yaml`.

The bind address is configured with:

```yaml
server:
  ip: 0.0.0.0
  port: 80
```

## Providers

The default config uses local stubs so the device protocol can be tested without external services:

```yaml
asr:
  type: file_stub
llm:
  type: echo
tts:
  type: stub
```

For an OpenAI-compatible deployment:

```yaml
asr:
  type: openai
  api_url: "https://api.openai.com/v1/audio/transcriptions"
  api_key: "..."
  model: "whisper-1"
  language: "zh"
  response_format: "json"
  output_dir: tmp
  delete_audio: true

llm:
  type: openai
  base_url: "https://api.openai.com/v1"
  api_key: "..."
  model: "gpt-4.1-mini"
  max_tokens: 800

tts:
  type: openai
  api_url: "https://api.openai.com/v1/audio/speech"
  api_key: "..."
  model: "tts-1"
  voice: "alloy"
  response_format: "wav"
  speed: 1.0
  sample_rate: 16000
  channels: 1
  frame_size: 960
```

OpenAI-compatible ASR uploads a generated WAV file. OpenAI-compatible TTS expects a PCM WAV response and converts it to Opus frames for the device.

Legacy Python `selected_module` LLM entries with `type: ollama` or `type: xinference` are normalized to the same OpenAI-compatible client by appending `/v1` to `base_url` when needed.

### Local Inference (sherpa-onnx)

VAD, ASR, and TTS can run fully offline via [sherpa-onnx-go](https://github.com/k2-fsa/sherpa-onnx-go) (CGO + prebuilt shared libs). This mirrors the Python `SileroVAD` / `FunASR(SenseVoice)` providers, and adds an offline Kokoro TTS. If a model file is missing, the factory logs a warning and falls back to `energy` VAD / `file_stub` ASR / `stub` TTS respectively, so the server always starts.

**VAD (Silero)** — drop `silero_vad.onnx` under `models/` (a symlink is fine):

```yaml
session:
  vad:
    type: silero
    model_dir: models/silero_vad.onnx   # directory or .onnx file
    threshold: 0.5
    min_silence_duration_ms: 1000
```

Model source: <https://github.com/k2-fsa/sherpa-onnx/releases> (`vad-models/silero_vad.onnx`, ~2.3 MB), or reuse the one already shipped with the Python project at `xiaozhi-esp32-server/main/xiaozhi-server/models/snakers4_silero-vad/src/silero_vad/data/silero_vad.onnx`:

```bash
mkdir -p models
ln -sf ../xiaozhi-esp32-server/main/xiaozhi-server/models/snakers4_silero-vad/src/silero_vad/data/silero_vad.onnx models/silero_vad.onnx
```

**ASR (SenseVoice)** — download `sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17` into `models/` (from <https://github.com/k2-fsa/sherpa-onnx/releases>, `asr-models`), then:

```yaml
asr:
  type: sherpa_sensevoice
  model_dir: models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17
  language: auto          # zh / en / ja / ko / yue / auto
  sample_rate: 16000
  channels: 1
```

**TTS (Kokoro)** — download `kokoro-multi-lang-v1_1` into `models/` (Chinese + English, 103 speakers). The model lives on Hugging Face under `csukuangfj/kokoro-multi-lang-v1_1`; GitHub releases (`tts-models/kokoro-multi-lang-v1_1.tar.bz2`) are an equivalent single tarball. Then:

```bash
# via HF mirror (China-friendly):
HF_ENDPOINT=https://hf-mirror.com huggingface-cli download \
  csukuangfj/kokoro-multi-lang-v1_1 --local-dir models/kokoro-multi-lang-v1_1 \
  --local-dir-use-symlinks False
```

```yaml
tts:
  type: kokoro
  model_dir: models/kokoro-multi-lang-v1_1
  sid: 0                  # speaker id (0..102); 0 = default female voice
  speed: 1.0              # Kokoro LengthScale: <1 faster, >1 slower
  silence_scale: 0.2      # inter-sentence silence ratio
  sample_rate: 16000
  channels: 1
  frame_size: 960
```

Notes:

- sherpa-onnx consumes **16 kHz mono float32** PCM; the adapters resample and normalize internally.
- `models/` is git-ignored. CGO must be enabled (`CGO_ENABLED=1`, default on Linux/macOS with a C toolchain).


Python provider bridges can be configured through local commands. These keep the device-facing Xiaozhi protocol unchanged while letting an existing Python wrapper run FunASR, EdgeTTS, SileroVAD, or similar local engines.

Command ASR receives a generated WAV path and must print the transcript to stdout:

```yaml
asr:
  type: command
  command: python3
  args: ["scripts/funasr_bridge.py", "{file}", "{session_id}"]
  env:
    FUNASR_MODEL: "iic/SenseVoiceSmall"
  output_dir: tmp
  delete_audio: true
  sample_rate: 16000
  channels: 1
```

Command TTS receives text and an output path. The command writes audio to `{output}`; `format` tells Go how to decode that file before sending Opus frames to the device:

```yaml
tts:
  type: command
  command: python3
  args: ["scripts/edge_tts_bridge.py", "--text", "{text}", "--output", "{output}"]
  env:
    EDGE_TTS_VOICE: "zh-CN-XiaoxiaoNeural"
  format: mp3
  output_dir: tmp
  sample_rate: 16000
  channels: 1
  frame_size: 960
```

Command VAD receives the current PCM frame as a WAV file and must print either a boolean-like value (`true`, `false`, `speech`, `silence`) or a probability. Probabilities are compared with `threshold`:

```yaml
session:
  vad:
    type: command
    command: python3
    args: ["scripts/silero_vad_bridge.py", "{file}"]
    env:
      SILERO_MODEL_DIR: "models/snakers4_silero-vad"
    threshold: 0.5
    output_dir: tmp
    sample_rate: 16000
    channels: 1
```

Custom HTTP TTS is also available for Python-compatible GET-style TTS services such as GPT-SoVITS wrappers. `{prompt_text}` is replaced with the text to synthesize:

```yaml
tts:
  type: custom
  url: "http://127.0.0.1:9880/tts"
  format: wav
  params:
    text: "{prompt_text}"
    speaker: "default"
  headers:
    Authorization: "Bearer token"
  sample_rate: 16000
  channels: 1
  frame_size: 960
```

Python-compatible stop notification audio can be enabled with:

```yaml
tts:
  stop_notify:
    enabled: true
    path: config/assets/tts_notify.mp3
```

The notify asset can be a raw Opus packet file, a sequence of big-endian length-prefixed Opus packets, a Python `.p3` Opus packet file, a 16-bit PCM WAV file, or an MP3/other audio file that `ffmpeg` can decode. Non-Opus assets are converted to device Opus frames before they are sent.

## Audio Flow

Binary Opus frames are paced to match the Python service's device playback behavior:

```yaml
audio_flow:
  enabled: true
  pre_buffer_frames: 3
  frame_duration_ms: 60
  send_rate_multiplier: 1.0
  max_delay_ms: 100
```

Sentence events also keep the Python-style interval between segments:

```yaml
sentence_delay:
  enabled: true
  base_delay_ms: 200
  dynamic: true
  length_threshold: 30
  long_sentence_extra_ms: 300
```

## Memory And Private Config

Memory is disabled by default:

```yaml
memory:
  type: none
```

Local short memory can be enabled:

```yaml
memory:
  type: local_short
  path: data/.memory.yaml
  max_len: 4000
```

Per-device private config can be enabled:

```yaml
private_config:
  enabled: true
  path: data/.private_config.yaml
```

When enabled, the server loads or creates config by `device-id`. An unbound device receives the auth-code voice prompt before normal chat.

## Idle Close

Auto-listen sessions can trigger the Python-style long-silence closing prompt:

```yaml
session:
  no_voice_close_seconds: 120
  silence_stop_ms: 700
local:
  no_voice_prompt: "请你以“时间过得真快”未来头，用富有感情、依依不舍的话来结束这场对话吧。"
```

`silence_stop_ms` matches Python's VAD pause handling: after speech has started, trailing silence longer than this threshold finishes the utterance. After the idle-close prompt is spoken, the connection is closed.

## Intent Handling

The text path keeps Python-compatible short-circuit handling for direct exit commands and muted wake words:

```yaml
intent:
  exit_commands: ["退出", "再见", "拜拜", "结束对话"]
  wakeup_words: ["你好小智", "你好啊小智", "小智你好", "小智"]
  enable_greeting: true
  functions: ["play_music", "get_news", "get_weather", "baidu_search", "hass_get_state", "hass_set_state", "hass_play_music"]
```

If `enable_greeting` is set to `false`, a wake word only emits `stt` and `tts stop`; it does not call the LLM or save dialogue history.

Built-in function-call tools include required Python-style tools `handle_exit_intent`, `plugin_loader`, `get_time`, `get_lunar`, and `handle_device`; optional tools such as `play_music`, `get_news`, `get_weather`, `baidu_search`, `hass_get_state`, `hass_set_state`, and `hass_play_music` are exposed when listed in `intent.functions` or loaded at runtime through `plugin_loader`. `get_time` returns Python-compatible current date/time text to the LLM for a natural reply. `get_news` fetches RSS news from `plugins.get_news`, keeps the latest item for follow-up detail requests, and feeds a Python-compatible news prompt back to the LLM for natural播报. `get_weather` uses the Amap geocode and weather APIs, then feeds Python-compatible weather data back to the LLM. `baidu_search` fetches Baidu-style HTML search results and can extract result page text before the second LLM response. Home Assistant tools call the Python-compatible `/api/states`, `/api/services/<domain>/<action>`, and `music_assistant/play_media` endpoints. `get_lunar` provides locally computed calendar context for a second LLM response. `handle_device` controls Python-compatible `Speaker` volume and `Screen` brightness through the Xiaozhi IoT command event. `play_music` scans `plugins.play_music.music_dir` for `.mp3`, `.wav`, and `.p3` files, converts them to device Opus frames when needed, and sends the same Xiaozhi `stt`/`tts` event sequence used by the Python service.

For exact Python `cnlunar` parity, `get_lunar` can delegate to a local command. The command must print the tool text to stdout:

```yaml
intent:
  lunar_command: python3
  lunar_args: ["scripts/lunar_bridge.py", "--query", "{query}", "--date", "{date}", "--time", "{time}"]
```

Weather and RSS sources can be configured with:

```yaml
plugins:
  get_weather:
    api_key: "your_amap_api_key_here"
    default_location: "广州"
    base_url: "https://restapi.amap.com"
  baidu_search:
    enabled: false
    search_url: "https://www.baidu.com/s"
    max_results: 10
    max_extract_pages: 3
    max_content_length: 1000
  home_assistant:
    devices:
      - "客厅,音箱,media_player.living_room"
    base_url: "http://homeassistant.local:8123"
    api_key: "your_home_assistant_token"
  get_news:
    default_rss_url: "https://www.chinanews.com.cn/rss/society.xml"
    category_urls:
      society: "https://www.chinanews.com.cn/rss/society.xml"
      world: "https://www.chinanews.com.cn/rss/world.xml"
      finance: "https://www.chinanews.com.cn/rss/finance.xml"
```

MCP stdio servers can be enabled with either inline config or a Python-compatible `data/.mcp_server_settings.json` file. MCP tools are exposed to the LLM with the `mcp_` prefix:

```yaml
mcp:
  enabled: true
  path: data/.mcp_server_settings.json
  servers:
    filesystem:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
```

## IoT

Devices may send `iot` descriptors and states. The server builds LLM tools from descriptors:

- properties become query tools such as `get_lamp_power`
- methods become control tools such as `lamp_setpower`

Control tools send the original protocol `iot.commands` event to the device.

## Test

```bash
GOCACHE=/tmp/go-build-cache go test ./...
```

Some tests use `httptest` and temporary audio files, so restricted sandboxes may need permission to bind localhost ports and write temp files.
