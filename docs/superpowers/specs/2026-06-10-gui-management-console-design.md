# GUI 管理控制台规格说明书

## 背景

`deploy-agent` 是一个 Windows 上运行的 Go 服务，用于从白名单目录执行 `.bat` 和 `.cmd` 脚本。当前已经支持：

1. HTTP API：`/health`、`/scripts`、`/run`。
2. MQTT 调度：通过命令 topic 触发脚本，并通过 reply topic 推送实时输出和最终结果。
3. 统一执行层：`internal/executor` 负责白名单校验、同名脚本锁、超时和执行结果。

这次需求是在现有服务外增加一个独立 Windows GUI 管理端，方便用户连接服务、查看脚本列表、启动脚本并实时观察输出。同时，GUI 需要支持本机管理模式，能够启动和停止与 GUI 同目录的 `deploy-agent.exe`。

## 目标

1. 新增一个独立 Windows 桌面 GUI 程序，技术栈使用 Go + Fyne。
2. GUI 支持远程连接已经运行的 `deploy-agent`。
3. GUI 支持本机模式，管理同目录的 `deploy-agent.exe` 进程。
4. GUI 可保存服务地址、用户名和密码到本地配置文件。
5. GUI 可以列出白名单脚本并手动刷新。
6. GUI 可以执行选中的脚本。
7. GUI 执行脚本时实时显示 stdout 和 stderr 输出。
8. 后端新增 HTTP 流式执行接口，供 GUI 使用。
9. 保持现有 `/health`、`/scripts`、`/run` 和 MQTT 行为不变。

## 非目标

1. 第一版不支持向脚本传参。
2. 第一版不支持中止正在运行的脚本。
3. 第一版不做任务队列。
4. 第一版 GUI 限制一次只启动一个脚本执行，避免界面状态复杂化。
5. 第一版不持久化脚本执行历史。
6. 第一版不做系统托盘。
7. 第一版不做 Windows 服务安装和卸载。
8. 第一版不使用 Windows 凭据管理器。
9. 第一版不通过 MQTT 实现 GUI 实时输出。

## 方案选择

采用“HTTP 流式接口 + Fyne 独立 GUI”的方案。

后端新增 `POST /run/stream`，仍然由 Basic Auth 保护。接口返回 NDJSON，每一行是一个 JSON 消息，用于表示输出分块或最终结果。GUI 无论处于本机模式还是远程模式，都只通过 HTTP 访问 `deploy-agent`。

不采用 MQTT 作为 GUI 的第一版实时输出通道。虽然 MQTT 已经支持实时输出，但它要求用户准备 broker，且 GUI 配置会更复杂。GUI 的主要使用场景是直接管理服务，HTTP 流式接口更直观。

不把 executor 直接嵌入 GUI。GUI 只做管理端，真正的脚本执行、白名单、锁、超时、输出编码和进程树清理由服务端继续负责，避免本机和远程形成两套执行路径。

## 后端 HTTP 流式接口

### 路由

新增接口：

```text
POST /run/stream
```

鉴权规则：

1. `/run/stream` 必须使用现有 Basic Auth。
2. `/health` 继续不鉴权。
3. `/scripts`、`/run` 继续保持现有鉴权行为。

### 请求

请求体和现有 `/run` 一致：

```json
{
  "script": "deploy.bat"
}
```

第一版不支持参数、环境变量或工作目录覆盖。

### 成功响应

响应 `Content-Type`：

```text
application/x-ndjson; charset=utf-8
```

流式输出消息：

```json
{"type":"output","script":"deploy.bat","stream":"stdout","data":"开始部署...\r\n"}
```

```json
{"type":"output","script":"deploy.bat","stream":"stderr","data":"warning...\r\n"}
```

最终消息：

```json
{"type":"final","script":"deploy.bat","exitCode":0,"timedOut":false,"startedAt":"2026-06-10T10:00:00+08:00","finishedAt":"2026-06-10T10:00:03+08:00","durationMs":3142}
```

超时最终消息：

```json
{"type":"final","script":"deploy.bat","exitCode":-1,"timedOut":true,"error":"script timed out","startedAt":"2026-06-10T10:00:00+08:00","finishedAt":"2026-06-10T10:05:00+08:00","durationMs":300000}
```

runner 启动失败但调度已经进入执行阶段时，最终消息包含稳定错误文本：

```json
{"type":"final","script":"deploy.bat","exitCode":-1,"timedOut":false,"error":"runner start failed","startedAt":"2026-06-10T10:00:00+08:00","finishedAt":"2026-06-10T10:00:00+08:00","durationMs":0}
```

### 调度前错误

调度前错误不进入 NDJSON 流，直接返回普通 JSON 错误，保持现有 HTTP API 风格：

