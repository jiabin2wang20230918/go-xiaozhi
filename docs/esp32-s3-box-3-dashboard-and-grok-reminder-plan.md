# ESP32-S3-BOX-3 信息面板与 Grok 主动提醒方案

## 1. 目标

在现有 `go-xiaozhi` 服务和 ESP32-S3-BOX-3 固件基础上，增加一套适合 320×240 LCD 的桌面信息终端体验：

- 首页显示时钟、日期、天气、网络状态和下一条提醒。
- 保留现有语音聊天、表情和 TTS 能力。
- 支持服务端主动向设备推送提醒，并由设备显示、播报和确认。
- 通过 Grok Bot 创建、修改、取消和查询提醒。
- 复用现有 Xiaozhi WebSocket 协议、天气服务、设备 session 和 IoT 机制。

核心原则：**ESP32 负责实时 UI、音频和交互，服务端负责天气、Grok、提醒调度和持久化。**

## 2. 现状与复用点

### 2.1 `go-xiaozhi` 已有能力

- Xiaozhi WebSocket 服务和设备 session 管理。
- ASR、LLM、TTS 和 Opus 音频链路。
- `service/weather` 天气查询服务。
- MCP 工具机制和 IoT 命令处理。
- 设备私有配置和设备 ID。
- 服务端时间下发能力。
- OpenAI 兼容 LLM 配置，可作为 Grok 接入基础。

### 2.2 ESP32 固件已具备能力

- LVGL LCD UI。
- ESP32-S3-BOX-3 音频输入输出。
- 设备状态、表情、聊天消息和通知显示。
- `Application::Alert()`、任务调度和协议事件处理。
- MQTT/WebSocket 设备通信。

## 3. 总体架构

```text
                         ┌──────────────────┐
                         │      Grok Bot     │
                         │  对话/提醒意图识别 │
                         └────────┬─────────┘
                                  │ tool calls
                                  ▼
┌──────────────┐       ┌──────────────────┐       ┌──────────────────┐
│ Weather API  │──────▶│    go-xiaozhi    │──────▶│ ESP32-S3-BOX-3   │
└──────────────┘       │ weather/scheduler│ WS/MQTT│ LCD/audio/buttons │
                       │ reminder storage │       └──────────────────┘
                       └──────────────────┘
```

ESP32 不直接保存 Grok API Key，也不直接访问天气供应商。这样可以降低固件复杂度，避免密钥泄露，并支持后续增加多个设备。

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
- 下一条提醒摘要。
- 空闲时显示 AI 表情或轻量动画。

### 4.2 Chat 页面

保留当前语音交互逻辑：

- 正在聆听。
- 正在思考。
- 正在播放。
- 最近一条用户文本。
- 最近一条 AI 回复摘要。
- 情绪表情。

### 4.3 Reminder 页面

提醒到达时覆盖或切换到提醒页：

- 提醒标题。
- 提醒内容。
- 来源标识：Grok Bot / 本地提醒。
- 触发时间和优先级。
- 确认、稍后提醒和关闭状态。

第一版不依赖触摸屏，使用按键和语音：

- BOOT 短按：开始/停止对话；提醒期间确认。
- BOOT 长按：Dashboard 与 Chat 页面切换。
- MUTE：静音或恢复播报。

## 5. 时钟方案

- 服务端连接建立时提供可靠时间。
- ESP32 使用本地系统时间和 `localtime_r()` 显示。
- 网络可用时通过 SNTP/NTP 校准。
- 每秒只更新时钟控件，不重建页面。
- 配置时区，默认支持 `Asia/Shanghai`，后续可扩展。
- 断网时继续显示最近一次有效时间。

ESP32 不需要每秒从服务端接收时间，避免增加网络流量和 UI 延迟。

## 6. 天气方案

现有 `service/weather/weather.go` 面向 LLM 返回自然语言报告。建议保留现有 `Query()`，新增结构化查询接口：

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
- 服务端缓存天气，避免每台设备重复访问供应商。
- 设备断网时保留最后一次天气，并显示更新时间。
- 天气请求失败不影响语音聊天和本地时钟。

天气城市建议保存在设备私有配置或服务端设备配置中，不建议依赖 ESP32 GPS。

## 7. 协议扩展

### 7.1 新增服务端事件类型

现有事件包括 `hello`、`stt`、`llm`、`tts`、`iot`。建议增加：

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

- `id`：服务端幂等 ID，设备避免重复显示。
- `category`：`reminder`、`weather_alert`、`system`。
- `priority`：`low`、`normal`、`high`。
- `speak`：是否通过 TTS 播报。
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

建议新增客户端事件：

```json
{
  "type": "notification_ack",
  "session_id": "session-id",
  "id": "reminder-20261030-1500",
  "action": "ack",
  "timestamp": 1793334005
}
```

`action` 可取：`shown`、`ack`、`snooze`、`dismiss`、`failed`。

## 8. 提醒系统

### 8.1 数据模型

第一版使用 SQLite 即可：

```text
reminders
├── id
├── device_id
├── title
├── content
├── trigger_at
├── timezone
├── repeat_rule
├── priority
├── speak
├── status
├── created_at
└── updated_at
```

### 8.2 调度器职责

新增 `service/reminder`：

