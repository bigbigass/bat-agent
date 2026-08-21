package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/liqixin/deploy-agent/internal/downloadtask"
	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/releasecatalog"
)

type Server struct {
	exec      *executor.Executor
	releases  ReleaseCatalog
	downloads DownloadService
}

// ReleaseCatalog and DownloadService are deliberately small HTTP-facing
// contracts; concrete catalog/task implementations are injected by main.
type ReleaseCatalog interface {
	Manifest() (*releasecatalog.Manifest, error)
	Refresh(context.Context) error
}
type DownloadService interface {
	Start(version, resourceID string) (downloadtask.Info, error)
	Get(taskID string) (downloadtask.Info, error)
	Cancel(taskID string) error
}
type DownloadRequest struct {
	Version    string `json:"version"`
	ResourceID string `json:"resourceId"`
}

// DownloadInfo is kept as an HTTP-package alias so callers that used the
// earlier handler DTO continue to compile while receiving the new fields.
type DownloadInfo = downloadtask.Info

var (
	ErrReleaseUnavailable  = errors.New("release service unavailable")
	ErrDownloadUnavailable = errors.New("download service unavailable")
	ErrDownloadNotFound    = downloadtask.ErrNotFound
	ErrDownloadConflict    = downloadtask.ErrConflict
)

func New(exec *executor.Executor, services ...any) *Server {
	s := &Server{exec: exec}
	for _, v := range services {
		if x, ok := v.(ReleaseCatalog); ok {
			s.releases = x
		}
		if x, ok := v.(DownloadService); ok {
			s.downloads = x
		}
	}
	return s
}

func (s *Server) Routes(authWrap func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.Handle("/scripts", authWrap(http.HandlerFunc(s.handleScripts)))
	mux.Handle("/run", authWrap(http.HandlerFunc(s.handleRun)))
	mux.Handle("/run/stream", authWrap(http.HandlerFunc(s.handleRunStream)))
	mux.Handle("/releases", authWrap(http.HandlerFunc(s.handleReleases)))
	mux.Handle("/releases/refresh", authWrap(http.HandlerFunc(s.handleReleaseRefresh)))
	mux.Handle("/downloads", authWrap(http.HandlerFunc(s.handleDownloads)))
	mux.Handle("/downloads/", authWrap(http.HandlerFunc(s.handleDownloadPath)))
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
	writeJSON(w, http.StatusOK, map[string]any{"scripts": s.exec.List()})
}

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.releases == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": ErrReleaseUnavailable.Error()})
		return
	}
	v, err := s.releases.Manifest()
	if err != nil {
		message := err.Error()
		if errors.Is(err, releasecatalog.ErrUnavailable) {
			message = ErrReleaseUnavailable.Error()
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": message})
		return
	}
	if v == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": ErrReleaseUnavailable.Error()})
		return
	}
	writeJSON(w, http.StatusOK, publicManifest(v))
}
func (s *Server) handleReleaseRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.releases == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": ErrReleaseUnavailable.Error()})
		return
	}
	defer r.Body.Close()
	if err := s.releases.Refresh(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	v, err := s.releases.Manifest()
	if err != nil {
		message := err.Error()
		if errors.Is(err, releasecatalog.ErrUnavailable) {
			message = ErrReleaseUnavailable.Error()
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": message})
		return
	}
	if v == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": ErrReleaseUnavailable.Error()})
		return
	}
	writeJSON(w, http.StatusOK, publicManifest(v))
}
func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.downloads == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": ErrDownloadUnavailable.Error()})
		return
	}
	defer r.Body.Close()
	var req DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Version) == "" || strings.TrimSpace(req.ResourceID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version and resourceId are required"})
		return
	}
	if req.Version == "." || req.Version == ".." || strings.ContainsAny(req.Version, `/\\:`) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid version"})
		return
	}
	v, err := s.downloads.Start(req.Version, req.ResourceID)
	if err != nil {
		s.writeDownloadError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}
