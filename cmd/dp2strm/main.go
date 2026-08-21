package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "time/tzdata"

	"github.com/robfig/cron/v3"

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

	proxy.InitGlobalLogger()

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

	// 1. Open SQLite database engine (/data/db/dp2strm.db)
	dbPath := "/data/db/dp2strm.db"
	if _, err := os.Stat("/data/db"); os.IsNotExist(err) {
		dbPath = filepath.Join(cfg.OutputDir, "dp2strm.db")
	}
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("[DB] Fatal: Failed to initialize SQLite database: %v", err)
	}
	defer database.Close()
	log.Printf("[DB] SQLite database online at: %s", dbPath)

	restClient := client.NewClient(cfg.DispatcharrURL, cfg.APIKey, cfg.Ordering)

	// Auto-discover XC streaming credentials from API key if unset or using defaults
	if (cfg.XCUser == "" || cfg.XCUser == "username" || cfg.XCPassword == "" || cfg.XCPassword == "password") && cfg.APIKey != "" {
		pCtx, pCancel := context.WithTimeout(context.Background(), 10*time.Second)
		discUser, discPass, err := restClient.FetchMyProfile(pCtx)
		pCancel()
		if err == nil && discUser != "" && discPass != "" {
			cfg.XCUser = discUser
			cfg.XCPassword = discPass
			log.Printf("[Config] Auto-discovered XC streaming credentials for user %q from Dispatcharr API.", cfg.XCUser)
		} else if err != nil {
			log.Printf("[Config] Warning: Could not auto-discover XC credentials: %v", err)
		}
	}

	syncEngine := syncer.NewSyncer(cfg, restClient, database)

	// Async Series Activation Callback Worker (auto-resolves canonical title & folder)
	onActivation := func(seriesID int) {
		log.Printf("[Activation] Async worker activated for Series ID %d...", seriesID)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		// Acquire VIP activation lock to halt any running global scan and pause background batch workers
		unlock := syncEngine.BeginVIPActivation(ctx, seriesID)
		defer unlock()

		_, _, err := syncEngine.PopulateActivatedSeries(ctx, seriesID, "", "")
		if err != nil {
			log.Printf("[Activation] Error populating series ID %d: %v", seriesID, err)
			return
		}
		syncEngine.TriggerTargetedEmbySeriesRefresh(ctx, seriesID)
		time.Sleep(2 * time.Second) // Brief settle window for Emby to index series
	}

	// Movie Activation Callback Worker (auto-resolves canonical title & folder)
	onMovieActivation := func(movieID int) (string, error) {
		log.Printf("[Activation] User played Movie ID %d! Resolving provider streams and quality variants...", movieID)
		mCtx, mCancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer mCancel()

		// Cancel any running global library scan so Emby is immediately free to process this VIP activation
		_ = syncEngine.CancelRunningEmbyScan(mCtx)

		_, _, baseTargetURL, err := syncEngine.PopulateActivatedMovie(mCtx, movieID, "", "", 0, "")
		if err != nil {
			log.Printf("[Activation] Error resolving movie ID %d: %v", movieID, err)
			return "", err
		}
		return baseTargetURL, nil
	}

	// 2. Start HTTP Playback Handler Server
	proxyServer := proxy.NewServer(cfg, database, onActivation)
	proxyServer.SetMovieActivationCallback(onMovieActivation)
	proxyServer.SetSyncCallback(func() error {
		return syncEngine.StageCatalogDelta(context.Background())
	})
	proxyServer.Start()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle OS termination signals cleanly
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if *syncOnce {
		log.Println("[Main] Executing single sync pass...")
		if err := syncEngine.SyncSharedPass(ctx, *dryRun); err != nil {
			log.Printf("[Main] Error during sync pass: %v", err)
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

	// Load IANA Timezone
	loc, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		log.Printf("[Main] Warning: Could not load timezone %q (%v). Defaulting to UTC.", cfg.TZ, err)
		loc = time.UTC
	}
	log.Printf("[Main] Daemon active. Timezone: %s", loc.String())

	// Start background Emby workers and Reconciler
	syncEngine.StartWorkers(ctx)
	defer syncEngine.StopWorkers()

	// Execute Initial Startup Staging Pass (unless SKIP_STARTUP_SYNC=true)
	if !cfg.SkipStartupSync {
		log.Println("[Main] Executing initial startup catalog staging pass...")
		if err := syncEngine.StageCatalogDelta(ctx); err != nil {
			log.Printf("[Main] Error during initial staging pass: %v", err)
		}
	} else {
		log.Println("[Main] SKIP_STARTUP_SYNC enabled. Skipping initial catalog staging pass.")
	}

	// Setup Cron Scheduler or Duration Ticker
	if cfg.SyncCron != "" {
		cronRunner := cron.New(cron.WithLocation(loc))
		_, err := cronRunner.AddFunc(cfg.SyncCron, func() {
			log.Printf("[Main] Cron schedule '%s' triggered (Time: %s). Executing catalog staging pass...", cfg.SyncCron, time.Now().In(loc).Format(time.RFC3339))
			if err := syncEngine.StageCatalogDelta(ctx); err != nil {
				log.Printf("[Main] Error during scheduled staging pass: %v", err)
			}
		})
		if err == nil {
			cronRunner.Start()
			defer cronRunner.Stop()
			log.Printf("[Main] Scheduled cron active: %q (Timezone: %s)", cfg.SyncCron, loc.String())

			<-ctx.Done()
			log.Println("[Main] Context cancelled. Exiting daemon.")
			return
		}
		log.Printf("[Main] Warning: Failed to parse SYNC_CRON %q (%v). Falling back to interval ticker.", cfg.SyncCron, err)
	}

	// Fallback to Ticker Interval (Default 4h)
	syncInterval := cfg.SyncInterval
	if syncInterval <= 0 {
		syncInterval = 4 * time.Hour
	}

	log.Printf("[Main] Scheduled catalog staging ticker active every: %v", syncInterval)
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[Main] Context cancelled. Exiting daemon.")
			return
		case <-ticker.C:
			log.Println("[Main] Ticker fired. Executing scheduled catalog staging pass...")
			if err := syncEngine.StageCatalogDelta(ctx); err != nil {
				log.Printf("[Main] Error during scheduled staging pass: %v", err)
			}
		}
	}
}
