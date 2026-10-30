# ESP32-S3-BOX-3 信息面板与 Grok Bot 主动提醒方案

> **修订说明（2026-10-30）**：按评审后的职责拆分改写。`go-xiaozhi` 定位为 **router**（对接各类 LLM/Bot，并提供 ASR/TTS 与设备通道）；**提醒的主数据与调度在 Grok Bot**。已去掉「小智侧 SQLite 提醒库 / 服务端调度器作为 SoR」、以及 Tailscale 网络假设。

## 1. 目标

在现有 `go-xiaozhi` 服务和 ESP32-S3-BOX-3 固件基础上，增加一套适合 320×240 LCD 的桌面信息终端体验：

- 首页显示时钟、日期、天气、网络状态和下一条提醒摘要（摘要可由服务端下发或本地缓存最近一条通知）。
- 保留现有语音聊天、表情和 TTS 能力。
- 支持 **外部 Bot（Grok Bot）主动调用**，向设备推送提醒，并由设备显示、播报和确认。
- 提醒的创建、修改、取消、查询与定时触发，由 **Grok Bot（含 routine）** 负责。
- 复用现有 Xiaozhi WebSocket 协议、天气服务、设备 session 和 IoT 机制。

核心原则：

- **ESP32**：实时 UI、音频和交互。
- **go-xiaozhi**：router + ASR/TTS + 设备会话路由 + 推送通道（协议与短生命周期投递状态）。
- **Grok Bot**：提醒系统的真相源（SoR）与调度；到点调用小智的 notify API。

不把 Grok/API Key 写入 ESP32 固件；也不把提醒日历存进小智。

## 2. 现状与复用点

### 2.1 `go-xiaozhi` 已有能力

- Xiaozhi WebSocket 服务和设备 session 管理。
- ASR、LLM、TTS 和 Opus 音频链路（LLM 为可插拔后端，与 Grok Bot 不是同一概念）。
- `service/weather` 天气查询服务。
- MCP / 内置 tool 机制和 IoT 命令处理（本方案 v1 **不**用其承载提醒 SoR）。
- 设备私有配置和设备 ID。
- 服务端时间下发能力。
- 持久化现状：短记忆 / 私有配置多为 **YAML 文件**；可选 `mlat` 记忆间接依赖 SQLite，与提醒无关。

### 2.2 ESP32 固件已具备能力

- LVGL LCD UI。
- ESP32-S3-BOX-3 音频输入输出。
- 设备状态、表情、聊天消息和通知显示。
- `Application::Alert()`、任务调度和协议事件处理。
- MQTT/WebSocket 设备通信。

## 3. 总体架构

```text
┌──────────────────┐
│     Grok Bot     │  提醒 SoR / routine 调度 / 自然语言建提醒
│  create/fire/CRUD│
└────────┬─────────┘
         │ HTTP POST /v1/notify  (localhost 或局域网 + Bearer token)
         ▼
┌──────────────────┐       ┌──────────────────┐
│ Weather API      │──────▶│    go-xiaozhi    │──────▶│ ESP32-S3-BOX-3   │
└──────────────────┘       │ router           │ WS/MQTT│ LCD / audio / btn │
                           │ ASR/TTS          │       └──────────────────┘
                           │ notify → device  │
                           │ LLM backends...  │
                           └──────────────────┘
```

说明：

- 日常语音对话仍走小智配置的 OpenAI 兼容 LLM（或其它后端），**不等于** Grok Bot。
- Grok Bot 通过受控 HTTP 进入小智；小智再把 `notification` 推到在线设备。
- 网络：**同机 `127.0.0.1`，或局域网 IP + token**。不依赖 Tailscale；穿透/公网后续另议。

## 4. 设备端 UI 设计

### 4.1 Dashboard 首页

适配 320×240 横向 LCD：

```text
┌────────────────────────┐
│ 10月30日 周五   Wi-Fi  │
│                        │
│        14:32           │
│                        │
│ ☀  24°C  多云          │
│    深圳  湿度 68%      │
│                        │
│ 下一提醒 15:00 项目会议 │
└────────────────────────┘
```

建议显示内容：

- 大号时钟，每秒本地刷新。
- 日期和星期。
- 天气图标、温度、天气描述、湿度和城市。
- Wi-Fi、静音和设备状态图标。
- 最近一条 / 下一条提醒摘要（来自最近收到的 `notification` 或服务端下发的摘要字段）。
- 空闲时显示 AI 表情或轻量动画。

### 4.2 Chat 页面

保留当前语音交互逻辑：正在聆听 / 思考 / 播放、最近用户与 AI 文本、情绪表情。

### 4.3 Reminder 页面

提醒到达时覆盖或切换到提醒页：

- 提醒标题与内容。
- 来源标识：Grok Bot / 系统。
- 触发时间与优先级。
- 确认、稍后、关闭状态。

第一版不依赖触摸屏，使用按键和语音：

