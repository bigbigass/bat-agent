# MQTT 脚本调度规格说明书

## 背景

`deploy-agent` 当前通过 HTTP API 调度 Windows `.bat` 和 `.cmd` 脚本。`POST /run` 会同步等待脚本执行结束，然后返回完整的 `stdout`、`stderr`、退出码、开始时间、结束时间和耗时。

本次需求是在不破坏现有 HTTP 行为的前提下，新增 MQTT 调度能力。另一个程序会通过 MQTT 下发脚本执行命令，并订阅执行过程中的实时输出，用于展示脚本执行进度。

## 目标

1. 支持通过 MQTT 触发白名单脚本执行。
2. MQTT 请求使用固定命令 topic，命令 JSON 中携带 `requestId`、`script` 和 `replyTo`。
3. MQTT 响应通过请求中的 `replyTo` topic 返回。
4. 脚本执行过程中实时推送 `stdout` 和 `stderr` 分块消息。
5. 脚本结束、超时或调度失败时，发送一条 `done: true` 的最终消息。
6. HTTP 和 MQTT 共用同一套脚本校验、白名单、同名脚本锁、超时和执行规则。
7. 默认保持当前 HTTP 行为不变，MQTT 需要通过配置显式开启。

## 非目标

1. 不做任务队列。同名脚本正在执行时，新请求直接失败。
2. 不做任务历史存储。
3. 不新增 HTTP 实时进度接口。
4. 不支持向脚本传参。
5. 不在 MQTT payload 中增加应用层 token。鉴权依赖 MQTT broker 的用户名、密码、TLS 和 ACL。
6. 不改变现有 HTTP API 的响应结构和状态码语义。

## 配置

新增 `services` 配置段，用于控制 HTTP 和 MQTT 是否启用。

```yaml
services:
  http:
    enabled: true
  mqtt:
    enabled: false
    broker: "tcp://127.0.0.1:1883"
    clientId: "deploy-agent"
    username: ""
    password: ""
    commandTopic: "deploy-agent/run"
    qos: 1
```

配置规则：

1. `services.http.enabled` 默认值为 `true`。
2. `services.mqtt.enabled` 默认值为 `false`。
3. 至少启用 HTTP 或 MQTT 中的一种；两者都为 `false` 时配置校验失败。
4. `services.mqtt.broker` 支持 `tcp://` 和 `ssl://`。
5. `services.mqtt.username` 和 `services.mqtt.password` 为空时，使用匿名连接。
6. `services.mqtt.commandTopic` 为空时配置校验失败。
7. `services.mqtt.qos` 允许 `0`、`1`、`2`，默认推荐 `1`。

`auth` 配置继续只用于 HTTP Basic Auth。MQTT 鉴权由 broker 负责。

## HTTP 协议

HTTP 协议保持现状，仍然是同步最终结果模式。

### POST /run

请求：

```json
{
  "script": "deploy.bat"
}
```

成功响应，HTTP `200`：

```json
{
  "script": "deploy.bat",
  "exitCode": 0,
  "stdout": "完整 stdout，最多 1MiB",
  "stderr": "完整 stderr，最多 1MiB",
  "startedAt": "2026-06-10T10:00:00+08:00",
  "finishedAt": "2026-06-10T10:00:03+08:00",
  "durationMs": 3142
}
```

超时响应，HTTP `504`：

```json
{
  "script": "deploy.bat",
  "exitCode": -1,
  "stdout": "已捕获 stdout，最多 1MiB",
  "stderr": "已捕获 stderr，最多 1MiB",
  "startedAt": "2026-06-10T10:00:00+08:00",
  "finishedAt": "2026-06-10T10:05:00+08:00",
  "durationMs": 300000,
  "timedOut": true
}
```

HTTP 错误响应继续使用当前结构：

```json
{
  "error": "script not found"
}
```

HTTP 状态码语义：

| 状态码 | 场景 |
| --- | --- |
| 400 | JSON 解析失败，或脚本名非法 |
| 401 | Basic Auth 失败 |
| 404 | 脚本不在白名单 |
| 409 | 同名脚本正在执行 |
| 500 | 启动进程失败或内部错误 |
| 504 | 执行超时 |

脚本自身返回非零退出码时，HTTP 仍返回 `200`，调用方通过 `exitCode` 判断脚本是否成功。

## MQTT 协议

MQTT 发布规则：

1. 命令订阅和结果发布使用 `services.mqtt.qos`。
2. 输出消息和最终消息都不设置 retained，避免显示端重连后收到旧任务的历史消息。
3. 每个请求的进度和最终结果都只发布到该请求 payload 中的 `replyTo`。

### 命令 Topic

服务订阅一个固定命令 topic。默认值：

```text
deploy-agent/run
```

