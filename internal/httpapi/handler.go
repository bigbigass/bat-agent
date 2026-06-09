package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/liqixin/deploy-agent/internal/registry"
	"github.com/liqixin/deploy-agent/internal/runner"
)

type Server struct {
	reg     *registry.Registry
	timeout time.Duration
}

func New(reg *registry.Registry, timeout time.Duration) *Server {
	return &Server{reg: reg, timeout: timeout}
}

func (s *Server) Routes(authWrap func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.Handle("/scripts", authWrap(http.HandlerFunc(s.handleScripts)))
	mux.Handle("/run", authWrap(http.HandlerFunc(s.handleRun)))
	return accessLog(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleScripts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scripts": s.reg.List()})
}

type runRequest struct {
	Script string `json:"script"`
}

type runResponse struct {
	Script     string    `json:"script"`
	ExitCode   int       `json:"exitCode"`
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	DurationMs int64     `json:"durationMs"`
	TimedOut   bool      `json:"timedOut,omitempty"`
	Error      string    `json:"error,omitempty"`
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	entry, err := s.reg.Lookup(req.Script)
	if err != nil {
		if errors.Is(err, registry.ErrInvalid) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid script name"})
			return
		}
		if errors.Is(err, registry.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "script not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !entry.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "script is already running",
			"script": entry.Name,
		})
		return
	}
	defer entry.Unlock()

	result, err := runner.Run(context.Background(), entry.Path, s.timeout)
	resp := runResponse{
		Script:     entry.Name,
		ExitCode:   result.ExitCode,
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		StartedAt:  result.StartedAt,
		FinishedAt: result.FinishedAt,
		DurationMs: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
		TimedOut:   result.TimedOut,
	}

	status := http.StatusOK
	switch {
	case result.TimedOut:
		status = http.StatusGatewayTimeout
	case err != nil:
		status = http.StatusInternalServerError
		resp.Error = err.Error()
	}
	writeJSON(w, status, resp)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %s %d %s",
			r.RemoteAddr, r.Method, r.URL.Path, sw.status,
			time.Since(start).Round(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
