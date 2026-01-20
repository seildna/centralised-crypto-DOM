package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dom/internal/config"
	"dom/internal/runner"
)

func main() {
	cfg := config.Load()
	log.Printf("starting dom runner: symbols=%v exchanges=%v dryRun=%v", cfg.Symbols, cfg.Exchanges, cfg.DryRun)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	r := runner.New(cfg)
	go func() {
		if err := r.Start(ctx); err != nil {
			log.Printf("runner stopped: %v", err)
		}
	}()

	<-sigCh
	log.Printf("shutdown requested")
	cancel()
	time.Sleep(200 * time.Millisecond)
}