func (s *Server) handleDownloadPath(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/downloads/"), "/")
	parts := strings.Split(path, "/")
	if path == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "cancel") {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if s.downloads == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": ErrDownloadUnavailable.Error()})
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		v, err := s.downloads.Get(id)
		if err != nil {
			s.writeDownloadError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	case len(parts) == 2 && r.Method == http.MethodPost:
		if err := s.downloads.Cancel(id); err != nil {
			s.writeDownloadError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancellation-requested", "taskId": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
func (s *Server) writeDownloadError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, downloadtask.ErrInvalidRequest) {
		code = http.StatusBadRequest
	}
	if errors.Is(err, downloadtask.ErrNotFound) || errors.Is(err, releasecatalog.ErrReleaseNotFound) || errors.Is(err, releasecatalog.ErrResourceNotFound) {
		code = http.StatusNotFound
	}
	if errors.Is(err, downloadtask.ErrConflict) {
		code = http.StatusConflict
	}
	if errors.Is(err, ErrDownloadUnavailable) || errors.Is(err, releasecatalog.ErrUnavailable) {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func publicManifest(manifest *releasecatalog.Manifest) *releasecatalog.Manifest {
	if manifest == nil {
		return nil
	}
	result := *manifest
	result.Releases = make([]releasecatalog.Release, len(manifest.Releases))
	for i := range manifest.Releases {
		result.Releases[i] = manifest.Releases[i]
		result.Releases[i].Resources = append([]releasecatalog.Resource(nil), manifest.Releases[i].Resources...)
		for j := range result.Releases[i].Resources {
			result.Releases[i].Resources[j].URL = ""
		}
	}
	return &result
}

type runRequest struct {
	Script      string              `json:"script"`
	Args        []string            `json:"args,omitempty"`
	PreDownload *preDownloadRequest `json:"preDownload,omitempty"`
}

type preDownloadRequest struct {
	Enabled  bool   `json:"enabled"`
	Project  string `json:"project"`
	Artifact string `json:"artifact"`
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

type streamOutputResponse struct {
	Type   string `json:"type"`
	Script string `json:"script"`
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type streamFinalResponse struct {
	Type       string    `json:"type"`
	Script     string    `json:"script"`
	ExitCode   int       `json:"exitCode"`
	TimedOut   bool      `json:"timedOut"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	DurationMs int64     `json:"durationMs"`
	Error      string    `json:"error,omitempty"`
}

type streamRunResult struct {
	result executor.Result
	err    error
}

func runOptionsFromRequest(req runRequest) executor.RunOptions {
	opts := executor.RunOptions{Args: req.Args}
	if req.PreDownload == nil {
		return opts
	}
	opts.PreDownload = executor.PreDownloadRequest{
		Enabled:  req.PreDownload.Enabled,
		Project:  req.PreDownload.Project,
		Artifact: req.PreDownload.Artifact,
	}
	return opts
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
	result, err := s.exec.RunCollectWithOptions(context.Background(), req.Script, runOptionsFromRequest(req))
	if err != nil {
		switch {
		case errors.Is(err, executor.ErrInvalidScriptName):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": executor.StableError(err)})
			return
		case errors.Is(err, executor.ErrInvalidScriptArg):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": executor.StableError(err)})
			return
		case errors.Is(err, executor.ErrInvalidPreDownloadRequest):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": executor.StableError(err)})
			return
		case errors.Is(err, executor.ErrScriptNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": executor.StableError(err)})
			return
		case errors.Is(err, executor.ErrScriptBusy):
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  executor.StableError(err),
				"script": result.Script,
			})
			return
		}
	}

	resp := runResponse{
		Script:     result.Script,
		ExitCode:   result.ExitCode,
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		StartedAt:  result.StartedAt,
		FinishedAt: result.FinishedAt,
		DurationMs: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
		TimedOut:   result.TimedOut,
	}
	if err != nil {
		resp.Error = executor.StableError(err)
	}

	status := http.StatusOK
	switch {
	case result.TimedOut:
		status = http.StatusGatewayTimeout
	case err != nil:
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, resp)
}

func (s *Server) handleRunStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	outputs := make(chan streamOutputResponse, 64)
	done := make(chan streamRunResult, 1)
	go func() {
		result, err := s.exec.RunStreamWithOptions(context.Background(), req.Script, runOptionsFromRequest(req), func(chunk executor.OutputChunk) {
			outputs <- streamOutputResponse{
				Type:   "output",
				Script: req.Script,
				Stream: chunk.Stream,
				Data:   chunk.Data,
			}
		})
		done <- streamRunResult{result: result, err: err}
		close(outputs)
	}()

	var enc *json.Encoder
	streaming := false
	startStream := func() {
		if streaming {
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		enc = json.NewEncoder(w)
		streaming = true
	}

	for {
		select {
		case out, ok := <-outputs:
			if !ok {
				outputs = nil
				continue
			}
			startStream()
			if err := enc.Encode(out); err != nil {
				go drainStream(outputs, done)
				return
			}
			flush(w)
		case run := <-done:
			if !streaming && writePreflightStreamError(w, run.result, run.err) {
				return
			}
			startStream()
			if !writeStreamOutputs(w, enc, outputs) {
				return
			}
			_ = enc.Encode(streamFinalFromResult(run.result, run.err))
			flush(w)
			return
		}
	}
}

func drainStream(outputs <-chan streamOutputResponse, done <-chan streamRunResult) {
	for outputs != nil || done != nil {
		select {
		case _, ok := <-outputs:
			if !ok {
				outputs = nil
			}
		case <-done:
			done = nil
		}
	}
}

func drainOutputs(outputs <-chan streamOutputResponse) {
	for range outputs {
	}
}

func writeStreamOutputs(w http.ResponseWriter, enc *json.Encoder, outputs <-chan streamOutputResponse) bool {
	if outputs == nil {
		return true
	}
	for out := range outputs {
		if err := enc.Encode(out); err != nil {
			go drainOutputs(outputs)
			return false
		}
		flush(w)
	}
	return true
}

func writePreflightStreamError(w http.ResponseWriter, result executor.Result, err error) bool {
	switch {
	case errors.Is(err, executor.ErrInvalidScriptName):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": executor.StableError(err)})
		return true
	case errors.Is(err, executor.ErrInvalidScriptArg):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": executor.StableError(err)})
		return true
	case errors.Is(err, executor.ErrInvalidPreDownloadRequest):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": executor.StableError(err)})
		return true
	case errors.Is(err, executor.ErrScriptNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": executor.StableError(err)})
		return true
	case errors.Is(err, executor.ErrScriptBusy):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  executor.StableError(err),
			"script": result.Script,
		})
		return true
	default:
		return false
	}
}

func streamFinalFromResult(result executor.Result, err error) streamFinalResponse {
	resp := streamFinalResponse{
		Type:       "final",
		Script:     result.Script,
		ExitCode:   result.ExitCode,
		TimedOut:   result.TimedOut,
		StartedAt:  result.StartedAt,
		FinishedAt: result.FinishedAt,
		DurationMs: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
	}
	if err != nil {
		resp.Error = executor.StableError(err)
	}
	return resp
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
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

func (s *statusWriter) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
