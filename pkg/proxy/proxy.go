package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"dp2strm/pkg/assets"
	"dp2strm/pkg/db"
	"dp2strm/pkg/models"
)

// ActivationCallback is a function signature for background series activation processing.
type ActivationCallback func(seriesID int)

// MovieActivationCallback is a function signature for background movie activation and provider resolution.
type MovieActivationCallback func(movieID int) (baseTargetURL string, err error)

// SyncCallback is a function signature for triggering an on-demand catalog staging pass.
type SyncCallback func() error

// Server is the HTTP client playback handler (issues HTTP 302 redirects to Dispatcharr / streamcache).
type Server struct {
	cfg               *models.Config
	database          *db.DB
	onActivation      ActivationCallback
	onMovieActivation MovieActivationCallback
	onSync            SyncCallback
	activeSeries      sync.Map
	httpServer        *http.Server
}

// NewServer creates a new HTTP client playback handler server.
func NewServer(cfg *models.Config, database *db.DB, onActivation ActivationCallback) *Server {
	srv := &Server{
		cfg:          cfg,
		database:     database,
		onActivation: onActivation,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/play/movie/", srv.handleMoviePlay)
	mux.HandleFunc("/play/series/", srv.handleSeriesPlay)
	mux.HandleFunc("/activate", srv.handleActivate)
	mux.HandleFunc("/sync", srv.handleSync)
	mux.HandleFunc("/stage", srv.handleSync)
	mux.HandleFunc("/healthz", srv.handleHealth)
	mux.HandleFunc("/status", srv.handleStatus)
	mux.HandleFunc("/debug/logs", srv.handleDebugLogs)
	mux.HandleFunc("/debug/queue", srv.handleDebugQueue)

	// Standard Go Runtime Debugging & Profiling (pprof)
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return srv
}

// SetMovieActivationCallback sets the callback function for movie provider resolution and activation.
func (s *Server) SetMovieActivationCallback(cb MovieActivationCallback) {
	s.onMovieActivation = cb
}

// SetSyncCallback sets the callback function for triggering manual staging runs.
func (s *Server) SetSyncCallback(cb SyncCallback) {
	s.onSync = cb
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.onSync == nil {
		http.Error(w, `{"error":"sync callback not registered"}`, http.StatusInternalServerError)
		return
	}
	go func() {
		_ = s.onSync()
	}()
	_, _ = w.Write([]byte(`{"status":"staging_pass_triggered"}`))
}

// Start launches the HTTP server asynchronously.
func (s *Server) Start() {
	go func() {
		log.Printf("[Proxy] Starting Extensionless Playback Handler on port %d...", s.cfg.Port)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[Proxy] HTTP Playback Handler error: %v", err)
		}
	}()
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var pending, inProgress, failed int
	if s.database != nil {
		pending, inProgress, failed, _ = s.database.GetQueueStats()
	}

	status := map[string]any{
		"status":              "online",
		"port":                s.cfg.Port,
		"sync_interval":       s.cfg.SyncInterval.String(),
		"reconcile_interval":  s.cfg.ReconcileInterval.String(),
		"dispatcharr_workers": s.cfg.DispatcharrWorkers,
		"emby_workers":        s.cfg.EmbyWorkers,
		"emby_pace_delay":     s.cfg.EmbyPaceDelay.String(),
		"queue": map[string]int{
			"pending":     pending,
			"in_progress": inProgress,
			"failed":      failed,
		},
		"emby_configured": s.cfg.EmbyURL != "" && s.cfg.EmbyAPIKey != "",
	}
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleDebugLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	logs := GlobalLogBuffer.GetLogs()
	for _, l := range logs {
		_, _ = w.Write([]byte(l))
	}
}

func (s *Server) handleDebugQueue(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.database == nil {
		http.Error(w, `{"error":"database not initialized"}`, http.StatusInternalServerError)
		return
	}
	statusFilter := r.URL.Query().Get("status")
	tasks, err := s.database.GetRecentQueueTasks(100, statusFilter)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(tasks)
}

// format302Target formats the final 302 target URL, appending ?url= parameter for streamcache if configured.
func format302Target(rawURL, streamcacheURL string) string {
	if streamcacheURL == "" {
		return rawURL
	}
	escapedURL := url.QueryEscape(rawURL)
	return fmt.Sprintf("%s?url=%s", strings.TrimRight(streamcacheURL, "/"), escapedURL)
}

