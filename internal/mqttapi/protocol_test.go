package mqttapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestParseCommandRequiresFields(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantCmd Command
		wantErr error
	}{
		{
			name:    "missing replyTo is checked before requestId",
			payload: `{"script":"deploy.bat"}`,
			wantCmd: Command{
				Script: "deploy.bat",
			},
			wantErr: ErrMissingReplyTo,
		},
		{
			name:    "missing requestId",
			payload: `{"replyTo":"deploy/replies","script":"deploy.bat"}`,
			wantCmd: Command{
				Script:  "deploy.bat",
				ReplyTo: "deploy/replies",
			},
			wantErr: ErrMissingRequestID,
		},
		{
			name:    "missing script",
			payload: `{"requestId":"req-1","replyTo":"deploy/replies"}`,
			wantCmd: Command{
				RequestID: "req-1",
				ReplyTo:   "deploy/replies",
			},
			wantErr: ErrInvalidScript,
		},
		{
			name:    "blank script",
			payload: `{"requestId":"req-1","replyTo":"deploy/replies","script":"   "}`,
			wantCmd: Command{
				RequestID: "req-1",
				Script:    "   ",
				ReplyTo:   "deploy/replies",
			},
			wantErr: ErrInvalidScript,
		},
		{
			name:    "invalid script extension",
			payload: `{"requestId":"req-1","replyTo":"deploy/replies","script":"tool.exe"}`,
			wantCmd: Command{
				RequestID: "req-1",
				Script:    "tool.exe",
				ReplyTo:   "deploy/replies",
			},
			wantErr: ErrInvalidScript,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := ParseCommand([]byte(tt.payload))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
			if cmd != tt.wantCmd {
				t.Fatalf("expected command %#v, got %#v", tt.wantCmd, cmd)
			}
		})
	}
}

func TestParseCommandRejectsInvalidJSON(t *testing.T) {
	_, err := ParseCommand([]byte(`{"requestId":`))
	if !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("expected %v, got %v", ErrInvalidJSON, err)
	}
	if got := ErrInvalidJSON.Error(); got != "invalid JSON body" {
		t.Fatalf("unexpected invalid JSON error string %q", got)
	}
}

func TestParseCommandAcceptsValidPayload(t *testing.T) {
	cmd, err := ParseCommand([]byte(`{"requestId":"req-1","script":"deploy.bat","replyTo":"deploy/replies"}`))
	if err != nil {
		t.Fatalf("ParseCommand returned error: %v", err)
	}

	want := Command{
		RequestID: "req-1",
		Script:    "deploy.bat",
		ReplyTo:   "deploy/replies",
	}
	if cmd != want {
		t.Fatalf("expected %#v, got %#v", want, cmd)
	}
}

func TestOutputMessageShape(t *testing.T) {
	body, err := json.Marshal(OutputMessage{
		RequestID: "req-1",
		Script:    "deploy.bat",
		Stream:    "stdout",
		Data:      "hello",
		Done:      false,
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	want := map[string]any{
		"requestId": "req-1",
		"script":    "deploy.bat",
		"stream":    "stdout",
		"data":      "hello",
		"done":      false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
}

func TestFinalMessageShape(t *testing.T) {
	exitCode := 0
	timedOut := false
	startedAt := time.Date(2026, 6, 10, 9, 30, 0, 0, time.UTC)
	finishedAt := startedAt.Add(1500 * time.Millisecond)

	body, err := json.Marshal(FinalMessage{
		RequestID:  "req-1",
		Script:     "deploy.bat",
		ExitCode:   &exitCode,
		TimedOut:   &timedOut,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		DurationMs: 1500,
		Done:       true,
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	want := map[string]any{
		"requestId":  "req-1",
		"script":     "deploy.bat",
		"exitCode":   float64(0),
		"timedOut":   false,
		"startedAt":  startedAt.Format(time.RFC3339),
		"finishedAt": finishedAt.Format(time.RFC3339Nano),
		"durationMs": float64(1500),
		"done":       true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
}

func TestFinalMessageOmitsOptionalFieldsForDispatchError(t *testing.T) {
	body, err := json.Marshal(FinalMessage{
		RequestID: "req-1",
		Script:    "deploy.bat",
		Error:     "script not found",
		Done:      true,
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	want := map[string]any{
		"requestId": "req-1",
		"script":    "deploy.bat",
		"error":     "script not found",
		"done":      true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
}