- BOOT 短按：开始/停止对话；提醒期间确认。
- BOOT 长按：Dashboard 与 Chat 页面切换。
- MUTE：静音或恢复播报。

## 5. 时钟方案

- 服务端连接建立时提供可靠时间。
- ESP32 使用本地系统时间和 `localtime_r()` 显示。
- 网络可用时通过 SNTP/NTP 校准。
- 每秒只更新时钟控件，不重建页面。
- 时区默认 `Asia/Shanghai`。
- 断网时继续显示最近一次有效时间。

ESP32 不需要每秒从服务端收时间。

## 6. 天气方案

现有 `service/weather/weather.go` 面向 LLM 返回自然语言报告。建议保留 `Query()`，新增结构化查询：

```go
type WeatherSnapshot struct {
    City        string    `json:"city"`
    Weather     string    `json:"weather"`
    Temperature int       `json:"temperature"`
    FeelsLike   int       `json:"feels_like,omitempty"`
    Humidity    int       `json:"humidity,omitempty"`
    Wind        string    `json:"wind,omitempty"`
    ReportTime  time.Time `json:"report_time"`
}

func (c *Client) QuerySnapshot(ctx context.Context, location string) (WeatherSnapshot, error)
```

更新策略：

- 设备上线后请求一次。
- 默认每 30 分钟更新一次。
- 服务端缓存天气，避免每台设备重复打供应商。
- 断网保留最后一次天气并显示更新时间。
- 天气失败不影响语音聊天和本地时钟。
- 城市保存在设备私有配置或服务端设备配置中。

若设备仅亮屏、无 WebSocket session，需约定短连拉取或 MQTT 旁路（v1 可要求面板刷新依赖已连接 session）。

## 7. 协议扩展

### 7.1 新增服务端事件类型

在现有 `hello`、`stt`、`llm`、`tts`、`iot` 之外增加：

```text
notification
weather
```

### 7.2 `notification` 服务端事件

```json
{
  "type": "notification",
  "session_id": "session-id",
  "id": "reminder-20261030-1500",
  "category": "reminder",
  "title": "项目会议",
  "content": "15分钟后开始项目会议，请准备 LCD 方向测试结果。",
  "trigger_at": 1793334000,
  "priority": "normal",
  "speak": true,
  "display_seconds": 15
}
```

字段约定：

- `id`：幂等 ID（由 Grok Bot 生成并带入），设备与小智避免重复显示/播报。
- `category`：`reminder`、`weather_alert`、`system`。
- `priority`：`low`、`normal`、`high`。
- `speak`：是否走 TTS（与聊天抢麦时的打断策略见风险节；v1 需定一条简单规则）。
- `display_seconds`：默认显示时长。

### 7.3 `weather` 服务端事件

```json
{
  "type": "weather",
  "session_id": "session-id",
  "city": "深圳",
  "weather": "多云",
  "temperature": 24,
  "feels_like": 25,
  "humidity": 68,
  "wind": "东北风 2级",
  "report_time": "2026-10-30T14:30:00+08:00"
}
```

### 7.4 设备回执事件

```json
{
  "type": "notification_ack",
  "session_id": "session-id",
  "id": "reminder-20261030-1500",
  "action": "ack",
  "timestamp": 1793334005
}
```

`action`：`shown`、`ack`、`snooze`、`dismiss`、`failed`。

小智可将回执转发或记录，供 Grok Bot 查询（HTTP 查询接口可选；v1 至少本地日志/短期状态）。

## 8. 提醒与推送（职责拆分）

### 8.1 Grok Bot（SoR + 调度）

- 保存提醒内容、触发时间、重复规则、目标 `device_id`。
- 用户通过与 Grok Bot 对话或配置 routine 创建/修改/取消/查询。
- 到点（或提前 N 分钟）调用 go-xiaozhi 的 notify API。
- 根据需要处理 snooze：Bot 侧改下次触发时间，再推一次。

### 8.2 go-xiaozhi（推送通道）

提供受控 HTTP（示例）：

```http
POST /v1/notify
Authorization: Bearer <token>
Content-Type: application/json

{
  "device_id": "aa:bb:cc:dd:ee:ff",
  "id": "reminder-20261030-1500",
  "category": "reminder",
  "title": "项目会议",
  "content": "15分钟后开始项目会议。",
  "trigger_at": 1793334000,
  "priority": "normal",
  "speak": true,
  "display_seconds": 15
}
```

行为：

1. 校验 token。
2. 按 `device_id` 找在线 session。
3. 经现有写队列下发 `notification`（禁止并发直写 WebSocket）。
4. `speak: true` 时按约定插入 TTS 或短提示音。
5. 设备离线：进入 **短期待投递**（内存或小型 JSON/YAML）；重连后补发；已 `ack` 的不再播。
6. **不**实现完整提醒日历、cron 扫描器或 SQLite 提醒表作为 MVP。

