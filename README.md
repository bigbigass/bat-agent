# deploy-agent

一个跑在 Windows 上的 Go 管理工具。日常使用启动 `deploy-agent-gui.exe`：它既是桌面 GUI，也是后台 HTTP/MQTT 服务，会从配置目录扫描白名单 `.bat` / `.cmd` 脚本并以管理员权限执行。HTTP API 仍然保留，便于 curl、远程调用和集成系统触发脚本。

## 构建

需要 Go 1.19+。

```cmd
build.bat
```

产物：`deploy-agent-gui.exe` 和兼容用的 `deploy-agent.exe`。日常使用优先启动 `deploy-agent-gui.exe`，它会在 GUI 进程内启动服务；`deploy-agent.exe` 只是无界面的后台入口。二者都内嵌 UAC 清单，双击会弹管理员确认框。

GUI 使用 Fyne 构建，需要启用 CGO，并且 PATH 中有可用的 C 编译器（如 gcc）。

## 配置

首次运行前拷贝一份 `config.example.yaml` 为 `config.yaml`，至少改掉密码：

```yaml
server:
  host: 0.0.0.0
  port: 8080
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
auth:
  username: admin
  password: change-me-please        # 长度 ≥ 8
runner:
  timeoutSeconds: 300               # 单脚本超时
  scriptDir: ""                     # 留空 = exe 所在目录
preRun:
  download:
    script: ""                          # 留空 = 自动使用 tools/download_simple.bat（如存在）
    timeoutSeconds: 300                 # 仅前置下载超时
```

`config.yaml` 优先从 exe 同目录读，没有则从当前工作目录读。

默认只启用 HTTP；MQTT 需要把 `services.mqtt.enabled` 显式设为 `true` 才会连接 broker 并订阅命令 topic。

### HTTP / MQTT 开关

- `services.http.enabled` 控制 HTTP 服务，默认 `true`。
- `services.mqtt.enabled` 控制 MQTT 调度，默认 `false`。
- 至少需要启用 HTTP 或 MQTT 中的一种。
- MQTT `broker` 支持 `tcp://` 和 `ssl://`。
- MQTT 鉴权依赖 broker 的账号、密码、TLS 和 ACL；`auth` 里的 Basic Auth 只用于 HTTP。

### GUI 执行前下载

GUI 会扫描脚本目录中的 `.bat` / `.cmd` 文件。脚本数量不需要写进 `config.yaml`；刷新脚本或等待自动重扫后即可在下拉框中选择。

GUI 勾选“执行前下载”后，需要填写项目编号和产物文件名。点击“执行脚本”时，服务端会先执行下载脚本：

```cmd
tools\download_simple.bat <项目编号> <产物文件名>
```

下载成功后，服务端才会执行当前下拉框选中的脚本，并把同样的两个值作为普通脚本参数传入：

```cmd
selected_script.bat <项目编号> <产物文件名>
```

也就是批处理里的 `%1` 是项目编号，`%2` 是产物文件名。下载失败或超时时不会执行目标脚本。

下载脚本默认使用 `config.yaml` 同目录下的 `tools\download_simple.bat`；如果 `preRun.download.script` 显式配置了路径，则使用配置路径覆盖默认值。

### 下载脚本

示例下载脚本按固定远端路径获取产物：

```text
/交付产物/<项目编号>/<产物文件名>
```

如果 GUI 或 HTTP 客户端请求前置下载，但默认下载脚本不存在且未显式配置 `preRun.download.script`，服务端会返回 `pre-run download is not configured`。`preRun.download.timeoutSeconds` 只作用于下载步骤，不影响目标脚本的 `runner.timeoutSeconds`。

`tools/download_simple.bat` 不是程序内置文件。部署时需要在 `config.yaml` 所在目录下自行提供该脚本，以及脚本依赖的工具和凭据（例如 `BaiduPCS-Go.exe`、`tools/cookie.ini`）。`preRun.download.script` 相对路径基于 `config.yaml` 所在目录解析。`tools/cookie.ini` 是本机凭据文件，不要提交或分享。

### 发布下载中心

下载中心是可选能力。启用后，服务端定期从稳定的 HTTPS 地址读取发布清单，GUI 只传递版本和资源 ID；制品始终下载并保存在服务端，远程客户端不会收到文件副本：

