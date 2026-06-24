package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/liqixin/deploy-agent/internal/appservice"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		fmt.Fprintln(os.Stderr, "\nPress Enter to exit...")
		_, _ = fmt.Scanln()
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := appservice.New(appservice.Options{})
	if err := svc.Start(ctx); err != nil {
		return err
	}
	if err := svc.Wait(ctx); err != nil {
		return err
	}
	log.Printf("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return svc.Shutdown(shutdownCtx)
}
