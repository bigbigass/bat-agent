//go:build windows

package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const maxOutputBytes = 1 << 20 // 1 MiB per stream

type Result struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	StartedAt  time.Time
	FinishedAt time.Time
	TimedOut   bool
}

type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

type OutputChunk struct {
	Stream Stream
	Data   string
}

// OutputFunc receives decoded stdout/stderr chunks from RunStream. Callbacks
// are invoked synchronously and serialized across both streams, and must return
// promptly. Callers that publish to a network or other slow sink should enqueue
// work and publish outside the runner callback.
type OutputFunc func(OutputChunk)

// Run executes the given .bat/.cmd script via cmd.exe and returns captured
// output. Runs with the parent process's token, so if deploy-agent is
// elevated the script is elevated too.
func Run(ctx context.Context, path string, timeout time.Duration) (Result, error) {
	return RunStreamWithArgs(ctx, path, nil, timeout, nil)
}

// RunStream executes the given .bat/.cmd script via cmd.exe, captures output,
// and optionally streams stdout/stderr chunks as they are written. Output
// callbacks are invoked synchronously and serialized across both streams, so a
// slow callback can block script output processing.
func RunStream(ctx context.Context, path string, timeout time.Duration, onOutput OutputFunc) (Result, error) {
	return RunStreamWithArgs(ctx, path, nil, timeout, onOutput)
}

// RunStreamWithArgs executes the given .bat/.cmd script with literal script
// arguments, captures output, and optionally streams stdout/stderr chunks.
func RunStreamWithArgs(ctx context.Context, path string, scriptArgs []string, timeout time.Duration, onOutput OutputFunc) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdoutBuf := &cappedBuffer{limit: maxOutputBytes}
	stderrBuf := &cappedBuffer{limit: maxOutputBytes}
	emit := serializedOutputFunc(onOutput)
	stdoutWriter := &streamCaptureWriter{capture: stdoutBuf, stream: StreamStdout, onOutput: emit}
	stderrWriter := &streamCaptureWriter{capture: stderrBuf, stream: StreamStderr, onOutput: emit}

	res := Result{ExitCode: -1, StartedAt: time.Now()}

	cmd, cleanup, err := batchCommand(ctx, path, scriptArgs)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		res.FinishedAt = time.Now()
		return res, err
	}
	cmd.Dir = filepath.Dir(path)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	err = cmd.Start()
	if err != nil {
		res.FinishedAt = time.Now()
		return res, err
	}

	waitErr := cmd.Wait()
	stdoutWriter.Flush()
	stderrWriter.Flush()
	res.FinishedAt = time.Now()

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		// Kill process tree as a safety net. cmd.exe may have spawned
		// children (start, call .exe) that Go's default kill won't reach.
		if cmd.Process != nil {
			killTree(cmd.Process.Pid)
		}
	}

	res.Stdout = decodeOutput(stdoutBuf.Bytes())
	res.Stderr = decodeOutput(stderrBuf.Bytes())

	if res.TimedOut {
		return res, nil
	}

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, waitErr
	}
	res.ExitCode = 0
	return res, nil
}

func batchCommand(ctx context.Context, path string, scriptArgs []string) (*exec.Cmd, func(), error) {
	if len(scriptArgs) == 0 {
		return exec.CommandContext(ctx, "cmd.exe", "/c", path), nil, nil
	}

	wrapperPath, err := writeArgsWrapper(path, len(scriptArgs))
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", wrapperPath)
	cmd.Env = os.Environ()
	for i, arg := range scriptArgs {
		cmd.Env = append(cmd.Env, runnerArgName(i)+"="+escapeExpandedBatchArg(arg))
	}
	return cmd, func() { _ = os.Remove(wrapperPath) }, nil
}

func writeArgsWrapper(path string, argCount int) (string, error) {
	file, err := os.CreateTemp("", "deploy-agent-runner-*.cmd")
	if err != nil {
		return "", err
	}
	wrapperPath := file.Name()

	var b strings.Builder
	b.WriteString("@echo off\r\n")
	b.WriteString("setlocal DisableDelayedExpansion\r\n")
	b.WriteString(`"`)
	b.WriteString(escapeBatchFileLiteral(path))
	b.WriteString(`"`)
	for i := 0; i < argCount; i++ {
		b.WriteString(` "%`)
		b.WriteString(runnerArgName(i))
		b.WriteString(`%"`)
	}
	b.WriteString("\r\n")

	if _, err := file.WriteString(b.String()); err != nil {
		_ = file.Close()
		_ = os.Remove(wrapperPath)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(wrapperPath)
		return "", err
	}
	return wrapperPath, nil
}