```yaml
release:
  enabled: true
  manifestURL: "https://release.example.com/deploy-agent/manifest.json"
  downloadDir: "tools/download"
  refreshSeconds: 300
```

`downloadDir` 是相对 `config.yaml` 所在目录解析的目录。正式文件落在 `<downloadDir>/<version>/<resourceName>`，最新版本还会更新 `<downloadDir>/latest/<resourceName>`。服务端先写 `.part` 临时文件，下载结束后严格校验清单中的字节数和 SHA-256，再原子发布文件。清单刷新失败不会使服务停止，内存中的上一份有效清单会继续提供给客户端；首次启动没有有效清单时下载中心显示不可用。

清单必须使用 `schemaVersion: 1`，每个资源的 `kind` 为 `bundle` 或 `component`，并提供唯一的 `id`、纯文件名 `name`、HTTPS 签名 `url`、正数 `size` 和 64 位十六进制 `sha256`。签名资源地址不会通过 API 返回给 GUI。

## 运行

双击 `deploy-agent.exe`（会弹 UAC），或在管理员 cmd 里直接跑：

```cmd
deploy-agent.exe
```

启动日志会打印扫描到的 bat 列表。

## HTTP API

所有除 `/health` 之外的端点都要 Basic Auth。

HTTP 是同步最终结果模式：`POST /run` 会等脚本执行结束后一次性返回最终结果。脚本自身 `exitCode` 非 0 时，HTTP 仍返回 200，调用方需要读取响应里的 `exitCode` 判断脚本是否成功。

### `GET /health`
不鉴权。
```
{"status":"ok"}
```

### `GET /scripts`
列出白名单里的 bat（启动时扫描 + 每分钟重扫）。
```
{"scripts":["deploy.bat","restart.bat"]}
```

### 发布与下载接口

以下接口都需要 Basic Auth；清单和任务只保存在服务端内存中，服务重启后任务记录不恢复。

```text
GET  /releases
POST /releases/refresh
POST /downloads
GET  /downloads/{taskId}
POST /downloads/{taskId}/cancel
```

`GET /releases` 返回最新版本和历史版本的资源元数据，不包含签名 URL；GUI 会按发布时间倒序展示这些版本。`POST /releases/refresh` 从配置的 `manifestURL` 拉取新清单并返回当前清单。启动下载时提交：

```json
{"version":"2026.08.19","resourceId":"api"}
```

成功返回 `202` 和任务状态。单个服务实例同一时间只允许一个活动下载，冲突返回 `409`。任务状态的 `state` 为 `queued`、`downloading`、`verifying`、`completed`、`failed` 或 `cancelled`，并包含 `bytesDone`、`totalBytes`、`percent`、`speedBytesPerSecond`、服务端 `destination` 和错误信息。取消会终止服务端请求并清理 `.part` 文件；失败后重新提交会创建新的任务 ID。

### `POST /run`
请求：
```json
{"script": "deploy.bat"}
```

也可以把参数传给当前选中的脚本：

```json
{
  "script": "deploy.bat",
  "args": ["ProjectA", "app.zip"]
}
```

脚本会收到 `%1=ProjectA`、`%2=app.zip`。参数不能包含双引号或换行；非法参数返回 400 和 `invalid script argument`。

也可以请求执行前下载；如果同时提供 `args`，下载成功后目标脚本会收到这些参数：

```json
{
  "script": "deploy.bat",
  "args": ["ProjectA", "app.zip"],
  "preDownload": {
    "enabled": true,
    "project": "ProjectA",
    "artifact": "app.zip"
  }
}
```

响应（200）：
```json
{
  "script": "deploy.bat",
  "exitCode": 0,
  "stdout": "...",
  "stderr": "...",
  "startedAt": "2026-04-20T10:00:00+08:00",
  "finishedAt": "2026-04-20T10:00:03+08:00",
  "durationMs": 3142
}
```

错误码：

| 码 | 场景 |
|---|---|
| 400 | JSON 解析失败 / script 非法（含路径分隔符）/ args 非法 |
| 401 | Basic Auth 失败 |
| 404 | 脚本不在白名单 |
| 409 | 同名脚本正在执行（同名串行） |
| 500 | 启动进程失败 |
| 504 | 执行超时，返回体 `timedOut: true` |

前置下载相关错误：

