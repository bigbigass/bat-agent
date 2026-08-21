package executor

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/liqixin/deploy-agent/internal/registry"
	"github.com/liqixin/deploy-agent/internal/runner"
)

var (
	ErrInvalidScriptName = errors.New("invalid script name")
	ErrScriptNotFound    = errors.New("script not found")
	ErrScriptBusy        = errors.New("script is already running")
	ErrRunnerStart       = errors.New("runner start failed")
	ErrScriptTimedOut    = errors.New("script timed out")
	ErrInvalidScriptArg  = errors.New("invalid script argument")

	ErrPreDownloadNotConfigured  = errors.New("pre-run download is not configured")
	ErrInvalidPreDownloadRequest = errors.New("invalid pre-run download request")
	ErrPreDownloadFailed         = errors.New("pre-run download failed")
	ErrPreDownloadTimedOut       = errors.New("pre-run download timed out")
)

type Executor struct {
	reg         *registry.Registry
	timeout     time.Duration
	preDownload PreDownloadConfig
}

type RunOptions struct {
	Args        []string
	PreDownload PreDownloadRequest
}

type PreDownloadRequest struct {
	Enabled  bool
	Project  string
	Artifact string
}

type PreDownloadConfig struct {
	ScriptPath string
	Timeout    time.Duration
}

type Option func(*Executor)

type Result struct {
	Script     string
	ExitCode   int
	Stdout     string
	Stderr     string
	StartedAt  time.Time
	FinishedAt time.Time
	TimedOut   bool
}

type OutputChunk struct {
	Stream string
	Data   string
}

type OutputFunc func(OutputChunk)

func New(reg *registry.Registry, timeout time.Duration, opts ...Option) *Executor {
	e := &Executor{reg: reg, timeout: timeout}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func WithPreDownloadConfig(cfg PreDownloadConfig) Option {
	return func(e *Executor) {
		e.preDownload = cfg
	}
}

func (e *Executor) List() []string {
	return e.reg.List()
}

func (e *Executor) RunCollect(ctx context.Context, script string) (Result, error) {
	return e.RunCollectWithOptions(ctx, script, RunOptions{})
}

func (e *Executor) RunCollectWithOptions(ctx context.Context, script string, opts RunOptions) (Result, error) {
	return e.RunStreamWithOptions(ctx, script, opts, nil)
}

func (e *Executor) RunStream(ctx context.Context, script string, onOutput OutputFunc) (Result, error) {
	return e.RunStreamWithOptions(ctx, script, RunOptions{}, onOutput)
}

func (e *Executor) RunStreamWithOptions(ctx context.Context, script string, opts RunOptions, onOutput OutputFunc) (Result, error) {
	entry, err := e.reg.Lookup(script)
	if err != nil {
		return lookupErrorResult(script, err)
	}

	preDownload, err := validatePreDownload(opts.PreDownload)
	if err != nil {
		return Result{Script: entry.Name, ExitCode: -1}, err
	}
	if preDownload.Enabled && strings.TrimSpace(e.preDownload.ScriptPath) == "" {
		return Result{Script: entry.Name, ExitCode: -1}, ErrPreDownloadNotConfigured
	}
	args, err := validateScriptArgs(opts.Args)
	if err != nil {
		return Result{Script: entry.Name, ExitCode: -1}, err
	}

	if !entry.TryLock() {
		return Result{Script: entry.Name, ExitCode: -1}, ErrScriptBusy
	}
	defer entry.Unlock()

	var preResult Result
	if preDownload.Enabled {
		preResult, err = e.runPreDownload(ctx, entry.Name, preDownload, onOutput)
		if err != nil {
			return preResult, err
		}
	}

	res, err := runner.RunStreamWithArgs(ctx, entry.Path, args, e.timeout, adaptOutputFunc(onOutput))
	out := resultFromRunner(entry.Name, res)
	if preDownload.Enabled {
		out = mergeResults(entry.Name, preResult, out)
	}
	if res.TimedOut {
		return out, ErrScriptTimedOut
	}
	if err != nil {
		return out, runnerStartError{err: err}
	}
	return out, nil
}

func validateScriptArgs(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	out := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsAny(arg, "\"\r\n") {
			return nil, ErrInvalidScriptArg
		}
		out[i] = arg
	}
	return out, nil
}