func (s *Server) handleMoviePlay(w http.ResponseWriter, r *http.Request) {
	// Extensionless Path formats:
	// 1. /play/movie/{movie_id}
	// 2. /play/movie/{movie_id}/{variant} (e.g. 4k)
	path := strings.TrimPrefix(r.URL.Path, "/play/movie/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		http.Error(w, "Movie ID required", http.StatusBadRequest)
		return
	}

	parts := strings.Split(path, "/")
	rawID := parts[0]
	// Strip trailing extensions if present for legacy compatibility
	if idx := strings.Index(rawID, "."); idx != -1 {
		rawID = rawID[:idx]
	}

	movieID, err := strconv.Atoi(rawID)
	if err != nil {
		http.Error(w, "Invalid movie ID", http.StatusBadRequest)
		return
	}

	variantKey := "base"
	if len(parts) > 1 && parts[1] != "" {
		v := parts[1]
		if idx := strings.Index(v, "."); idx != -1 {
			v = v[:idx]
		}
		if v != "" {
			variantKey = v
		}
	}

	// 1. Query SQLite database for 302 target URL
	targetURL := ""
	if s.database != nil {
		targetURL, _ = s.database.GetMovieTargetURL(movieID, variantKey)
	}

	// 2. If target URL is missing or unactivated, trigger provider resolution callback
	if targetURL == "" && s.onMovieActivation != nil {
		resolvedBaseURL, resErr := s.onMovieActivation(movieID)
		if resErr == nil && resolvedBaseURL != "" {
			if variantKey == "base" {
				targetURL = resolvedBaseURL
			} else if s.database != nil {
				targetURL, _ = s.database.GetMovieTargetURL(movieID, variantKey)
			}
		}
	}

	// 3. Fallback to direct Dispatcharr URL if not found in database
	if targetURL == "" {
		targetURL = fmt.Sprintf("%s/movie/%s/%s/%d.mkv", strings.TrimRight(s.cfg.DispatcharrURL, "/"), s.cfg.XCUser, s.cfg.XCPassword, movieID)
	}

	finalRedirectURL := format302Target(targetURL, s.cfg.StreamcacheURL)
	log.Printf("[Proxy] Movie %d (Variant '%s') -> HTTP 302 Redirect: %s", movieID, variantKey, finalRedirectURL)
	http.Redirect(w, r, finalRedirectURL, http.StatusFound)
}

