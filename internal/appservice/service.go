package appservice

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liqixin/deploy-agent/internal/auth"
	"github.com/liqixin/deploy-agent/internal/config"
	"github.com/liqixin/deploy-agent/internal/downloadtask"
	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/httpapi"
	"github.com/liqixin/deploy-agent/internal/mqttapi"
	"github.com/liqixin/deploy-agent/internal/registry"
	"github.com/liqixin/deploy-agent/internal/releasecatalog"
)

var ErrAlreadyStarted = errors.New("app service already started")

var listenTCP = net.Listen

type Options struct {
	ConfigPath string
}

type HTTPClientConfig struct {
	BaseURL  string
	Username string
	Password string
}

type Service struct {
	options     Options
	mutex       sync.Mutex
	started     bool
	cfg         *config.Config
	cfgPath     string
	cancel      context.CancelFunc
	srv         *http.Server
	errCh       chan error
	done        chan struct{}
	doneOnce    sync.Once
	httpBaseURL string
}

func New(options Options) *Service {
	return &Service{options: options}
}

func (s *Service) Start(parent context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.started {
		return ErrAlreadyStarted
	}

	cfgPath := s.options.ConfigPath
	if cfgPath == "" {
		var err error
		cfgPath, err = FindConfig()
		if err != nil {
			s.resetLocked()
			return err
		}
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		s.resetLocked()
		return err
	}
	log.Printf("loaded config from %s", cfgPath)

	scriptDir, err := cfg.ResolveScriptDir()
	if err != nil {
		s.resetLocked()
		return fmt.Errorf("resolve scriptDir: %w", err)
	}

	reg, err := registry.New(scriptDir)
	if err != nil {
		s.resetLocked()
		return err
	}
	log.Printf("script dir: %s", reg.Dir())
	log.Printf("discovered %d script(s): %v", len(reg.List()), reg.List())

	ctx, cancel := context.WithCancel(parent)
	errCh := make(chan error, 1)
	done := make(chan struct{})
	s.started = true
	s.cfg = cfg
	s.cfgPath = cfgPath
	s.cancel = cancel
	s.errCh = errCh
	s.done = done
	s.doneOnce = sync.Once{}
	s.httpBaseURL = ""

	fail := func(err error) error {
		cancel()
		s.closeDoneLocked()
		s.resetLocked()
		return err
	}

	go reg.WatchRescan(ctx, 60*time.Second)

	timeout := time.Duration(cfg.Runner.TimeoutSeconds) * time.Second
	execOptions, err := executorOptionsFromConfig(cfg, cfgPath)
	if err != nil {
		return fail(err)
	}
	exec := executor.New(reg, timeout, execOptions...)

	if cfg.Services.MQTT.Enabled {
		mqttClient := mqttapi.NewClient(mqttConfigFromConfig(cfg), exec)
		if err := mqttClient.Start(ctx); err != nil {
			return fail(fmt.Errorf("start mqtt: %w", err))
		}
	}

	if cfg.Services.HTTP.Enabled {
		api := httpapi.New(exec)
		if cfg.Release.Enabled && strings.TrimSpace(cfg.Release.ManifestURL) != "" {
			catalog, downloads, err := releaseServicesFromConfig(ctx, cfg, cfgPath, &http.Client{Timeout: 10 * time.Second}, &http.Client{})
			if err != nil {
				return fail(err)
			}
			api = httpapi.New(exec, catalog, downloads)
		}
		authWrap := func(h http.Handler) http.Handler {
			return auth.BasicAuth(cfg.Auth.Username, cfg.Auth.Password, h)
		}
		addr := httpListenAddress(cfg)
		ln, err := listenTCP("tcp", addr)
		if err != nil {
			return fail(fmt.Errorf("listen http %s: %w", addr, err))
		}
		srv := &http.Server{
			Addr:              addr,
			Handler:           api.Routes(authWrap),
			ReadHeaderTimeout: 10 * time.Second,
		}
		s.srv = srv
		s.httpBaseURL = HTTPBaseURLForConfig(cfg)
		go func() {
			log.Printf("deploy-agent http listening on %s (timeout %s)", addr, timeout)
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				select {
				case errCh <- err:
				default:
				}
			}
		}()
	}

	return nil
}

