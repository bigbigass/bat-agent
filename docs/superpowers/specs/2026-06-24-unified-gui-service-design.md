# GUI 与服务合并规格说明书

## 背景

当前项目有两个可执行程序：

1. `deploy-agent.exe`：后台服务入口，负责加载 `config.yaml`、扫描脚本、启动 HTTP/MQTT、执行脚本。
2. `deploy-agent-gui.exe`：Fyne 桌面管理端，负责连接服务、显示脚本列表、触发执行和展示输出。

GUI 的本机模式会启动同目录下的 `deploy-agent.exe`，然后继续通过 HTTP 调用 `/health`、`/scripts` 和 `/run/stream`。这保证了 GUI 和远程管理走同一套协议，但用户必须同时理解两个程序和两个进程。

新的目标是让 `deploy-agent-gui.exe` 自身就是服务：用户只启动一个 GUI 程序，它在同一进程里启动后台能力，并在窗口关闭后继续挂在系统托盘。

## 目标

1. `deploy-agent-gui.exe` 同时承担桌面 GUI 和后台服务职责。
2. 打开 GUI 时自动启动内嵌服务，不再启动外部 `deploy-agent.exe`。
3. 内嵌服务复用现有服务逻辑：配置加载、脚本 registry、executor、HTTP、MQTT、超时、锁和输出处理都保持一致。
4. HTTP API 保持可用，现有 `/health`、`/scripts`、`/run`、`/run/stream` 行为不变。
5. GUI 仍通过 HTTP 客户端访问本机服务，避免形成另一套执行路径。
6. 关闭主窗口时默认隐藏到系统托盘，服务继续运行。
7. 托盘菜单提供打开窗口和退出程序。
8. 退出程序时优雅停止 HTTP server、MQTT client、registry watcher 和相关后台 goroutine。
9. README 和构建说明更新为单程序使用方式。

## 非目标

1. 不把程序注册为 Windows Service。
2. 不新增开机自启动。
3. 不新增多实例协调或端口抢占。
4. 不改变脚本白名单规则、路径穿越保护、同名脚本锁或输出大小限制。
5. 不改变远程 HTTP API 的认证规则。
6. 不在本次工作中重设计 GUI 界面。
7. 不新增脚本执行中止能力。

## 方案选择

采用“服务核心抽包 + GUI 内嵌启动”的方案。

把当前根目录 `main.go` 中的服务启动流程抽到 `internal/appservice`。这个包暴露一个小型生命周期 API，负责从配置启动完整后台能力。根目录纯服务入口和 GUI 入口都可以调用它，但 GUI 不再通过 `internal/gui/localservice` 启动另一个进程。

GUI 启动后：

1. 加载服务端 `config.yaml`。
2. 在同一进程中启动 `appservice.Service`。
3. 使用配置中的 HTTP 地址构造本机 Base URL。
4. 调用现有 `apiclient` 连接 `/health` 和 `/scripts`。
5. 主窗口关闭时隐藏窗口，不停止服务。
6. 用户从托盘选择“退出”时停止服务并退出进程。

保留 HTTP API 是刻意选择。即使服务内嵌到 GUI，HTTP 仍然是远程调用、curl 调试、GUI 实时输出和未来集成的稳定控制面。这样既满足“只开一个东西”，又避免 GUI 直接调用 executor 造成两套行为。

## 架构

### 服务生命周期包

新增包：

```text
internal/appservice
```

职责：

1. 查找并加载 `config.yaml`。
2. 解析脚本目录。
3. 创建 `registry.Registry` 并启动定时重扫。
4. 创建 `executor.Executor`，包括 pre-run download 配置。
5. 按配置启动 HTTP server。
6. 按配置启动 MQTT client。
7. 暴露当前 HTTP 地址，供 GUI 连接。
8. 提供 `Shutdown(ctx)` 做优雅停止。

核心类型：

```go
type Service struct {
    // 内部保存 config、registry、executor、http server、mqtt client 等状态。
}

type Options struct {
    ConfigPath string
}

func New(options Options) *Service
func (s *Service) Start(ctx context.Context) error
func (s *Service) Shutdown(ctx context.Context) error
func (s *Service) HTTPBaseURL() string
```

`Start` 应只负责启动一次。重复启动应返回稳定错误，或在 GUI 层禁止重复调用。

### 纯服务入口

根目录 `main.go` 变薄：

1. 设置日志。
2. 创建 `appservice.Service`。
3. 调用 `Start`。
4. 等待系统信号。
5. 调用 `Shutdown`。

这样保留纯后台入口的兼容性，但它不再拥有独立的一套启动逻辑。

### GUI 入口

`cmd/deploy-agent-gui` 改为内嵌服务入口：

1. 创建 Fyne app 和主窗口。
2. 启动 `appservice.Service`。
3. 服务启动成功后自动连接本机 HTTP 地址。
4. 删除“启动服务”和“停止服务”按钮，改为显示当前内嵌服务状态。
5. 远程模式可以保留，用于连接其他机器的 `deploy-agent`；本机模式固定连接当前进程内嵌服务。
6. 原 `internal/gui/localservice` 不再用于主流程，可删除或废弃。

GUI 不直接调用 executor。脚本列表、执行和流式输出继续通过 `internal/gui/apiclient` 调 HTTP API。

