# AGENTS.md

## Project Overview

`deploy-agent` is a Windows-targeted Go HTTP service that runs whitelisted `.bat` and `.cmd` scripts from a configured directory. It is intended to run elevated, so child scripts inherit the administrator token.

Primary modules:

- `main.go`: loads config, builds the script registry, wires auth and HTTP routes, and handles shutdown.
- `internal/config`: YAML config loading, defaults, validation, and script directory resolution.
- `internal/registry`: script whitelist scanning, lookup validation, sorted listing, and per-script locks.
- `internal/httpapi`: `/health`, `/scripts`, and `/run` handlers plus JSON responses and access logging.
- `internal/auth`: Basic Auth middleware.
- `internal/runner`: Windows-only script execution, timeout handling, output capture, process-tree kill, and GBK fallback decoding.

## Commands

Use these from the repository root:

```cmd
gofmt -w .
go test ./...
go vet ./...
build.bat
```

`build.bat` installs `github.com/akavel/rsrc` if needed, embeds `deploy-agent.manifest` into `resource.syso`, and builds `deploy-agent.exe`.

## Platform Notes

- This project targets Windows. `internal/runner/runner.go` has `//go:build windows`, uses `cmd.exe`, and calls `taskkill`.
- Prefer verifying on Windows when changing runner behavior, process handling, scripts, manifests, or build output.
- `deploy-agent.exe`, `resource.syso`, and `config.yaml` are ignored generated/local files. Do not commit local credentials from `config.yaml`; update `config.example.yaml` for documented config changes.

## Behavioral Guardrails

- `/health` is intentionally unauthenticated. All other HTTP endpoints must stay behind Basic Auth.
- Only allow script names from the registry whitelist. Preserve the current path traversal protections: no path separators, drive separators, or `..` in requested script names.
- The registry scans only files in the script directory with `.bat` or `.cmd` extensions. It should not recurse into subdirectories.
- Keep per-script locking semantics: the same script cannot run concurrently, but different scripts may run at the same time.
- Rescans should preserve existing `Entry` pointers where possible so in-flight locks survive reloads.
- Timeout responses should report `timedOut: true` with HTTP 504, and runner timeout cleanup should continue to terminate the process tree.
- Output capture is intentionally capped at 1 MiB per stream. Preserve UTF-8 passthrough and GBK fallback decoding for Chinese Windows consoles.

## Code Style

- Follow standard Go formatting with `gofmt`.
- Keep packages small and focused; prefer using the existing package boundaries instead of adding cross-cutting helpers.
- Wrap lower-level errors with context when returning them across package boundaries.
- Keep API responses stable unless the user explicitly requests an API change; update README examples when response shapes or status codes change.