### 命令 Payload

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "replyTo": "deploy-agent/replies/abc-123"
}
```

字段说明：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `requestId` | 是 | 调用方生成的请求 ID，用于关联一次执行 |
| `script` | 是 | 要执行的白名单脚本文件名，只允许 `.bat` 或 `.cmd` 文件名 |
| `replyTo` | 是 | `deploy-agent` 发布实时输出和最终结果的 MQTT topic |

命令 payload 不支持脚本参数，不支持 token。

### 实时输出消息

脚本执行过程中，服务把 `stdout` 和 `stderr` 分块发布到 `replyTo`。

stdout 消息：

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "stream": "stdout",
  "data": "正在部署第 1 步...\r\n",
  "done": false
}
```

stderr 消息：

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "stream": "stderr",
  "data": "warning: something...\r\n",
  "done": false
}
```

字段说明：

| 字段 | 说明 |
| --- | --- |
| `requestId` | 原样返回请求中的 `requestId` |
| `script` | 原样返回请求中的 `script` |
| `stream` | `stdout` 或 `stderr` |
| `data` | 当前输出分块内容 |
| `done` | 实时输出消息固定为 `false` |

输出编码规则沿用当前 runner 行为：优先按 UTF-8 透传；如果不是合法 UTF-8，则按 GBK 解码，适配简体中文 Windows 控制台。

### 最终完成消息

脚本执行结束后，服务发布一条最终消息到 `replyTo`。

成功：

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "exitCode": 0,
  "timedOut": false,
  "startedAt": "2026-06-10T10:00:00+08:00",
  "finishedAt": "2026-06-10T10:00:03+08:00",
  "durationMs": 3142,
  "done": true
}
```

脚本执行完成但退出码非零：

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "exitCode": 2,
  "timedOut": false,
  "startedAt": "2026-06-10T10:00:00+08:00",
  "finishedAt": "2026-06-10T10:00:03+08:00",
  "durationMs": 3142,
  "done": true
}
```

超时：

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "exitCode": -1,
  "timedOut": true,
  "error": "script timed out",
  "startedAt": "2026-06-10T10:00:00+08:00",
  "finishedAt": "2026-06-10T10:05:00+08:00",
  "durationMs": 300000,
  "done": true
}
```

调度失败：

```json
{
  "requestId": "abc-123",
  "script": "missing.bat",
  "error": "script not found",
  "done": true
}
```

最终消息字段说明：

| 字段 | 说明 |
| --- | --- |
| `requestId` | 能解析到请求 ID 时原样返回 |
| `script` | 能解析到脚本名时原样返回 |
| `exitCode` | 脚本退出码；调度失败时可省略；超时时为 `-1` |
| `timedOut` | 是否超时；调度失败时可省略 |
| `error` | 调度失败、runner 错误或超时时的错误描述；脚本退出码非零时不设置该字段 |
| `startedAt` | 脚本进程启动时间；进程未启动时可省略 |
| `finishedAt` | 脚本进程结束时间；进程未启动时可省略 |
| `durationMs` | 脚本执行耗时；进程未启动时可省略 |
| `done` | 最终消息固定为 `true` |

### MQTT 显示端状态判断

显示端按以下规则判断状态：

| 条件 | 状态 |
| --- | --- |
| `done == false` | 运行中，追加 `data` 到对应输出窗口 |
| `done == true` 且 `timedOut == true` | 超时 |
| `done == true` 且 `error` 非空 | 调度失败或 runner 错误 |
| `done == true` 且 `exitCode == 0` | 执行成功 |
| `done == true` 且 `exitCode != 0` | 脚本执行完成但脚本自身失败 |

如果 `done == true` 且同时存在 `error` 和 `timedOut == true`，显示端应优先展示超时状态，并显示 `error` 文本。

### MQTT 错误处理

服务收到 MQTT 命令后：

1. 如果 payload 是合法 JSON，且能解析出 `replyTo`，则所有调度错误都尽量发布到 `replyTo`，并设置 `done: true`。
2. 如果 payload 不是合法 JSON，且无法可靠解析 `replyTo`，只记录服务日志，不发布响应。
3. 如果缺少 `requestId`，但有 `replyTo`，发布 `done: true` 错误消息，`error` 为 `missing requestId`。
4. 如果缺少 `replyTo`，只记录服务日志，不发布响应。
5. 如果脚本名非法，发布 `done: true` 错误消息，`error` 为 `invalid script name`。
6. 如果脚本不存在，发布 `done: true` 错误消息，`error` 为 `script not found`。
7. 如果同名脚本正在执行，发布 `done: true` 错误消息，`error` 为 `script is already running`。

稳定错误文本：

```text
invalid JSON body
missing requestId
missing replyTo
invalid script name
script not found
script is already running
runner start failed
script timed out
```

`invalid JSON body` 主要用于服务日志；只有在实现能够可靠确定 `replyTo` 的情况下，才会通过 MQTT 返回。

## 执行并发语义

HTTP 和 MQTT 共用同一套并发规则：

1. 同一个脚本不能并发执行。
2. 不同脚本可以同时执行。
3. registry 重扫时尽量保留已有 `Entry` 指针，保证进行中的锁不丢失。
4. MQTT 不做排队；同名脚本忙时直接返回最终错误消息。

