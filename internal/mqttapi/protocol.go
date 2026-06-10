package mqttapi

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrInvalidJSON      = errors.New("invalid JSON body")
	ErrMissingRequestID = errors.New("missing requestId")
	ErrMissingReplyTo   = errors.New("missing replyTo")
	ErrInvalidScript    = errors.New("invalid script name")
)

type Command struct {
	RequestID string `json:"requestId"`
	Script    string `json:"script"`
	ReplyTo   string `json:"replyTo"`
}

func ParseCommand(payload []byte) (Command, error) {
	var cmd Command
	if err := json.Unmarshal(payload, &cmd); err != nil {
		return Command{}, ErrInvalidJSON
	}
	if strings.TrimSpace(cmd.ReplyTo) == "" {
		return cmd, ErrMissingReplyTo
	}
	if strings.TrimSpace(cmd.RequestID) == "" {
		return cmd, ErrMissingRequestID
	}
	if !validScriptName(cmd.Script) {
		return cmd, ErrInvalidScript
	}
	return cmd, nil
}

func validScriptName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(name))
	return (ext == ".bat" || ext == ".cmd") &&
		!strings.ContainsAny(name, `/\:`) &&
		!strings.Contains(name, "..")
}

type OutputMessage struct {
	RequestID string `json:"requestId"`
	Script    string `json:"script"`
	Stream    string `json:"stream"`
	Data      string `json:"data"`
	Done      bool   `json:"done"`
}

type FinalMessage struct {
	RequestID  string     `json:"requestId,omitempty"`
	Script     string     `json:"script,omitempty"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	TimedOut   *bool      `json:"timedOut,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
	Done       bool       `json:"done"`
}