可选：`GET /v1/devices/{id}/notify/{notify_id}` 查投递状态；`POST` snooze/dismiss 代理回 Grok Bot（v1 可只做设备 ack → 小智状态）。

### 8.3 语音里「帮我定个提醒」

v1 **主路径**是用户对 Grok Bot 说，或在 Bot 里配 routine。  
在小智会话内用 LLM tool 建提醒 = 可选后续；若做，也应把 CRUD 转发到 Grok Bot / 外部 API，而不是在小智落库。

## 9. 服务端改造清单

```text
pkg/protocol/xiaozhi/server_event.go   # notification / weather
pkg/protocol/xiaozhi/client_event.go   # notification_ack
handler/local/session.go               # 主动写队列推送
handler/local/conn.go
service/weather/weather.go             # QuerySnapshot + cache
service/notify/                        # HTTP notify + 短期 pending/幂等
```

主要任务：

1. 增加 `notification`、`weather`、`notification_ack` 协议结构与测试。
2. session 支持服务端主动写消息（复用写队列）。
3. 结构化天气查询与缓存、设备下发。
4. 增加带 token 的 HTTP notify API。
5. 可选：离线 pending（内存或 JSON/YAML），重连补发；**不上**提醒 SoR / SQLite 调度器。
6. 文档与最小集成测试（mock session 推送）。

## 10. ESP32 改造清单

```text
main/display/dashboard_view.cc
main/display/chat_view.cc
main/display/reminder_view.cc
main/display/ui_manager.cc
main/services/clock_service.cc
main/services/weather_service.cc
main/services/reminder_service.cc
```

主要任务：

1. Dashboard / Chat / Reminder 三视图。
2. 本地时钟与日期。
3. 天气事件解析与缓存。
4. 提醒弹窗、提示音 / TTS 播报状态。
5. 确认与 `notification_ack`。
6. BOOT 长按页面切换。
7. 官方与商家 LCD 配置隔离。

## 11. 分阶段实施

### 阶段一：UI MVP

模拟天气和提醒数据：Dashboard、时钟日期、天气卡片、Chat、Reminder 弹窗、BOOT 切换。  
验收：上电稳定显示 Dashboard，语音页不退化。

### 阶段二：协议扩展

- `notification` / `weather` / `notification_ack`。
- Go 协议单测。
- 测试接口向在线设备推送，屏幕显示并能确认。

### 阶段三：真实天气

复用天气配置，结构化快照、缓存与下发。  
验收：Dashboard 真实天气；失败显示缓存与更新时间。

### 阶段四：HTTP notify + 投递

- Token 鉴权的 `/v1/notify`。
- 在线推送、ack、幂等 `id`。
- 可选离线 pending + 重连补发（非提醒日历）。

验收：用 curl/局域网调用 notify，设备显示并播报；断线补发不重复已确认项。

### 阶段五：Grok Bot 对接

- Grok Bot routine / 对话到点调用 notify。
- 提醒 CRUD 与调度留在 Bot 侧。
- （可选）ack 结果回传 Bot。

验收：在 Grok Bot 创建定时提醒后，设备能主动显示并播报；取消后不再推送。

暂缓：触摸屏、多设备管理后台、复杂条件提醒、天气雷达、把提醒 SoR 迁回小智、Tailscale/公网穿透。

## 12. 风险与边界

- 密钥不进 ESP32；notify API 必须鉴权，仅信任持有 token 的调用方。
- 主动推送必须走现有写队列。
- 设备离线：短期 pending 即可；长期日历在 Grok Bot。
- `speak: true` 与聊天 TTS 的优先级 / 打断策略 v1 需写死一条（例如：high 可打断，normal 等当前句结束）。
- 时间统一用带时区的时间戳；Bot 与小智约定 UTC 或明确 offset。
- 勿将「Grok Bot」与「llm.base_url 上的 OpenAI 兼容模型」混为一谈。

## 13. 第一版建议范围

1. ESP32 Dashboard：时钟、日期、模拟天气、设备状态。
2. 服务端 `weather` 结构化事件。
3. 服务端 `notification` + 设备 `notification_ack`。
4. HTTP `/v1/notify`（localhost/局域网 + token）。
5. Grok Bot 侧保存并调度提醒，到点调用 notify。

暂缓：小智内 SQLite 提醒库、小智内 cron 调度器、语音 LLM tool 建提醒、Tailscale、触摸屏。

## 14. 验收标准

- 官方与商家 LCD 配置互不影响。
- Dashboard 在 320×240 上可读、时钟每秒刷新不闪烁。
- 天气断网显示缓存，不阻塞语音。
- Grok Bot（或 curl）调用 notify 后，提醒能上屏并（在 `speak: true` 时）播报。
- 用户确认后服务端能收到 `notification_ack`。
- 同一 `id` 不重复播报已确认提醒；重连补发行为符合预期。
- 文档与实现一致：**提醒不落在 go-xiaozhi 作 SoR**。
- 相关 Go 协议 / notify 与固件新增逻辑有针对性测试。