func runnerArgName(index int) string {
	return "DEPLOY_AGENT_ARG_" + strconv.Itoa(index+1)
}

func escapeBatchFileLiteral(value string) string {
	return strings.NewReplacer(
		`^`, `^^`,
		`%`, `%%`,
	).Replace(value)
}

func escapeExpandedBatchArg(value string) string {
	return strings.NewReplacer(
		`^`, `^^`,
		`&`, `^&`,
		`<`, `^<`,
		`>`, `^>`,
		`|`, `^|`,
		`(`, `^(`,
		`)`, `^)`,
	).Replace(value)
}

func serializedOutputFunc(onOutput OutputFunc) OutputFunc {
	if onOutput == nil {
		return nil
	}
	var mu sync.Mutex
	return func(chunk OutputChunk) {
		mu.Lock()
		defer mu.Unlock()
		onOutput(chunk)
	}
}

type streamCaptureWriter struct {
	capture  *cappedBuffer
	stream   Stream
	onOutput OutputFunc
	pending  []byte
}

func (w *streamCaptureWriter) Write(p []byte) (int, error) {
	if _, err := w.capture.Write(p); err != nil {
		return 0, err
	}
	if w.onOutput != nil {
		w.emit(p)
	}
	return len(p), nil
}

func (w *streamCaptureWriter) Flush() {
	if len(w.pending) == 0 || w.onOutput == nil {
		w.pending = nil
		return
	}
	w.onOutput(OutputChunk{Stream: w.stream, Data: decodeOutput(w.pending)})
	w.pending = nil
}

func (w *streamCaptureWriter) emit(p []byte) {
	b := append(w.pending, p...)
	w.pending = nil

	complete, pending, ok := splitUTF8Complete(b)
	if ok {
		w.pending = append(w.pending, pending...)
		if len(complete) > 0 {
			w.onOutput(OutputChunk{Stream: w.stream, Data: string(complete)})
		}
		return
	}

	complete, pending = splitGBKComplete(b)
	w.pending = append(w.pending, pending...)
	if len(complete) > 0 {
		w.onOutput(OutputChunk{Stream: w.stream, Data: decodeOutput(complete)})
	}
}

func splitUTF8Complete(b []byte) (complete []byte, pending []byte, ok bool) {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			if utf8SequenceSize(b[i]) > len(b)-i {
				return b[:i], b[i:], true
			}
			return nil, nil, false
		}
		i += size
	}
	return b, nil, true
}

func utf8SequenceSize(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b >= 0xC2 && b <= 0xDF:
		return 2
	case b >= 0xE0 && b <= 0xEF:
		return 3
	case b >= 0xF0 && b <= 0xF4:
		return 4
	default:
		return 0
	}
}

func splitGBKComplete(b []byte) (complete []byte, pending []byte) {
	for i := 0; i < len(b); {
		if b[i] <= 0x7F {
			i++
			continue
		}
		if isGBKLeadByte(b[i]) && i+1 >= len(b) {
			return b[:i], b[i:]
		}
		if isGBKLeadByte(b[i]) && isGBKTrailByte(b[i+1]) {
			i += 2
			continue
		}
		i++
	}
	return b, nil
}

func isGBKLeadByte(b byte) bool {
	return b >= 0x81 && b <= 0xFE
}

func isGBKTrailByte(b byte) bool {
	return b >= 0x40 && b <= 0xFE && b != 0x7F
}

// killTree uses taskkill to terminate the pid and all descendants.
func killTree(pid int) {
	_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}

// decodeOutput returns a UTF-8 string. If bytes are already valid UTF-8
// (e.g. bat used `chcp 65001`), return as-is; otherwise assume GBK which
// is the default on simplified-Chinese Windows consoles.
func decodeOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b)
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(decoded)
}

// cappedBuffer is an io.Writer that stores at most `limit` bytes and
// silently discards the rest.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) <= remaining {
		return c.buf.Write(p)
	}
	_, _ = c.buf.Write(p[:remaining])
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte { return c.buf.Bytes() }

var _ io.Writer = (*cappedBuffer)(nil)
