package mqttapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/liqixin/deploy-agent/internal/executor"
)

type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

type Handler struct {
	exec *executor.Executor
	pub  Publisher
	qos  byte
}

func NewHandler(exec *executor.Executor, pub Publisher, qos byte) *Handler {
	return &Handler{exec: exec, pub: pub, qos: qos}
}

func (h *Handler) Handle(ctx context.Context, payload []byte) error {
	cmd, err := ParseCommand(payload)
	if err != nil {
		if errors.Is(err, ErrInvalidJSON) || errors.Is(err, ErrMissingReplyTo) {
			return err
		}
		return h.publishFinal(ctx, cmd.ReplyTo, FinalMessage{
			RequestID: cmd.RequestID,
			Script:    cmd.Script,
			Error:     err.Error(),
			Done:      true,
		})
	}

	outputs := newOutputQueue()
	outputErr := h.publishOutputs(ctx, cmd.ReplyTo, outputs)

	res, runErr := h.exec.RunStream(ctx, cmd.Script, func(chunk executor.OutputChunk) {
		outputs.Enqueue(OutputMessage{
			RequestID: cmd.RequestID,
			Script:    cmd.Script,
			Stream:    chunk.Stream,
			Data:      chunk.Data,
			Done:      false,
		})
	})
	outputs.Close()

	var errOut error
	if err := <-outputErr; err != nil {
		errOut = err
	}

	final := resultFinalMessage(cmd, res, runErr)
	if err := h.publishFinal(ctx, cmd.ReplyTo, final); err != nil {
		return err
	}
	return errOut
}

func (h *Handler) publishOutputs(ctx context.Context, topic string, outputs *outputQueue) <-chan error {
	done := make(chan error, 1)
	go func() {
		var firstErr error
		for {
			msg, ok := outputs.Pop()
			if !ok {
				break
			}
			if firstErr != nil {
				continue
			}
			if err := h.publishJSON(ctx, topic, msg); err != nil {
				firstErr = err
			}
		}
		done <- firstErr
	}()
	return done
}

type outputQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	messages []OutputMessage
	closed   bool
}

func newOutputQueue() *outputQueue {
	q := &outputQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *outputQueue) Enqueue(msg OutputMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.messages = append(q.messages, msg)
	q.cond.Signal()
}

func (q *outputQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

func (q *outputQueue) Pop() (OutputMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.messages) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.messages) == 0 {
		return OutputMessage{}, false
	}
	msg := q.messages[0]
	copy(q.messages, q.messages[1:])
	q.messages[len(q.messages)-1] = OutputMessage{}
	q.messages = q.messages[:len(q.messages)-1]
	return msg, true
}

func resultFinalMessage(cmd Command, res executor.Result, err error) FinalMessage {
	final := FinalMessage{
		RequestID: cmd.RequestID,
		Script:    res.Script,
		Done:      true,
	}
	if final.Script == "" {
		final.Script = cmd.Script
	}
	if err != nil {
		final.Error = executor.StableError(err)
	}
	if includeRunFields(err, res) {
		exitCode := res.ExitCode
		timedOut := res.TimedOut
		final.ExitCode = &exitCode
		final.TimedOut = &timedOut
		final.StartedAt = &res.StartedAt
		final.FinishedAt = &res.FinishedAt
		final.DurationMs = res.FinishedAt.Sub(res.StartedAt).Milliseconds()
	}
	return final
}

func includeRunFields(err error, res executor.Result) bool {
	if res.StartedAt.IsZero() || res.FinishedAt.IsZero() {
		return false
	}
	return !errors.Is(err, executor.ErrRunnerStart)
}

func (h *Handler) publishFinal(ctx context.Context, topic string, msg FinalMessage) error {
	return h.publishJSON(ctx, topic, msg)
}

func (h *Handler) publishJSON(ctx context.Context, topic string, msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal MQTT reply: %w", err)
	}
	if err := h.pub.Publish(ctx, topic, body); err != nil {
		return fmt.Errorf("publish MQTT reply to %q: %w", topic, err)
	}
	return nil
}

type PahoPublisher struct {
	client paho.Client
	qos    byte
}

func NewPahoPublisher(client paho.Client, qos byte) *PahoPublisher {
	return &PahoPublisher{client: client, qos: qos}
}

func (p *PahoPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	token := p.client.Publish(topic, p.qos, false, payload)
	select {
	case <-token.Done():
		if err := token.Error(); err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ Publisher = (*PahoPublisher)(nil)
