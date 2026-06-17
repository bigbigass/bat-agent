# Pre-Run Download Design

## Goal

Add an optional "download before running" behavior to deploy-agent so the GUI can download a delivery artifact before executing a selected script.

The download source path is fixed by business rule:

```text
/交付产物/<项目编号>/<产物文件名>
```

The GUI supplies the project number and artifact file name at run time.

## Scope

This feature covers:

- GUI controls for enabling pre-run download and entering project/artifact values.
- HTTP request support for optional pre-run download metadata.
- Executor orchestration that runs a configured download script before the selected target script.
- Streaming output from both the download phase and target script phase.
- Documentation and tests for config, HTTP behavior, executor behavior, GUI request generation, and error handling.

This feature does not cover:

- Uploading artifacts.
- Listing remote artifacts.
- Persisting artifact history.
- Changing the whitelist rules for target scripts.
- Making `tools/cookie.ini` part of the repository.

## Configuration

Add a top-level `preRun.download` config section:

```yaml
preRun:
  download:
    script: "tools/download_simple.bat"
    timeoutSeconds: 300
```

`script` points to the download script. Relative paths resolve from the directory containing `config.yaml`; this makes packaged deployments predictable even when the process working directory differs.

`timeoutSeconds` limits the download step only. The existing `runner.timeoutSeconds` continues to limit the selected target script.

If the GUI sends `preDownload.enabled=true` while `preRun.download.script` is empty, the service rejects the run before starting the target script.

## HTTP API

Existing clients remain compatible. Requests that only send `script` behave exactly as they do today.

`POST /run` and `POST /run/stream` accept an optional `preDownload` object:

```json
{
  "script": "deploy.bat",
  "preDownload": {
    "enabled": true,
    "project": "ProjectA",
    "artifact": "app.zip"
  }
}
```

Validation rules:

- `preDownload` can be omitted.
- If `enabled=false`, `project` and `artifact` are ignored.
- If `enabled=true`, both `project` and `artifact` must be non-empty after trimming spaces.
- `project` and `artifact` must not contain `/`, `\`, `:`, or `..`.
- `artifact` is a file name, not a path.

Validation failures return HTTP 400 with stable error text.

## GUI Behavior

The management console adds:

- A checkbox labeled `执行前下载`.
- A text input labeled `项目编号`.
- A text input labeled `产物文件名`.

When the checkbox is unchecked, the inputs are disabled and the GUI sends the old request shape.

When the checkbox is checked, both inputs are required before the run button can trigger a request. The GUI sends the `preDownload` object to `/run/stream`.

The GUI appends streamed output as it does today. Download output and target script output use the existing stdout/stderr stream format so old stream parsing remains simple.

## Execution Flow

All entry points should route through the executor orchestration so behavior is shared by HTTP, GUI-over-HTTP, and future callers.

For a request with pre-run download enabled:

1. Validate and look up the target script through the registry.
2. Acquire the target script lock.
3. Run the configured download script with arguments:

   ```cmd
   download_simple.bat <项目编号> <产物文件名>
   ```

4. If the download exits with code 0, run the selected target script.
5. If the download fails, times out, or cannot start, do not run the selected target script.
6. Release the target script lock after the whole flow completes.

The target script lock covers both download and target execution. This prevents two runs of the same script from downloading different artifacts into shared local paths at the same time. Different target scripts may still run concurrently.

The configured download script is not selected through the script registry. It is a service-side helper path, not a user-callable whitelist script.

## Results And Errors

If the download step fails:

- The target script is not executed.
- `/run` returns HTTP 500 for download start/non-zero-exit failures and HTTP 504 for download timeout.
- `/run/stream` returns a final NDJSON message with a stable error.
- Download stdout/stderr captured before failure remains visible to the caller as the run stdout/stderr.

Stable executor errors include:

```text
pre-run download is not configured
invalid pre-run download request
pre-run download failed
pre-run download timed out
```

Timeout behavior:

- Download timeout maps to `pre-run download timed out`.
- Download timeout sets `timedOut: true` because the run ended due to a timeout before the target script could start.
- Target script timeout keeps the existing `script timed out` behavior and HTTP 504 for `/run`.

The response shape stays compatible by adding fields only when needed. The existing `timedOut: true` rule for target script timeouts remains unchanged.

## Security And Path Safety

The selected target script remains constrained by the registry whitelist.

The download helper script path comes from service config and resolves once at startup. It does not accept request-controlled paths.

Request-controlled `project` and `artifact` values are passed as process arguments, not interpolated into a shell command string. The download script itself constructs the fixed remote path.

The service rejects path separators, drive separators, and `..` in `project` and `artifact` values to avoid turning the remote path or local download destination into an unintended path.

Secrets in `tools/cookie.ini` remain local-only and must not be committed.

## Testing

Implementation should be test-driven.

Coverage should include:

- Config loading defaults and validation for `preRun.download`.
- HTTP decode and validation for optional `preDownload`.
- Backward compatibility for old `/run` and `/run/stream` requests.
- Executor runs download before target script when enabled.
- Executor skips target script when download fails.
- Executor returns stable timeout/error text for download failures.
- Target script lock covers the whole pre-download plus target run flow.
- GUI client sends old request shape when unchecked.
- GUI client sends `preDownload` when checked.
- GUI run button validation requires project and artifact when checked.

Windows verification is preferred for final runner/process behavior because the production runner uses `cmd.exe`, `.bat` files, and `taskkill`.
