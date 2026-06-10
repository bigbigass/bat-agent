package mqttapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/registry"
)

type publishedMessage struct {
	topic   string
	payload []byte
}

type fakePublisher struct {
	mu        sync.Mutex
	messages  []publishedMessage
	onPublish func(topic string, payload []byte)
}

func (p *fakePublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	if p.onPublish != nil {
		p.onPublish(topic, payload)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, publishedMessage{
		topic:   topic,
		payload: append([]byte(nil), payload...),
	})
	return nil
}

func (p *fakePublisher) snapshot() []publishedMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]publishedMessage, len(p.messages))
	copy(out, p.messages)
	return out
}

func TestHandleCommandPublishesOutputAndFinalMessage(t *testing.T) {
	exec := newTestExecutor(t, map[string]string{
		"hello.bat": "@echo off\r\necho hello\r\n",
	})
	pub := &fakePublisher{}
	handler := NewHandler(exec, pub, 1)

	err := handler.Handle(context.Background(), []byte(`{"requestId":"req-1","script":"hello.bat","replyTo":"deploy/replies"}`))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	messages := pub.snapshot()
	if len(messages) < 2 {
		t.Fatalf("expected at least output and final messages, got %d", len(messages))
	}
	if messages[0].topic != "deploy/replies" {
		t.Fatalf("first topic = %q, want deploy/replies", messages[0].topic)
	}
	var out OutputMessage
	unmarshalPayload(t, messages[0].payload, &out)
	if out.RequestID != "req-1" || out.Script != "hello.bat" || out.Stream != "stdout" || out.Done {
		t.Fatalf("unexpected output message: %#v", out)
	}
	if !strings.Contains(out.Data, "hello") {
		t.Fatalf("output data = %q, want it to contain hello", out.Data)
	}

	var final FinalMessage
	unmarshalPayload(t, messages[len(messages)-1].payload, &final)
	if !final.Done {
		t.Fatal("final Done = false, want true")
	}
	if final.RequestID != "req-1" || final.Script != "hello.bat" {
		t.Fatalf("unexpected final identity: %#v", final)
	}
	if final.Error != "" {
		t.Fatalf("final Error = %q, want empty", final.Error)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("final ExitCode = %v, want 0", final.ExitCode)
	}
	if final.TimedOut == nil || *final.TimedOut {
		t.Fatalf("final TimedOut = %v, want false", final.TimedOut)
	}
	if final.StartedAt == nil || final.FinishedAt == nil || final.DurationMs < 0 {
		t.Fatalf("final timing fields not set correctly: %#v", final)
	}
}

func TestHandleCommandPublishesMissingRequestID(t *testing.T) {
	pub := &fakePublisher{}
	handler := NewHandler(newTestExecutor(t, nil), pub, 1)

	err := handler.Handle(context.Background(), []byte(`{"script":"hello.bat","replyTo":"deploy/replies"}`))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	messages := pub.snapshot()
	if len(messages) != 1 {
		t.Fatalf("published %d messages, want 1", len(messages))
	}
	var final FinalMessage
	unmarshalPayload(t, messages[0].payload, &final)
	if final.Error != ErrMissingRequestID.Error() || !final.Done {
		t.Fatalf("unexpected final message: %#v", final)
	}
	if final.ExitCode != nil || final.TimedOut != nil || final.StartedAt != nil || final.FinishedAt != nil || final.DurationMs != 0 {
		t.Fatalf("dispatch error should omit run fields: %#v", final)
	}
}

func TestHandleCommandWithoutReplyToPublishesNothing(t *testing.T) {
	pub := &fakePublisher{}
	handler := NewHandler(newTestExecutor(t, nil), pub, 1)

	err := handler.Handle(context.Background(), []byte(`{"requestId":"req-1","script":"hello.bat"}`))
	if err == nil {
		t.Fatal("Handle returned nil error, want missing replyTo error")
	}
	if got := len(pub.snapshot()); got != 0 {
		t.Fatalf("published %d messages, want 0", got)
	}
}