- `preDownload` 参数非法时返回 400，错误文本为 `invalid pre-run download request`。
- 请求前置下载但服务端未配置脚本时返回 `pre-run download is not configured`，不会执行目标脚本。
- 下载超时时返回超时结果，响应体 `timedOut: true`，`/run` 返回 504。
- 下载失败时返回 `pre-run download failed`，不会执行目标脚本。

### `POST /run/stream`

需要 Basic Auth。请求体与 `/run` 一致：

```json
{"script":"deploy.bat"}
```

也可以把参数传给当前选中的脚本：

```json
{
  "script": "deploy.bat",
  "args": ["ProjectA", "app.zip"]
}
```

也可以请求执行前下载；如果同时提供 `args`，下载成功后目标脚本会收到这些参数：

```json
{
  "script": "deploy.bat",
  "args": ["ProjectA", "app.zip"],
  "preDownload": {
    "enabled": true,
    "project": "ProjectA",
    "artifact": "app.zip"
  }
}
```

响应是 NDJSON，每一行一个 JSON：

```json
{"type":"output","script":"deploy.bat","stream":"stdout","data":"开始部署...\r\n"}
{"type":"output","script":"deploy.bat","stream":"stderr","data":"warning...\r\n"}
{"type":"final","script":"deploy.bat","exitCode":0,"timedOut":false,"startedAt":"2026-06-10T10:00:00+08:00","finishedAt":"2026-06-10T10:00:03+08:00","durationMs":3142}
```

一旦进入流式响应，HTTP 状态码保持 `200`。脚本超时、非零退出码或 runner 错误通过最后一条 `type: "final"` 判断。调度前错误仍返回普通 JSON 错误和对应 HTTP 状态码。

前置下载参数非法时，调度前返回 400 和 `invalid pre-run download request`。请求前置下载但服务端未配置脚本时，最终消息会返回 `pre-run download is not configured`，不会执行目标脚本。下载超时会在最终 NDJSON 消息里同时返回 `timedOut: true` 和 `error: "pre-run download timed out"`；下载失败会在最终消息里返回 `pre-run download failed`，不会执行目标脚本。

## MQTT API

MQTT 调度需要显式开启 `services.mqtt.enabled`。HTTP 和 MQTT 共用同一套脚本白名单、脚本名校验、同名脚本锁和超时规则。

显示程序应先订阅命令 payload 里的 `replyTo`，再向命令 topic 发布命令。输出消息和最终消息都不会设置 retained；如果脚本很快完成，发布命令后才订阅 `replyTo` 可能会错过实时输出或最终消息。

### 命令 Topic

服务订阅固定命令 topic，默认值：

```text
deploy-agent/run
```

可通过 `services.mqtt.commandTopic` 修改。

### 命令 Payload

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "replyTo": "deploy-agent/replies/abc-123"
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `requestId` | 是 | 调用方生成的请求 ID，用于关联一次执行 |
| `script` | 是 | 要执行的白名单脚本文件名 |
| `replyTo` | 是 | deploy-agent 发布实时输出和最终结果的 MQTT topic |

### 实时输出消息

