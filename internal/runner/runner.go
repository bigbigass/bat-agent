//go:build windows

package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
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

type OutputFunc func(OutputChunk)

// Run executes the given .bat/.cmd script via cmd.exe and returns captured
// output. Runs with the parent process's token, so if deploy-agent is
// elevated the script is elevated too.
func Run(ctx context.Context, path string, timeout time.Duration) (Result, error) {
	return RunStream(ctx, path, timeout, nil)
}

// RunStream executes the given .bat/.cmd script via cmd.exe, captures output,
// and optionally streams stdout/stderr chunks as they are written.
func RunStream(ctx context.Context, path string, timeout time.Duration, onOutput OutputFunc) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", path)
	cmd.Dir = filepath.Dir(path)

	stdoutBuf := &cappedBuffer{limit: maxOutputBytes}
	stderrBuf := &cappedBuffer{limit: maxOutputBytes}

	res := Result{ExitCode: -1, StartedAt: time.Now()}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		res.FinishedAt = time.Now()
		return res, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		res.FinishedAt = time.Now()
		return res, err
	}

	err = cmd.Start()
	if err != nil {
		res.FinishedAt = time.Now()
		return res, err
	}

	outputDone := make(chan error, 2)
	go captureOutput(stdoutPipe, stdoutBuf, StreamStdout, onOutput, outputDone)
	go captureOutput(stderrPipe, stderrBuf, StreamStderr, onOutput, outputDone)

	waitErr := cmd.Wait()
	stdoutErr := <-outputDone
	stderrErr := <-outputDone
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

	if stdoutErr != nil {
		return res, stdoutErr
	}
	if stderrErr != nil {
		return res, stderrErr
	}
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

func captureOutput(r io.Reader, capture *cappedBuffer, stream Stream, onOutput OutputFunc, done chan<- error) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = capture.Write(chunk)
			if onOutput != nil {
				onOutput(OutputChunk{
					Stream: stream,
					Data:   decodeOutput(chunk),
				})
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				done <- nil
				return
			}
			done <- err
			return
		}
	}
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