func TestHandleCommandPublishesScriptNotFound(t *testing.T) {
	pub := &fakePublisher{}
	handler := NewHandler(newTestExecutor(t, nil), pub, 1)

	err := handler.Handle(context.Background(), []byte(`{"requestId":"req-1","script":"missing.bat","replyTo":"deploy/replies"}`))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	var final FinalMessage
	unmarshalPayload(t, pub.snapshot()[0].payload, &final)
	if final.Error != executor.StableError(executor.ErrScriptNotFound) {
		t.Fatalf("final Error = %q, want script not found", final.Error)
	}
	if final.ExitCode != nil || final.StartedAt != nil || final.FinishedAt != nil {
		t.Fatalf("script not found should omit run fields: %#v", final)
	}
}

func TestHandleCommandPublishesInvalidScriptToReplyTo(t *testing.T) {
	pub := &fakePublisher{}
	handler := NewHandler(newTestExecutor(t, nil), pub, 1)

	err := handler.Handle(context.Background(), []byte(`{"requestId":"req-1","script":"..\\evil.bat","replyTo":"deploy/replies"}`))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	var final FinalMessage
	unmarshalPayload(t, pub.snapshot()[0].payload, &final)
	if final.Error != ErrInvalidScript.Error() || !final.Done {
		t.Fatalf("unexpected final message: %#v", final)
	}
}

func TestHandleCommandPublishesNonZeroExitWithoutError(t *testing.T) {
	exec := newTestExecutor(t, map[string]string{
		"fail.bat": "@echo off\r\nexit /b 7\r\n",
	})
	pub := &fakePublisher{}
	handler := NewHandler(exec, pub, 1)

	err := handler.Handle(context.Background(), []byte(`{"requestId":"req-1","script":"fail.bat","replyTo":"deploy/replies"}`))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	messages := pub.snapshot()
	var final FinalMessage
	unmarshalPayload(t, messages[len(messages)-1].payload, &final)
	if final.Error != "" {
		t.Fatalf("final Error = %q, want empty", final.Error)
	}
	if final.ExitCode == nil || *final.ExitCode != 7 {
		t.Fatalf("final ExitCode = %v, want 7", final.ExitCode)
	}
}

func TestHandleCommandPublishesFinalAfterOutputMessages(t *testing.T) {
	firstPublishStarted := make(chan struct{})
	allowFirstPublish := make(chan struct{})
	var once sync.Once
	pub := &fakePublisher{
		onPublish: func(topic string, payload []byte) {
			var marker struct {
				Done bool `json:"done"`
			}
			if err := json.Unmarshal(payload, &marker); err != nil {
				return
			}
			if !marker.Done {
				once.Do(func() {
					close(firstPublishStarted)
					<-allowFirstPublish
				})
			}
		},
	}
	exec := newTestExecutor(t, map[string]string{
		"hello.bat": "@echo off\r\necho first\r\n",
	})
	handler := NewHandler(exec, pub, 1)

	done := make(chan error, 1)
	go func() {
		done <- handler.Handle(context.Background(), []byte(`{"requestId":"req-1","script":"hello.bat","replyTo":"deploy/replies"}`))
	}()

	select {
	case <-firstPublishStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for output publish to start")
	}
	if got := len(pub.snapshot()); got != 0 {
		t.Fatalf("handler published final before output drained; got %d recorded messages", got)
	}
	close(allowFirstPublish)

	if err := <-done; err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	messages := pub.snapshot()
	if len(messages) < 2 {
		t.Fatalf("published %d messages, want at least 2", len(messages))
	}
	for i, msg := range messages[:len(messages)-1] {
		var out OutputMessage
		unmarshalPayload(t, msg.payload, &out)
		if out.Done {
			t.Fatalf("message %d is final before last message: %#v", i, out)
		}
	}
	var final FinalMessage
	unmarshalPayload(t, messages[len(messages)-1].payload, &final)
	if !final.Done {
		t.Fatalf("last message is not final: %#v", final)
	}
}

func newTestExecutor(t *testing.T, files map[string]string) *executor.Executor {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return executor.New(reg, 5*time.Second)
}

func unmarshalPayload(t *testing.T, payload []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(payload, v); err != nil {
		t.Fatalf("Unmarshal(%q) returned error: %v", string(payload), err)
	}
}