| 状态码 | 场景 |
| --- | --- |
| 400 | JSON 解析失败或脚本名非法 |
| 401 | Basic Auth 失败 |
| 404 | 脚本不在白名单 |
| 409 | 同名脚本正在执行 |
| 500 | 其他内部错误 |

示例：

```json
{
  "error": "script not found"
}
```

### 流式状态码

一旦进入 NDJSON 流，HTTP 状态码使用 `200`。超时、脚本非零退出码和启动后的 runner 错误都通过最后一条 `type: "final"` 消息表达。

原因是 HTTP 响应头发送后无法可靠修改状态码。GUI 应以最终消息为准判断执行结果。

### 请求断开语义

第一版不支持用户从 GUI 中止脚本。为了避免用户关闭窗口或网络短暂断开时误杀脚本，`/run/stream` 不把客户端连接断开解释为取消脚本。

实现时，脚本执行上下文应使用独立于 `r.Context()` 的执行上下文，并只受 runner 超时控制。HTTP 写入失败后，服务可以停止继续写输出给该客户端，但脚本仍按正常超时和完成规则运行。此语义只适用于 `/run/stream`；现有 `/run` 行为不因本设计改变。

## GUI 程序

### 位置和产物

新增 GUI 入口建议放在：

```text
cmd/deploy-agent-gui
```

构建产物：

```text
deploy-agent-gui.exe
```

发布时建议同目录放置：

```text
deploy-agent.exe
deploy-agent-gui.exe
config.example.yaml
```

本机模式固定管理同目录的 `deploy-agent.exe`。第一版不提供手动选择服务程序路径。

### 技术栈

GUI 使用 Fyne。

选择原因：

1. 使用 Go 维护，和当前项目语言一致。
2. 适合构建轻量 Windows 管理工具。
3. 不需要把 Web 前端构建链引入仓库。
4. 第一版界面偏运维工具，Fyne 的朴素风格可接受。

### 模式

GUI 支持两种模式。

本机模式：

1. GUI 查找自身同目录的 `deploy-agent.exe`。
2. 用户可以点击“启动服务”运行该 exe。
3. 启动后 GUI 轮询 `/health`，成功后标记服务可用。
4. 用户可以点击“停止服务”停止 GUI 本次启动的进程。
5. 如果本机服务已经由其他方式运行，GUI 可以连接它，但第一版不强杀未知进程。

远程模式：

1. GUI 不管理服务进程。
2. GUI 只使用用户填写的服务地址、用户名和密码连接远程 `deploy-agent`。

两种模式都通过相同 HTTP 客户端调用 `/health`、`/scripts` 和 `/run/stream`。

### 配置保存

GUI 保存连接配置到本地文件，包含：

1. 模式：本机或远程。
2. 服务地址。
3. 用户名。
4. 密码。

密码第一版按便捷优先保存到本地配置文件。文档必须明确说明：这不是强安全存储，用户应保护配置文件权限，不应把 GUI 配置文件提交到仓库或发给他人。

推荐配置文件位置使用用户配置目录下的应用专属路径，例如：

```text
%AppData%\deploy-agent-gui\config.json
```

具体路径通过 Go 标准库获取，避免硬编码用户名。

### 主界面

界面采用实用管理面板布局。

顶部连接区：

1. 模式切换：本机、远程。
2. 服务地址输入。
3. 用户名输入。
4. 密码输入。
5. 连接按钮。
6. 保存配置按钮。
7. 本机模式下显示启动服务和停止服务按钮。
8. 显示连接状态和服务状态。

脚本列表区：

1. 连接成功后调用 `/scripts` 拉取白名单脚本。
2. 展示 `.bat` 和 `.cmd` 文件名。
3. 提供刷新按钮。
4. 选中一个脚本后，执行按钮可用。

运行状态区：

1. 当前脚本名。
2. 状态：空闲、运行中、成功、脚本失败、超时、请求失败、连接中断。
3. 开始时间。
4. 耗时。
5. 退出码。
6. 错误文本。

输出区：

1. 实时追加 stdout 和 stderr。
2. stdout 和 stderr 使用不同前缀或颜色区分。
3. 运行中自动滚动到底部。
4. 新任务开始时清空输出区。
5. 输出内容不持久化到磁盘。

最近执行记录区：

1. 只记录当前 GUI 会话。
2. 每条记录包含脚本名、状态、退出码、耗时和完成时间。
3. GUI 重启后记录清空。

### 执行限制

第一版 GUI 同一时间只允许启动一个脚本执行。执行中禁用执行按钮，避免一个界面同时管理多个流式响应。

后端仍保留现有并发语义：同名脚本不能并发，不同脚本可以并发。GUI 的限制只是第一版交互选择，不改变服务端能力。

## GUI HTTP 客户端

GUI 内部建议拆出小型 HTTP 客户端包或组件，负责：

