package executor

import (
	"context"
	"errors"
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
)

type Executor struct {
	reg     *registry.Registry
	timeout time.Duration
}

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

func New(reg *registry.Registry, timeout time.Duration) *Executor {
	return &Executor{reg: reg, timeout: timeout}
}

func (e *Executor) List() []string {
	return e.reg.List()
}

func (e *Executor) RunCollect(ctx context.Context, script string) (Result, error) {
	return e.RunStream(ctx, script, nil)
}

func (e *Executor) RunStream(ctx context.Context, script string, onOutput OutputFunc) (Result, error) {
	entry, err := e.reg.Lookup(script)
	if err != nil {
		return lookupErrorResult(script, err)
	}

	if !entry.TryLock() {
		return Result{Script: entry.Name, ExitCode: -1}, ErrScriptBusy
	}
	defer entry.Unlock()

	res, err := runner.RunStream(ctx, entry.Path, e.timeout, adaptOutputFunc(onOutput))
	out := Result{
		Script:     entry.Name,
		ExitCode:   res.ExitCode,
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		StartedAt:  res.StartedAt,
		FinishedAt: res.FinishedAt,
		TimedOut:   res.TimedOut,
	}
	if res.TimedOut {
		return out, ErrScriptTimedOut
	}
	if err != nil {
		return out, runnerStartError{err: err}
	}
	return out, nil
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