func releaseServicesFromConfig(ctx context.Context, cfg *config.Config, cfgPath string, manifestClient, downloadClient *http.Client) (*releasecatalog.Catalog, *downloadtask.Manager, error) {
	downloadDir, err := cfg.ResolveReleaseDownloadDir(cfgPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve release.downloadDir: %w", err)
	}
	catalog, err := releasecatalog.New(cfg.Release.ManifestURL, manifestClient)
	if err != nil {
		return nil, nil, fmt.Errorf("create release catalog: %w", err)
	}
	downloads, err := downloadtask.New(ctx, downloadDir, catalog, downloadClient)
	if err != nil {
		return nil, nil, fmt.Errorf("create release download manager: %w", err)
	}

	// Keep startup bounded: an unavailable release host must not prevent the
	// script service from becoming ready. A later manual or scheduled refresh
	// can populate the catalog.
	refreshCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	refreshErr := catalog.Refresh(refreshCtx)
	cancel()
	if refreshErr != nil {
		log.Printf("initial release manifest refresh failed; downloads remain unavailable until a refresh succeeds: %v", refreshErr)
	}
	go refreshReleaseCatalog(ctx, catalog, time.Duration(cfg.Release.RefreshSeconds)*time.Second)
	return catalog, downloads, nil
}

func refreshReleaseCatalog(ctx context.Context, catalog *releasecatalog.Catalog, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := catalog.Refresh(refreshCtx)
			cancel()
			if err != nil {
				log.Printf("release manifest refresh failed; keeping cached manifest: %v", err)
			}
		}
	}
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.mutex.Lock()
	cancel := s.cancel
	srv := s.srv
	if cancel != nil {
		cancel()
	}
	s.closeDoneLocked()
	s.started = false
	s.cancel = nil
	s.srv = nil
	s.errCh = nil
	s.httpBaseURL = ""
	s.mutex.Unlock()

	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

func (s *Service) Wait(ctx context.Context) error {
	s.mutex.Lock()
	errCh := s.errCh
	done := s.done
	s.mutex.Unlock()

	if errCh == nil && done == nil {
		<-ctx.Done()
		return nil
	}

	select {
	case <-ctx.Done():
		return nil
	case <-done:
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Service) HTTPBaseURL() string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.httpBaseURL
}

func (s *Service) HTTPClientConfig() (HTTPClientConfig, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.cfg == nil || !s.cfg.Services.HTTP.Enabled || s.httpBaseURL == "" {
		return HTTPClientConfig{}, false
	}
	return HTTPClientConfig{
		BaseURL:  s.httpBaseURL,
		Username: s.cfg.Auth.Username,
		Password: s.cfg.Auth.Password,
	}, true
}

func (s *Service) resetLocked() {
	s.started = false
	s.cfg = nil
	s.cfgPath = ""
	s.cancel = nil
	s.srv = nil
	s.errCh = nil
	s.done = nil
	s.doneOnce = sync.Once{}
	s.httpBaseURL = ""
}

func (s *Service) closeDoneLocked() {
	closeDone(&s.doneOnce, s.done)
}

func closeDone(doneOnce *sync.Once, done chan struct{}) {
	if done == nil {
		return
	}
	doneOnce.Do(func() {
		close(done)
	})
}

func httpListenAddress(cfg *config.Config) string {
	host := trimHostBrackets(strings.TrimSpace(cfg.Server.Host))
	return net.JoinHostPort(host, strconv.Itoa(cfg.Server.Port))
}

func HTTPBaseURLForConfig(cfg *config.Config) string {
	host := localConnectHost(cfg.Server.Host)
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Server.Port))
}

func localConnectHost(host string) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return trimHostBrackets(host)
	}
}

func trimHostBrackets(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	return host
}

func mqttConfigFromConfig(cfg *config.Config) mqttapi.Config {
	return mqttapi.Config{
		Broker:       cfg.Services.MQTT.Broker,
		ClientID:     cfg.Services.MQTT.ClientID,
		Username:     cfg.Services.MQTT.Username,
		Password:     cfg.Services.MQTT.Password,
		CommandTopic: cfg.Services.MQTT.CommandTopic,
		QoS:          byte(cfg.Services.MQTT.QoS),
	}
}

func executorOptionsFromConfig(cfg *config.Config, cfgPath string) ([]executor.Option, error) {
	downloadScript, err := cfg.ResolvePreRunDownloadScript(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("resolve preRun.download.script: %w", err)
	}
	if downloadScript == "" {
		defaultScript := filepath.Join(filepath.Dir(cfgPath), "tools", "download_simple.bat")
		if _, err := os.Stat(defaultScript); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("stat default preRun.download.script: %w", err)
		}
		downloadScript = defaultScript
	}
	timeoutSeconds := cfg.PreRun.Download.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 300
	}
	return []executor.Option{
		executor.WithPreDownloadConfig(executor.PreDownloadConfig{
			ScriptPath: downloadScript,
			Timeout:    time.Duration(timeoutSeconds) * time.Second,
		}),
	}, nil
}

func FindConfig() (string, error) {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "config.yaml"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "config.yaml"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("config.yaml not found (looked in: %v). Copy config.example.yaml to config.yaml and edit it", candidates)
}
