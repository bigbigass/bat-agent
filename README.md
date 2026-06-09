# deploy-agent

一个跑在 Windows 上的 Go HTTP 服务。把它丢进任何放着一堆 `.bat` / `.cmd` 脚本的目录，启动后即可通过 HTTP 触发这些脚本。程序以管理员权限运行，子进程自动继承管理员 token。

## 构建

需要 Go 1.19+。

```cmd
build.bat
```

产物：`deploy-agent.exe`（已内嵌 UAC 清单，双击会弹管理员确认框）。

## 配置

首次运行前拷贝一份 `config.example.yaml` 为 `config.yaml`，至少改掉密码：

```yaml
server:
  host: 0.0.0.0
  port: 8080
auth:
  username: admin
  password: change-me-please        # 长度 ≥ 8
runner:
  timeoutSeconds: 300               # 单脚本超时
  scriptDir: ""                     # 留空 = exe 所在目录
```

`config.yaml` 优先从 exe 同目录读，没有则从当前工作目录读。

## 运行

双击 `deploy-agent.exe`（会弹 UAC），或在管理员 cmd 里直接跑：

```cmd
deploy-agent.exe
```

启动日志会打印扫描到的 bat 列表。

## HTTP API

所有除 `/health` 之外的端点都要 Basic Auth。

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

### `POST /run`
请求：
```json
{"script": "deploy.bat"}
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
| 400 | JSON 解析失败 / script 非法（含路径分隔符） |
| 401 | Basic Auth 失败 |
| 404 | 脚本不在白名单 |
| 409 | 同名脚本正在执行（同名串行） |
| 500 | 启动进程失败 |
| 504 | 执行超时，返回体 `timedOut: true` |

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

## 安全说明

- 只允许调用 **exe 所在目录**下的 `.bat` / `.cmd`，不支持子目录或路径穿越。
- 同一脚本同时只能跑一份；并发请求第二个返回 409。
- 默认没启 TLS。要暴露到外网请在前面加反向代理（Caddy/Nginx 等）或自己加 TLS。
- Basic Auth 明文传输，强烈建议配合 HTTPS。

## 非目标

目前不做：异步任务 / 参数传递 / Windows 服务注册 / 日志轮转。需要时再扩。
