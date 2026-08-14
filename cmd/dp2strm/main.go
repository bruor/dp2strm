package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "time/tzdata"

	"dp2strm/pkg/client"
	"dp2strm/pkg/config"
	"dp2strm/pkg/db"
	"dp2strm/pkg/proxy"
	"dp2strm/pkg/syncer"
)

func main() {
	syncOnce := flag.Bool("sync-once", false, "Execute a single sync pass and exit")
	dryRun := flag.Bool("dry-run", false, "Perform dry-run (log actions without modifying disk)")
	serveProxy := flag.Bool("serve", false, "Run HTTP playback proxy server in background")
	batchSize := flag.Int("batch-size", 0, "Limit maximum number of items to sync (0 = unlimited)")
	flag.Parse()
	_ = serveProxy

	log.Println("==================================================")
	log.Println("             dp2strm Daemon (Go Edition)          ")
	log.Println("==================================================")

	cfg := config.LoadConfig()

	if *batchSize > 0 {
		cfg.BatchSize = *batchSize
	}

	if cfg.APIKey == "" {
		log.Println("[Warning] No API key specified in DISPATCHARR_API_KEY environment variable.")
	}

	// 1. Open SQLite database engine (/data/strm/dp2strm.db)
	dbPath := filepath.Join(cfg.OutputDir, "dp2strm.db")
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("[DB] Fatal: Failed to initialize SQLite database: %v", err)
	}
	defer database.Close()
	log.Printf("[DB] SQLite database online at: %s", dbPath)

	restClient := client.NewClient(cfg.DispatcharrURL, cfg.APIKey)
	syncEngine := syncer.NewSyncer(cfg, restClient, database)

	// Async Series Activation Callback Worker
	onActivation := func(seriesID int) {
		log.Printf("[Activation] Async worker activated for Series ID %d...", seriesID)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		cleanTitle := fmt.Sprintf("Series %d", seriesID)
		seriesDir := filepath.Join(cfg.OutputDir, "TV Shows", cleanTitle)
		_, _, err := syncEngine.PopulateActivatedSeries(ctx, seriesID, cleanTitle, seriesDir)
		if err != nil {
			log.Printf("[Activation] Error populating series ID %d: %v", seriesID, err)
			return
		}
		syncEngine.TriggerTargetedEmbySeriesRefresh(ctx, seriesID)
	}

	// 2. Start HTTP Playback Handler Server
	proxyServer := proxy.NewServer(cfg, database, onActivation)
	proxyServer.Start()

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
		_ = proxyServer.Stop(ctx)
		log.Println("[Main] Single sync pass completed. Exiting.")
		return
	}

	// 3. Daemon Mode Execution
	go func() {
		<-sigChan
		log.Println("[Main] Shutdown signal received. Stopping dp2strm...")
		_ = proxyServer.Stop(ctx)
		cancel()
	}()

	log.Printf("[Main] Daemon active. Timezone: %s", cfg.TZ)

	// Execute Initial Startup Sync Pass
	if err := syncEngine.SyncMovies(ctx, *dryRun); err != nil {
		log.Printf("[Main] Error during initial movie sync: %v", err)
	}
	if err := syncEngine.SyncSeries(ctx, *dryRun); err != nil {
		log.Printf("[Main] Error during initial series sync: %v", err)
	}

	// Determine Ticker Interval (Default 1h if interval is 0 or unspecified in daemon mode)
	syncInterval := cfg.SyncInterval
	if syncInterval <= 0 {
		syncInterval = 1 * time.Hour
	}

	log.Printf("[Main] Scheduled background sync ticker active every: %v", syncInterval)
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[Main] Context cancelled. Exiting daemon.")
			return
		case <-ticker.C:
			log.Println("[Main] Ticker fired. Executing scheduled sync pass...")
			if err := syncEngine.SyncMovies(ctx, *dryRun); err != nil {
				log.Printf("[Main] Error during scheduled movie sync: %v", err)
			}
			if err := syncEngine.SyncSeries(ctx, *dryRun); err != nil {
				log.Printf("[Main] Error during scheduled series sync: %v", err)
			}
		}
	}
}
