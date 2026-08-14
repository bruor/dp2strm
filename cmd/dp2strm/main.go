package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dp2strm/pkg/client"
	"dp2strm/pkg/config"
	"dp2strm/pkg/proxy"
	"dp2strm/pkg/syncer"
)

func main() {
	syncOnce := flag.Bool("sync-once", true, "Execute a single sync pass and exit")
	dryRun := flag.Bool("dry-run", false, "Perform dry-run (log actions without modifying disk)")
	serveProxy := flag.Bool("serve", false, "Run HTTP playback proxy server in background")
	batchSize := flag.Int("batch-size", 0, "Limit maximum number of items to sync (0 = unlimited)")
	flag.Parse()

	log.Println("==================================================")
	log.Println("             dp2strm Daemon (Go Edition)          ")
	log.Println("==================================================")

	cfg := config.LoadConfig()

	if *batchSize > 0 {
		cfg.BatchSize = *batchSize
	}

	if cfg.APIKey == "" {
		log.Println("Warning: No API key specified in configuration or DISPATCHARR_API_KEY env.")
	}

	restClient := client.NewClient(cfg.DispatcharrURL, cfg.APIKey)
	syncEngine := syncer.NewSyncer(cfg, restClient)

	// Start HTTP Playback Proxy Server if enabled or serving mode
	var proxyServer *proxy.Server
	if *serveProxy || cfg.ProxyPort > 0 {
		proxyServer = proxy.NewServer(cfg)
		proxyServer.Start()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle OS termination signals cleanly
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if *syncOnce {
		log.Println("[Main] Executing single sync pass...")
		if err := syncEngine.SyncMovies(ctx, *dryRun); err != nil {
			log.Printf("[Main] Error during movie sync: %v", err)
		}
		if err := syncEngine.SyncSeries(ctx, *dryRun); err != nil {
			log.Printf("[Main] Error during series sync: %v", err)
		}
		if proxyServer != nil && !*serveProxy {
			_ = proxyServer.Stop(ctx)
		}
		log.Println("[Main] Sync completed. Exiting.")
		return
	}

	// Daemon Mode with Ticker
	go func() {
		<-sigChan
		log.Println("[Main] Shutdown signal received. Stopping dp2strm...")
		if proxyServer != nil {
			_ = proxyServer.Stop(ctx)
		}
		cancel()
	}()

	log.Printf("[Main] Daemon mode active. Sync interval: %v", cfg.SyncInterval)
	if err := syncEngine.SyncMovies(ctx, *dryRun); err != nil {
		log.Printf("[Main] Error during initial sync: %v", err)
	}

	ticker := time.NewTicker(cfg.SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[Main] Context cancelled. Exiting daemon.")
			return
		case <-ticker.C:
			log.Println("[Main] Ticker fired. Starting scheduled movie sync...")
			if err := syncEngine.SyncMovies(ctx, *dryRun); err != nil {
				log.Printf("[Main] Error during scheduled sync: %v", err)
			}
		}
	}
}