脚本运行过程中，服务会把 `stdout` / `stderr` 分块发布到 `replyTo`：

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "stream": "stdout",
  "data": "正在部署第 1 步...\r\n",
  "done": false
}
```

实时输出消息字段：

| 字段 | 说明 |
|---|---|
| `requestId` | 原样返回命令中的 `requestId` |
| `script` | 原样返回命令中的 `script` |
| `stream` | `stdout` 或 `stderr` |
| `data` | 当前输出分块内容 |
| `done` | 固定为 `false` |

### 最终消息

脚本完成、失败、超时或调度错误时，服务会向 `replyTo` 发布一条最终消息：

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

最终消息字段：

| 字段 | 说明 |
|---|---|
| `requestId` | 能解析到请求 ID 时原样返回 |
| `script` | 能解析到脚本名时原样返回 |
| `exitCode` | 脚本退出码；调度失败时可省略；超时时为 `-1` |
| `timedOut` | 是否超时；调度失败时可省略 |
| `error` | 调度失败、runner 错误或超时时的错误描述；脚本退出码非零时不设置 |
| `startedAt` | 脚本进程启动时间；进程未启动时可省略 |
| `finishedAt` | 脚本进程结束时间；进程未启动时可省略 |
| `durationMs` | 脚本执行耗时；进程未启动时可省略 |
| `done` | 固定为 `true` |

`done=false` 表示中间输出；`done=true` 表示最终完成、失败、超时或调度错误。

### 显示端状态判断

另一个显示程序可以按下面的优先级判断状态：

| 条件 | 状态 |
|---|---|
| `done == false` | 运行中，追加 `data` 到对应输出窗口 |
| `done == true && timedOut == true` | 超时 |
| `done == true && error` 非空 | 调度失败或 runner 错误 |
| `done == true && exitCode == 0` | 成功 |
| `done == true && exitCode != 0` | 脚本自身失败 |

如果 `timedOut` 和 `error` 同时存在，优先展示超时，并同时显示 `error` 文本。

### MQTT 错误处理

- payload 是合法 JSON 且有 `replyTo` 时，服务会尽量发布 `done=true` 的错误消息。
- 缺少 `replyTo` 或无法可靠解析 `replyTo` 时，只记录服务日志，不发布 MQTT 响应。
- `invalid JSON body` 表示命令 payload 不是合法 JSON，此时无法可靠解析 `replyTo`，因此通常只作为稳定日志错误文本出现，不会作为 MQTT 响应发出。
- 稳定错误文本包括：

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

## 使用示例

```bash
# 列出脚本
curl -u admin:change-me-please http://localhost:8080/scripts

# 触发执行
curl -u admin:change-me-please -X POST http://localhost:8080/run \
     -H "Content-Type: application/json" \
     -d "{\"script\":\"deploy.bat\"}"
```

## 验证管理员权限

放一个 `whoami.bat`：
```bat
@echo off
whoami /groups | findstr "S-1-16-12288"
```
触发执行后 stdout 里能看到 `S-1-16-12288`（High Mandatory Level），说明脚本是以管理员身份跑的。

## GUI 管理端

`deploy-agent-gui.exe` 是主要入口：它既是 Windows 桌面程序，也是 `deploy-agent` 后台服务。

本机模式：

- 直接启动 `deploy-agent-gui.exe`。
- GUI 本机模式需要管理员权限；`build.bat` 生成的 GUI 已内嵌 `requireAdministrator` 清单，启动时会弹 UAC。
- GUI 会在同一进程内启动 HTTP/MQTT 服务，不再要求同目录存在 `deploy-agent.exe`。
- 本机模式不需要在界面里输入服务地址、用户名或密码；GUI 会使用 `config.yaml` 中的 `server` 和 `auth` 配置连接内嵌服务。
- 关闭窗口只会隐藏到系统托盘，服务继续运行。
- 从托盘菜单选择“打开”可恢复窗口，选择“退出”才会停止内嵌服务并结束进程。

远程模式：

- 填写远程服务地址、HTTP Basic Auth 用户名和密码。
- GUI 通过 `/scripts` 列出脚本，通过 `/run/stream` 执行脚本并实时显示输出。
- “下载中心”按清单展示最新完整包和历史版本组件，支持进度、速度、取消、失败重试和 SHA-256 校验结果；远程模式会明确标注文件仍保存在服务端。
- 远程模式不会管理本机内嵌服务以外的任何远程进程。

GUI 会把远程模式的服务地址、用户名和密码保存到本地配置文件，默认在用户配置目录下的 `deploy-agent-gui/config.json`。本机模式不会保存本机服务账号密码；本机认证来自 `config.yaml`。远程密码是便捷存储，不是强安全存储，请保护该文件权限，不要提交或分享它。

第一版限制：

- 不支持中止正在运行的脚本。
- 不持久化执行历史。
- 不提供 Windows Service 注册。

## 安全说明

- 只允许调用 **配置的脚本目录** 下白名单内的 `.bat` / `.cmd`；扫描不递归子目录，请求脚本名会拒绝路径分隔符和 `..`。
- 同一脚本同时只能跑一份；并发请求第二个返回 409。
- 默认没启 TLS。要暴露到外网请在前面加反向代理（Caddy/Nginx 等）或自己加 TLS。
- Basic Auth 明文传输，强烈建议配合 HTTPS。

## 非目标

目前不做：脚本执行任务持久化与中止 / Windows 服务注册 / 日志轮转。发布下载任务仅保存在内存中，服务重启后不恢复。
