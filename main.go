package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/liqixin/deploy-agent/internal/auth"
	"github.com/liqixin/deploy-agent/internal/config"
	"github.com/liqixin/deploy-agent/internal/httpapi"
	"github.com/liqixin/deploy-agent/internal/registry"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		// Keep the console open when double-clicked so users can read the error.
		fmt.Fprintln(os.Stderr, "\nPress Enter to exit...")
		_, _ = fmt.Scanln()
		os.Exit(1)
	}
}

func run() error {
	cfgPath, err := findConfig()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	log.Printf("loaded config from %s", cfgPath)

	scriptDir, err := cfg.ResolveScriptDir()
	if err != nil {
		return fmt.Errorf("resolve scriptDir: %w", err)
	}

	reg, err := registry.New(scriptDir)
	if err != nil {
		return err
	}
	log.Printf("script dir: %s", reg.Dir())
	log.Printf("discovered %d script(s): %v", len(reg.List()), reg.List())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go reg.WatchRescan(ctx, 60*time.Second)

	timeout := time.Duration(cfg.Runner.TimeoutSeconds) * time.Second
	api := httpapi.New(reg, timeout)
	authWrap := func(h http.Handler) http.Handler {
		return auth.BasicAuth(cfg.Auth.Username, cfg.Auth.Password, h)
	}

	addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.Routes(authWrap),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("deploy-agent listening on %s (timeout %s)", addr, timeout)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		log.Printf("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// findConfig looks for config.yaml next to the executable, then in the
// current working directory.
func findConfig() (string, error) {
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