func (s *Server) handleSeriesPlay(w http.ResponseWriter, r *http.Request) {
	// Extensionless Path formats:
	// 1. /play/series/{series_id}/{season}/{episode}
	// 2. /play/series/{series_id}/{season}/{episode}/{variant} (e.g. 4k)
	path := strings.TrimPrefix(r.URL.Path, "/play/series/")
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid series stream path (expected /play/series/{id}/{season}/{episode})", http.StatusBadRequest)
		return
	}

	rawSeriesID := parts[0]
	seriesID, sErr := strconv.Atoi(rawSeriesID)
	seasonNum, seErr := strconv.Atoi(parts[1])

	rawEp := parts[2]
	if idx := strings.Index(rawEp, "."); idx != -1 {
		rawEp = rawEp[:idx]
	}
	epNum, epErr := strconv.Atoi(rawEp)

	if sErr != nil || seErr != nil || epErr != nil {
		http.Error(w, "Invalid numeric parameters in series path", http.StatusBadRequest)
		return
	}

	variantKey := "base"
	if len(parts) > 3 && parts[3] != "" {
		v := parts[3]
		if idx := strings.Index(v, "."); idx != -1 {
			v = v[:idx]
		}
		if v != "" {
			variantKey = v
		}
	}

	// 0. Handle Special Activation Bumper (S00E99 / Season 00 Special)
	if s.cfg.EnableActivationBumper && ((seasonNum == models.ActivationSeasonNumber && epNum == models.ActivationEpisodeNumber) || (seasonNum == 0 && epNum == 99)) {
		rangeHeader := r.Header.Get("Range")
		// Fast single-shot probe handling for ffprobe / HEAD
		isSmallProbe := (r.Method == http.MethodHead) || (rangeHeader != "" && strings.HasPrefix(rangeHeader, "bytes=0-") && !strings.Contains(rangeHeader, "bytes=0-80"))
		if isSmallProbe {
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "activated.mp4", time.Unix(1700000000, 0), bytes.NewReader(assets.ActivationBumperMP4))
			return
		}

		// Check if already activated in DB
		isActivated := false
		if s.database != nil {
			if st, _ := s.database.GetSeriesState(seriesID); st != nil && st.IsActivated {
				isActivated = true
			}
		}

		// Trigger background activation immediately
		if !isActivated && s.onActivation != nil {
			if _, inProgress := s.activeSeries.LoadOrStore(seriesID, struct{}{}); !inProgress {
				go func(sid int) {
					defer s.activeSeries.Delete(sid)
					s.onActivation(sid)
				}(seriesID)
			}
		}

		log.Printf("[Proxy] Series %d: Streaming 5s Activation Bumper to client...", seriesID)
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "activated.mp4", time.Unix(1700000000, 0), bytes.NewReader(assets.ActivationBumperMP4))

		// AFTER PLAYBACK FINISHES: Wait 1s grace period for player pipeline to close, then ensure activation completion
		go func(sid int) {
			time.Sleep(1 * time.Second)
			if s.onActivation != nil {
				s.onActivation(sid)
			}
		}(seriesID)
		return
	}

	// 1. Query SQLite database for 302 target URL
	targetURL := ""
	if s.database != nil {
		targetURL, _ = s.database.GetSeriesTargetURL(seriesID, seasonNum, epNum, variantKey)
	}

	// Check if this series is activated in DB
	isActivated := false
	if s.database != nil {
		if st, _ := s.database.GetSeriesState(seriesID); st != nil && st.IsActivated {
			isActivated = true
		}
	}

	// If unactivated, run activation (with deduplication to prevent looping on multiple Emby probes)
	if !isActivated && s.onActivation != nil {
		if _, inProgress := s.activeSeries.LoadOrStore(seriesID, struct{}{}); !inProgress {
			if targetURL == "" {
				s.onActivation(seriesID)
				s.activeSeries.Delete(seriesID)
				if s.database != nil {
					targetURL, _ = s.database.GetSeriesTargetURL(seriesID, seasonNum, epNum, variantKey)
				}
			} else {
				go func(sid int) {
					defer s.activeSeries.Delete(sid)
					s.onActivation(sid)
				}(seriesID)
			}
		}
	}

	// 2. If targetURL is still empty for the requested season/episode (e.g. show only has Season 2+),
	// query the earliest available episode target URL for this series
	if targetURL == "" && s.database != nil {
		targetURL, _ = s.database.GetFirstAvailableSeriesTargetURL(seriesID, variantKey)
	}

	// 3. Fallback to direct Dispatcharr XC stream URL if still not found
	if targetURL == "" {
		targetURL = fmt.Sprintf("%s/series/%s/%s/%d.mkv", strings.TrimRight(s.cfg.DispatcharrURL, "/"), s.cfg.XCUser, s.cfg.XCPassword, seriesID)
	}

	finalRedirectURL := format302Target(targetURL, s.cfg.StreamcacheURL)
	log.Printf("[Proxy] Series %d S%02dE%02d (Variant '%s') -> HTTP 302 Redirect: %s", seriesID, seasonNum, epNum, variantKey, finalRedirectURL)

	// Trigger predictive fetch-ahead for next episode(s) in background (500ms Tap & Bail) ONLY if streamcache is enabled
	if s.cfg.FetchAheadCount > 0 && s.cfg.StreamcacheURL != "" {
		go func(sid, sNum, eNum, count int) {
			for i := 1; i <= count; i++ {
				nextEp := eNum + i
				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				prefetchURL := fmt.Sprintf("%s/play/series/%d/%d/%d", strings.TrimRight(s.cfg.URL, "/"), sid, sNum, nextEp)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, prefetchURL, nil)
				if err == nil {
					resp, pErr := http.DefaultClient.Do(req)
					if pErr == nil && resp != nil {
						resp.Body.Close()
					}
				}
				cancel()
			}
		}(seriesID, seasonNum, epNum, s.cfg.FetchAheadCount)
	}

	// Sub-50ms Immediate 302 Redirect!
	http.Redirect(w, r, finalRedirectURL, http.StatusFound)
}

func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	rawSeriesID := r.URL.Query().Get("series_id")
	seriesID, err := strconv.Atoi(rawSeriesID)
	if err != nil || seriesID <= 0 {
		http.Error(w, "Valid series_id query parameter required", http.StatusBadRequest)
		return
	}

	log.Printf("[Proxy] Activation request received for Series ID: %d", seriesID)

	targetURL := ""
	if s.database != nil {
		targetURL, _ = s.database.GetSeriesTargetURL(seriesID, 1, 1, "base")
	}

	// Trigger activation to populate real episode target streams (with deduplication)
	if s.onActivation != nil {
		if _, inProgress := s.activeSeries.LoadOrStore(seriesID, struct{}{}); !inProgress {
			if targetURL == "" {
				s.onActivation(seriesID)
				s.activeSeries.Delete(seriesID)
				if s.database != nil {
					targetURL, _ = s.database.GetSeriesTargetURL(seriesID, 1, 1, "base")
				}
			} else {
				go func(sid int) {
					defer s.activeSeries.Delete(sid)
					s.onActivation(sid)
				}(seriesID)
			}
		}
	}

	if targetURL == "" {
		targetURL = fmt.Sprintf("%s/series/%s/%s/%d.mkv", strings.TrimRight(s.cfg.DispatcharrURL, "/"), s.cfg.XCUser, s.cfg.XCPassword, seriesID)
	}
	finalRedirectURL := format302Target(targetURL, s.cfg.StreamcacheURL)

	// Sub-50ms Immediate 302 Redirect!
	http.Redirect(w, r, finalRedirectURL, http.StatusFound)
}