## 内部架构

新增统一执行层：

```text
internal/executor
```

职责：

1. 校验脚本名。
2. 查询 registry 白名单。
3. 获取同名脚本锁。
4. 调用 runner 执行脚本。
5. 处理超时和 runner 错误。
6. 生成统一执行结果。
7. 为 HTTP 提供收集完整输出的调用方式。
8. 为 MQTT 提供实时输出回调的调用方式。

HTTP 调用路径：

```text
HTTP /run
  -> executor.RunCollect(...)
  -> runner.RunStream(...)
  -> 收集 stdout/stderr
  -> 返回现有 HTTP JSON
```

MQTT 调用路径：

```text
MQTT commandTopic
  -> internal/mqttapi 解析命令
  -> executor.RunStream(...)
  -> stdout/stderr 回调发布 done=false 消息
  -> 执行结束后发布 done=true 消息
```

新增 MQTT 包：

```text
internal/mqttapi
```

职责：

1. 根据配置连接 MQTT broker。
2. 订阅 `commandTopic`。
3. 解析命令 JSON。
4. 调用 executor 执行脚本。
5. 发布实时输出消息。
6. 发布最终完成或错误消息。
7. 在服务关闭时断开 MQTT 连接。

改造 runner：

1. 保留输出大小上限，每个 stream 最多保留 1MiB 用于最终结果。
2. 增加实时输出回调能力。
3. 保留 UTF-8 透传和 GBK fallback 解码。
4. 保留超时后使用 `taskkill /F /T /PID` 清理进程树。

`main.go` 启动流程：

```text
加载配置
创建 registry
启动 registry 定时重扫
创建 executor
如果 services.http.enabled=true，启动 HTTP server
如果 services.mqtt.enabled=true，启动 MQTT client
收到退出信号后关闭 HTTP server 和 MQTT client
```

## 依赖选择

MQTT 客户端采用：

```text
github.com/eclipse/paho.mqtt.golang
```

选择原因：

1. 支持 MQTT over TCP 和 TLS。
2. 支持 username/password。
3. 支持 QoS。
4. 支持自动重连。
5. 适合当前服务作为 MQTT client 订阅命令 topic 的模式。

## 兼容性

1. 现有 `config.yaml` 在没有 `services` 配置段时继续可用。
2. 现有 HTTP API 行为不变。
3. `/health` 继续不鉴权。
4. `/scripts` 和 `/run` 继续使用 Basic Auth。
5. `runner.timeoutSeconds`、`runner.scriptDir` 语义不变。
6. `deploy-agent.exe`、`resource.syso` 和本地 `config.yaml` 继续不提交。

## 测试策略

### 配置测试

覆盖：

1. 没有 `services` 配置段时，默认 HTTP 开启、MQTT 关闭。
2. HTTP 和 MQTT 都关闭时，配置校验失败。
3. MQTT 开启但 `commandTopic` 为空时，配置校验失败。
4. MQTT `qos` 不在 `0`、`1`、`2` 范围内时，配置校验失败。
5. MQTT broker 使用 `tcp://` 和 `ssl://` 时配置通过。

### executor 测试

覆盖：

1. 脚本名非法时返回 `invalid script name`。
2. 脚本不存在时返回 `script not found`。
3. 同名脚本忙时返回 `script is already running`。
4. 不同脚本可以并发执行。
5. `RunCollect` 能收集 stdout/stderr 并返回最终结果。
6. `RunStream` 能通过回调输出 stdout/stderr 分块，并返回最终结果。

### MQTT 协议测试

使用 fake publisher 或接口替身测试，不依赖真实 broker。

覆盖：

1. 合法命令触发 executor，并向 `replyTo` 发布输出消息和最终消息。
2. 输出消息 `done` 为 `false`。
3. 最终消息 `done` 为 `true`。
4. 缺少 `requestId` 时发布 `missing requestId`。
5. 缺少 `replyTo` 时只记录错误，不发布消息。
6. 脚本不存在时发布 `script not found`。
7. 同名脚本忙时发布 `script is already running`。

### HTTP 回归测试

覆盖：

1. `/health` 不鉴权。
2. `/scripts` 仍需 Basic Auth。
3. `/run` 仍需 Basic Auth。
4. `/run` 成功时响应结构与现有协议一致。
5. `/run` 脚本退出码非零时 HTTP 仍返回 `200`。
6. `/run` 超时时返回 `504` 且 `timedOut: true`。

## 实施顺序建议

1. 扩展配置结构和配置校验。
2. 新增 executor，并让 HTTP `/run` 改为调用 executor，保持 HTTP 响应不变。
3. 改造 runner，支持实时输出回调，同时保留完整输出收集。
4. 新增 MQTT 配置和 `internal/mqttapi`。
5. 在 `main.go` 中按配置启动 HTTP 和 MQTT。
6. 更新 `config.example.yaml` 和 README。
7. 运行 `gofmt -w .`、`go test ./...`、`go vet ./...` 和 `build.bat`。