func validatePreDownload(req PreDownloadRequest) (PreDownloadRequest, error) {
	if !req.Enabled {
		return PreDownloadRequest{}, nil
	}
	req.Project = strings.TrimSpace(req.Project)
	req.Artifact = strings.TrimSpace(req.Artifact)
	if req.Project == "" || req.Artifact == "" {
		return req, ErrInvalidPreDownloadRequest
	}
	if unsafeDownloadValue(req.Project) || unsafeDownloadValue(req.Artifact) {
		return req, ErrInvalidPreDownloadRequest
	}
	return req, nil
}

func (e *Executor) runPreDownload(ctx context.Context, script string, req PreDownloadRequest, onOutput OutputFunc) (Result, error) {
	res, err := runner.RunStreamWithArgs(ctx, e.preDownload.ScriptPath, []string{req.Project, req.Artifact}, e.preDownload.Timeout, adaptOutputFunc(onOutput))
	out := resultFromRunner(script, res)
	if res.TimedOut {
		return out, ErrPreDownloadTimedOut
	}
	if err != nil {
		return out, preDownloadStartError{err: err}
	}
	if res.ExitCode != 0 {
		return out, ErrPreDownloadFailed
	}
	return out, nil
}

func mergeResults(script string, pre Result, target Result) Result {
	return Result{
		Script:     script,
		ExitCode:   target.ExitCode,
		Stdout:     pre.Stdout + target.Stdout,
		Stderr:     pre.Stderr + target.Stderr,
		StartedAt:  firstTime(pre.StartedAt, target.StartedAt),
		FinishedAt: target.FinishedAt,
		TimedOut:   target.TimedOut,
	}
}

func firstTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

func unsafeDownloadValue(value string) bool {
	return strings.ContainsAny(value, `/\:&|<>^%"!`) || strings.Contains(value, "..")
}

func resultFromRunner(script string, res runner.Result) Result {
	return Result{
		Script:     script,
		ExitCode:   res.ExitCode,
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		StartedAt:  res.StartedAt,
		FinishedAt: res.FinishedAt,
		TimedOut:   res.TimedOut,
	}
}

func lookupErrorResult(script string, err error) (Result, error) {
	res := Result{Script: script, ExitCode: -1}
	switch {
	case errors.Is(err, registry.ErrInvalid):
		return res, ErrInvalidScriptName
	case errors.Is(err, registry.ErrNotFound):
		return res, ErrScriptNotFound
	default:
		return res, err
	}
}

func adaptOutputFunc(onOutput OutputFunc) runner.OutputFunc {
	if onOutput == nil {
		return nil
	}
	return func(chunk runner.OutputChunk) {
		onOutput(OutputChunk{Stream: string(chunk.Stream), Data: chunk.Data})
	}
}

func StableError(err error) string {
	switch {
	case errors.Is(err, ErrInvalidScriptName):
		return "invalid script name"
	case errors.Is(err, ErrScriptNotFound):
		return "script not found"
	case errors.Is(err, ErrScriptBusy):
		return "script is already running"
	case errors.Is(err, ErrRunnerStart):
		return "runner start failed"
	case errors.Is(err, ErrScriptTimedOut):
		return "script timed out"
	case errors.Is(err, ErrInvalidScriptArg):
		return "invalid script argument"
	case errors.Is(err, ErrPreDownloadNotConfigured):
		return "pre-run download is not configured"
	case errors.Is(err, ErrInvalidPreDownloadRequest):
		return "invalid pre-run download request"
	case errors.Is(err, ErrPreDownloadFailed):
		return "pre-run download failed"
	case errors.Is(err, ErrPreDownloadTimedOut):
		return "pre-run download timed out"
	case err == nil:
		return ""
	default:
		return err.Error()
	}
}

type runnerStartError struct {
	err error
}

func (e runnerStartError) Error() string {
	return ErrRunnerStart.Error() + ": " + e.err.Error()
}

func (e runnerStartError) Unwrap() error {
	return e.err
}

func (e runnerStartError) Is(target error) bool {
	return target == ErrRunnerStart
}

type preDownloadStartError struct {
	err error
}

func (e preDownloadStartError) Error() string {
	return ErrPreDownloadFailed.Error() + ": " + e.err.Error()
}

func (e preDownloadStartError) Unwrap() error {
	return e.err
}

func (e preDownloadStartError) Is(target error) bool {
	return target == ErrPreDownloadFailed
}