1. 组装 Base URL。
2. 设置 Basic Auth。
3. 调用 `/health`。
4. 调用 `/scripts`。
5. 调用 `/run/stream`。
6. 逐行解析 NDJSON。
7. 将输出消息和最终消息回调给界面层。

NDJSON 解析规则：

1. 每行必须是独立 JSON。
2. `type == "output"` 时追加输出。
3. `type == "final"` 时更新最终状态并结束本次运行。
4. 遇到空行时忽略。
5. 遇到不能解析的行，标记本次运行“连接中断”或“协议错误”，并保留已经收到的输出。

## 错误处理

GUI 状态映射：

| 条件 | GUI 状态 |
| --- | --- |
| `/health` 成功 | 已连接 |
| 连接服务失败 | 未连接 |
| `/scripts` 返回 401 | 鉴权失败 |
| `/run/stream` 返回 400 | 请求错误 |
| `/run/stream` 返回 404 | 脚本不存在 |
| `/run/stream` 返回 409 | 脚本正在运行 |
| 流读取中断且未收到 final | 连接中断 |
| final `timedOut == true` | 超时 |
| final `error` 非空且未超时 | 请求失败或 runner 错误 |
| final `exitCode == 0` | 成功 |
| final `exitCode != 0` | 脚本失败 |

当脚本不存在时，GUI 应提示用户并刷新脚本列表，因为白名单可能已经重扫变化。

当连接失败时，GUI 不清空已有脚本列表，方便用户检查配置后重试。

## 构建

现有 `build.bat` 继续负责构建服务端 `deploy-agent.exe`，并保留资源清单嵌入行为。

GUI 可以采用以下两种构建方式之一：

1. 扩展 `build.bat`，同时构建 `deploy-agent.exe` 和 `deploy-agent-gui.exe`。
2. 新增 `build-gui.bat`，只构建 GUI。

推荐第一版扩展 `build.bat`，让用户一次构建得到两个 exe。README 需要说明产物和运行方式。

## 文档更新

README 需要新增 GUI 使用说明，包含：

1. GUI 是独立程序。
2. 本机模式要求 `deploy-agent-gui.exe` 与 `deploy-agent.exe` 同目录。
3. 远程模式需要填写服务地址、用户名和密码。
4. 实时输出通过 `/run/stream` 实现。
5. GUI 会把账号密码保存到本地配置文件，存在本机文件权限风险。
6. 第一版不支持中止正在运行的脚本。
7. 第一版不支持脚本参数。

HTTP API 文档需要新增 `/run/stream` 的请求、NDJSON 响应、错误码和状态判断规则。

## 测试策略

### 后端测试

新增或扩展 `internal/httpapi` 测试，覆盖：

1. `/run/stream` 需要 Basic Auth。
2. `/health` 仍然不鉴权。
3. `/scripts` 和 `/run` 行为不变。
4. `/run/stream` JSON 解析失败返回 400。
5. `/run/stream` 非法脚本名返回 400。
6. `/run/stream` 脚本不存在返回 404。
7. `/run/stream` 同名脚本忙返回 409。
8. `/run/stream` 正常执行时输出 `type: "output"` 消息。
9. `/run/stream` 正常结束时输出 `type: "final"` 消息。
10. `/run/stream` 脚本非零退出码时 final 保留退出码，不把 HTTP 状态改为错误。
11. `/run/stream` 超时时 final 包含 `timedOut: true` 和 `error: "script timed out"`。

### GUI 测试

GUI 测试重点放在界面外的逻辑：

1. 配置文件读写。
2. 服务地址规范化。
3. Basic Auth HTTP 客户端。
4. `/scripts` 响应解析。
5. NDJSON 输出消息解析。
6. NDJSON final 消息解析。
7. HTTP 错误状态映射。
8. 本机同目录 `deploy-agent.exe` 路径解析。

Fyne 界面第一版做手动验证和轻量 smoke test，不强制复杂 UI 自动化。

### 验证命令

实现完成后应运行：

```cmd
gofmt -w .
go test ./...
go vet ./...
build.bat
```

涉及 Windows 进程管理和 Fyne GUI 的行为，应优先在 Windows 环境验证。

## 实施顺序建议

1. 在 `internal/httpapi` 增加 `/run/stream` 协议类型和 handler 测试。
2. 实现 `/run/stream`，复用 `executor.RunStream`。
3. 确认现有 HTTP 和 MQTT 测试不回归。
4. 新增 GUI 配置读写逻辑和测试。
5. 新增 GUI HTTP 客户端和 NDJSON 解析测试。
6. 新增 Fyne GUI 入口和界面布局。
7. 接入本机同目录服务启动和停止。
8. 更新构建脚本。
9. 更新 README 和示例。
10. 运行验证命令并在 Windows 上做手动 GUI 验证。