- 创建、更新、删除和查询提醒。
- 定时扫描到期任务。
- 根据 `device_id` 定位在线 session。
- 推送 `notification` 事件。
- 设备离线时保留待投递状态。
- 设备重连后补发未确认提醒。
- 依据回执更新提醒状态。
- 支持一次性、每日、每周和简单 cron 规则。

### 8.3 Grok 工具

通过现有 MCP/工具机制暴露：

```text
create_reminder(title, content, trigger_at, timezone, repeat_rule, priority)
cancel_reminder(reminder_id)
update_reminder(reminder_id, ...)
list_reminders(status)
snooze_reminder(reminder_id, duration)
```

示例对话：

```text
用户：明天下午三点提醒我开项目会议。
Grok：已为你设置明天 15:00 的项目会议提醒。
```

天气条件提醒可以由调度器执行：

```text
用户：如果明天下雨，提醒我带伞。
```

服务端保存条件，在天气刷新后判断是否触发，不依赖设备保持在线。

## 9. 服务端改造清单

建议按以下范围实施：

```text
pkg/protocol/xiaozhi/server_event.go
pkg/protocol/xiaozhi/client_event.go
service/weather/weather.go
service/reminder/reminder.go
service/reminder/scheduler.go
handler/local/session.go
handler/local/conn.go
```

主要任务：

1. 增加 `notification` 和 `weather` 事件结构。
2. 增加客户端 `notification_ack` 解析。
3. 为 session 增加服务端主动写消息能力。
4. 增加结构化天气查询和缓存。
5. 增加 SQLite 提醒存储。
6. 增加提醒调度器和断线补发。
7. 将提醒工具接入 Grok/MCP 工具列表。
8. 增加协议、天气和提醒单元测试。

## 10. ESP32 改造清单

建议新增 UI 和服务模块，避免把业务逻辑全部堆到 `display.cc`：

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

1. 将当前单页 UI 抽象成 Dashboard、Chat、Reminder 三个视图。
2. 增加本地时钟和日期刷新。
3. 增加天气事件解析和天气缓存。
4. 增加提醒弹窗、提示音和 TTS 播报状态。
5. 增加提醒确认和回执发送。
6. 增加 BOOT 长按页面切换。
7. 保持原厂 `ESP BOX 3` 与商家 LCD 配置隔离。

## 11. 分阶段实施

### 阶段一：UI MVP

使用模拟天气和提醒数据完成：

- Dashboard 页面。
- 大号时钟和日期。
- 天气卡片。
- Chat 页面。
- Reminder 弹窗。
- BOOT 页面切换。

验收：设备上电后能稳定显示 Dashboard，语音聊天页面不退化。

### 阶段二：协议扩展

- 新增 `notification`、`weather`、`notification_ack`。
- 增加 Go 协议单元测试。
- 设备能够接收测试通知并回执。

验收：通过测试接口向在线设备推送通知，屏幕显示并能确认。

### 阶段三：真实天气

- 复用现有天气 API 配置。
- 增加结构化天气快照。
- 增加缓存和设备下发。

验收：Dashboard 显示真实天气，失败时显示缓存和更新时间。

### 阶段四：提醒调度

- SQLite 持久化。
- 一次性和重复提醒。
- 断线补发。
- 设备确认状态。

验收：设备断线期间创建提醒，重连后能正确补发且不重复播报。

### 阶段五：Grok Bot

- Grok 作为 LLM provider 或 OpenAI 兼容接口。
- 接入提醒工具。
- 支持自然语言创建和取消提醒。
- 增加天气条件提醒。

验收：通过语音自然语言完成提醒创建、查询、修改、取消，并在设备端主动播报。

## 12. 风险与边界

- 不把 Grok/API Key 放入 ESP32 固件。
- 提醒消息需要鉴权和设备绑定，避免伪造推送。
- 服务端主动推送必须复用现有写队列，不能并发直接写 WebSocket。
- 设备离线时需要持久化待投递事件。
- TTS 播报期间收到新提醒时，要定义优先级和打断策略。
- 触摸屏 GT911 尚未接入，第一版使用 BOOT、MUTE 和语音。
- 天气供应商和 Grok 接口都需要超时、重试和限流。
- 所有时间统一使用带时区的时间戳，服务端存 UTC 或明确时区。

## 13. 第一版建议范围

第一版优先实现以下闭环：

1. ESP32 Dashboard：时钟、日期、模拟天气、设备状态。
2. 服务端 `weather` 结构化事件。
3. 服务端 `notification` 事件。
4. 本地测试提醒推送和设备确认。
5. SQLite 一次性提醒。
6. Grok `create_reminder` 和 `list_reminders` 工具。

暂缓：触摸屏、多设备管理、复杂 cron、天气雷达图、复杂动画和完整手机端管理界面。

## 14. 验收标准

- 官方和商家 LCD 配置互不影响。
- Dashboard 在 320×240 屏幕上无裁切、无镜像、文字可读。
- 时钟每秒刷新且不闪烁。
- 天气断网时显示缓存，不阻塞语音功能。
- 提醒能在屏幕显示并通过扬声器播报。
- 用户确认后服务端能收到回执。
- 设备重连后不会重复播报已确认提醒。
- Grok 能完成提醒的创建、查询和取消。
- Go 服务端和固件相关新增代码都有针对性测试。