## 托盘行为

Fyne 支持桌面系统托盘能力，优先使用 Fyne 原生桌面 API 实现，避免额外引入托盘库。

目标行为：

1. 程序启动时显示主窗口。
2. 点击窗口关闭按钮时隐藏窗口，进程继续运行。
3. 托盘菜单包含“打开”和“退出”。
4. 点击“打开”显示并聚焦主窗口。
5. 点击“退出”触发服务 `Shutdown`，然后退出 Fyne app。

需要防止关闭窗口直接结束进程。实现上应使用窗口 close intercept，并由托盘“退出”路径设置显式退出标记。

## 配置

继续使用服务端 `config.yaml` 作为后台服务配置来源。GUI 自己的连接配置文件仍可保存远程连接信息和界面偏好。

本机模式下，GUI 不需要用户输入服务地址。服务地址来自内嵌服务实际监听地址：

```text
http://<server.host>:<server.port>
```

如果 `server.host` 是空值或通配地址，本机 GUI 应使用 `127.0.0.1` 作为连接主机。端口仍来自配置。

认证仍使用 `config.yaml` 中的 Basic Auth 用户名和密码。GUI 内嵌服务时可以直接用已加载配置填充本机客户端，不要求用户重复输入本机账号密码。

远程模式继续使用 GUI 配置文件中的远程地址、用户名和密码。

## 错误处理

服务启动失败时：

1. GUI 主窗口仍显示。
2. 状态栏展示失败原因。
3. 脚本列表和执行按钮不可用。
4. 用户可以修正 `config.yaml` 后重启程序。

HTTP 端口被占用时：

1. 服务启动失败，错误应包含监听地址。
2. GUI 不尝试自动换端口，因为外部调用者可能依赖固定地址。

MQTT 启动失败时：

1. 保持现有语义：如果配置启用 MQTT 且启动失败，服务启动失败。
2. 不静默降级，避免用户以为调度已可用。

退出时：

1. 对 HTTP server 使用有限超时的 graceful shutdown。
2. MQTT client 需要停止订阅和断开连接。
3. registry watcher 通过 context 退出。
4. 已启动脚本的处理保持现有 runner 语义，不在本设计中新增强制取消。

## 构建和发布

`build.bat` 应生成至少一个主使用产物：

```text
deploy-agent-gui.exe
```

该程序内嵌 UAC `requireAdministrator` 清单，双击后获得管理员权限，脚本子进程继续继承管理员 token。

`build.bat` 继续生成 `deploy-agent.exe` 作为兼容产物，但 README 应明确：

1. 日常使用只需要启动 `deploy-agent-gui.exe`。
2. GUI 版不再要求同目录存在 `deploy-agent.exe`。
3. `deploy-agent.exe` 如果存在，只是无界面后台入口，不是 GUI 的依赖。

## 文档更新

README 需要更新：

1. 项目运行方式改为优先启动 `deploy-agent-gui.exe`。
2. 说明 GUI 同时是服务，打开后自动启动 HTTP/MQTT。
3. 说明关闭窗口会最小化到托盘，托盘退出才停止服务。
4. 删除“GUI 会启动同目录 deploy-agent.exe”的旧说明。
5. 保留 HTTP API、MQTT、配置文件和安全说明。
6. 更新构建产物说明。

## 测试策略

### 服务核心测试

新增或调整 `internal/appservice` 测试，覆盖：

1. 配置路径查找失败时返回清晰错误。
2. 配置加载失败时返回错误。
3. HTTP disabled 且 MQTT disabled 时沿用配置校验错误。
4. HTTP enabled 时生成正确本机 Base URL。
5. 通配 host 转换为 GUI 可连接的 `127.0.0.1`。
6. pre-run download 配置仍被传入 executor。

### GUI 逻辑测试

尽量把托盘和窗口关闭决策抽成可测试的小函数，覆盖：

1. 普通关闭窗口隐藏而不是退出。
2. 托盘退出触发服务 shutdown。
3. 本机模式使用内嵌服务地址和认证信息。
4. 远程模式仍使用 GUI 配置地址和认证信息。

Fyne 真实托盘行为以 Windows 手动验证为准。

### 回归测试

继续运行：

```cmd
gofmt -w .
go test ./...
go vet ./...
build.bat
```

Windows 手动验证：

1. 双击 `deploy-agent-gui.exe`，弹 UAC，窗口出现。
2. GUI 自动连接本机服务并加载脚本。
3. curl 可以访问 `/health`。
4. GUI 执行脚本并实时显示输出。
5. 关闭窗口后进程仍在，HTTP `/health` 仍可访问。
6. 托盘打开窗口可恢复。
7. 托盘退出后进程结束，HTTP 端口释放。

## 实施顺序建议

1. 抽出 `internal/appservice`，让现有 `main.go` 复用它。
2. 为 `appservice` 补充服务启动和 Base URL 测试。
3. 改 GUI 启动流程，启动内嵌服务并自动连接。
4. 移除外部 `deploy-agent.exe` 启停按钮和 `localservice` 主流程依赖。
5. 增加窗口关闭拦截和托盘菜单。
6. 更新 README 和构建说明。
7. 运行 Go 测试、vet 和 Windows 构建。
8. 做托盘和单进程手动验证。
