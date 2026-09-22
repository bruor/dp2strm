package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
type ActivationCallback func(seriesID int) error

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
	activeSeriesMu    sync.Mutex
	activeSeries      map[int]chan struct{}
	httpServer        *http.Server
}

// NewServer creates a new HTTP client playback handler server.
func NewServer(cfg *models.Config, database *db.DB, onActivation ActivationCallback) *Server {
	srv := &Server{
		cfg:          cfg,
		database:     database,
		onActivation: onActivation,
		activeSeries: make(map[int]chan struct{}),
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

// format302Target formats the final 302 target URL, dynamically rewriting Dispatcharr's base URL
// to streamcacheDispatcharrURL (if configured) and appending ?url= parameter for streamcache.
func format302Target(rawURL, streamcacheURL, streamcacheDispatcharrURL, dispatcharrURL string) string {
	if streamcacheDispatcharrURL != "" && dispatcharrURL != "" {
		origBase := strings.TrimRight(dispatcharrURL, "/")
		targetBase := strings.TrimRight(streamcacheDispatcharrURL, "/")
		if strings.HasPrefix(rawURL, origBase) {
			rawURL = targetBase + strings.TrimPrefix(rawURL, origBase)
		}
	}
	if streamcacheURL == "" {
		return rawURL
	}
	baseSC := strings.TrimRight(streamcacheURL, "/")
	if !strings.HasSuffix(baseSC, "/stream") {
		baseSC += "/stream"
	}
	escapedURL := url.QueryEscape(rawURL)
	return fmt.Sprintf("%s?url=%s", baseSC, escapedURL)
}

func (s *Server) formatRedirect(rawURL string) string {
	return format302Target(rawURL, s.cfg.StreamcacheURL, s.cfg.StreamcacheDispatcharrURL, s.cfg.DispatcharrURL)
}

func (s *Server) handleMoviePlay(w http.ResponseWriter, r *http.Request) {
	// Extensionless Path formats:
	// 1. /play/movie/{identifier} (e.g. tmdb:1739639, imdb:tt36615831, or 1198086)
	// 2. /play/movie/{identifier}/{variant} (e.g. tmdb:1739639/4k)
	path := strings.TrimPrefix(r.URL.Path, "/play/movie/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		http.Error(w, "Movie identifier required", http.StatusBadRequest)
		return
	}

	parts := strings.Split(path, "/")
	rawID := parts[0]
	// Strip trailing extensions if present for legacy compatibility
	if idx := strings.Index(rawID, "."); idx != -1 {
		rawID = rawID[:idx]
	}

	movieID := 0
	if s.database != nil {
		resolvedID, _, resErr := s.database.GetMovieIDByIdentifier(rawID)
		if resErr == nil && resolvedID > 0 {
			movieID = resolvedID
		}
	}

	if movieID == 0 {
		if numID, err := strconv.Atoi(rawID); err == nil && numID > 0 {
			movieID = numID
		} else {
			http.Error(w, fmt.Sprintf("Invalid movie identifier %q", rawID), http.StatusBadRequest)
			return
		}
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
		} else if s.database != nil {
			// Self-Healing Fallback: Check if movie ID was re-assigned upstream in Dispatcharr
			if healedID, fErr := s.database.FindMovieByFingerprint(movieID, "", 0); fErr == nil && healedID > 0 && healedID != movieID {
				log.Printf("[Proxy] Self-Healing: Stale movie ID %d healed to active Dispatcharr ID %d. Retrying activation...", movieID, healedID)
				healedBaseURL, hErr := s.onMovieActivation(healedID)
				if hErr == nil && healedBaseURL != "" {
					staleMovieID := movieID
					movieID = healedID
					if variantKey == "base" {
						targetURL = healedBaseURL
					} else {
						targetURL, _ = s.database.GetMovieTargetURL(movieID, variantKey)
					}
					// 1. Alias in stream_variants for stale ID so subsequent requests before disk reconciliation resolve in <1ms
					_ = s.database.SaveStreamVariant(&db.StreamVariant{
						ItemID:             staleMovieID,
						ItemType:           "movie",
						VariantKey:         variantKey,
						TargetURL:          targetURL,
						Quality:            "1080p",
						ContainerExtension: "mkv",
					})
					// 2. Enqueue reconcile task so disk .strm file gets corrected during next maintenance run
					_ = s.database.EnqueueTask("movie", healedID, "reconcile_movie", models.PriorityReconcile)
				}
			}
		}
	}

	// 3. Fallback to direct Dispatcharr URL if not found in database
	if targetURL == "" {
		targetURL = fmt.Sprintf("%s/movie/%s/%s/%d.mkv", strings.TrimRight(s.cfg.DispatcharrURL, "/"), s.cfg.XCUser, s.cfg.XCPassword, movieID)
	}

	finalRedirectURL := s.formatRedirect(targetURL)
	log.Printf("[Proxy] Movie %s (Resolved ID: %d, Variant '%s') -> HTTP 302 Redirect: %s", rawID, movieID, variantKey, finalRedirectURL)
	http.Redirect(w, r, finalRedirectURL, http.StatusFound)
}

func (s *Server) handleSeriesPlay(w http.ResponseWriter, r *http.Request) {
	// Extensionless Path formats:
	// 1. /play/series/{identifier}/{season}/{episode} (e.g. tmdb:218510/1/1 or 30998/1/1)
	// 2. /play/series/{identifier}/{season}/{episode}/{variant} (e.g. 4k)
	path := strings.TrimPrefix(r.URL.Path, "/play/series/")
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid series stream path (expected /play/series/{id}/{season}/{episode})", http.StatusBadRequest)
		return
	}

	rawSeriesID := parts[0]
	if idx := strings.Index(rawSeriesID, "."); idx != -1 {
		rawSeriesID = rawSeriesID[:idx]
	}

	seriesID := 0
	if s.database != nil {
		resolvedID, _, resErr := s.database.GetSeriesIDByIdentifier(rawSeriesID)
		if resErr == nil && resolvedID > 0 {
			seriesID = resolvedID
		}
	}

	if seriesID == 0 {
		if numID, err := strconv.Atoi(rawSeriesID); err == nil && numID > 0 {
			seriesID = numID
		} else {
			http.Error(w, fmt.Sprintf("Invalid series identifier %q", rawSeriesID), http.StatusBadRequest)
			return
		}
	}

	seasonNum, seErr := strconv.Atoi(parts[1])

	rawEp := parts[2]
	if idx := strings.Index(rawEp, "."); idx != -1 {
		rawEp = rawEp[:idx]
	}
	epNum, epErr := strconv.Atoi(rawEp)

	if seErr != nil || epErr != nil {
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

	// If unactivated, run activation (or wait if another concurrent request is already activating it)
	if !isActivated && s.onActivation != nil {
		s.activeSeriesMu.Lock()
		waitCh, inProgress := s.activeSeries[seriesID]
		if !inProgress {
			waitCh = make(chan struct{})
			s.activeSeries[seriesID] = waitCh
			s.activeSeriesMu.Unlock()

			if targetURL == "" {
				actErr := s.onActivation(seriesID)
				s.activeSeriesMu.Lock()
				delete(s.activeSeries, seriesID)
				close(waitCh)
				s.activeSeriesMu.Unlock()

				if s.database != nil {
					targetURL, _ = s.database.GetSeriesTargetURL(seriesID, seasonNum, epNum, variantKey)
					// Self-Healing Fallback for Series if targetURL still empty
					if targetURL == "" {
						if healedID, fErr := s.database.FindSeriesByFingerprint(seriesID, "", 0); fErr == nil && healedID > 0 && healedID != seriesID {
							log.Printf("[Proxy] Self-Healing: Stale series ID %d healed to active Dispatcharr ID %d. Retrying activation...", seriesID, healedID)
							staleSeriesID := seriesID
							seriesID = healedID
							actErr = s.onActivation(healedID)
							targetURL, _ = s.database.GetSeriesTargetURL(seriesID, seasonNum, epNum, variantKey)
							if targetURL != "" {
								_ = s.database.SaveStreamVariant(&db.StreamVariant{
									ItemID:             staleSeriesID,
									ItemType:           "series",
									SeasonNum:          seasonNum,
									EpisodeNum:         epNum,
									VariantKey:         variantKey,
									TargetURL:          targetURL,
									Quality:            "1080p",
									ContainerExtension: "mkv",
								})
								_ = s.database.EnqueueTask("series", healedID, "reconcile_series", models.PriorityReconcile)
							}
						}
					}
				}

				if actErr != nil && targetURL == "" {
					log.Printf("[Proxy] Series %s (ID: %d): Activation failed: %v. Rejecting playback to let Emby fail.", rawSeriesID, seriesID, actErr)
					http.Error(w, fmt.Sprintf("Failed to activate series: %v", actErr), http.StatusBadGateway)
					return
				}
			} else {
				go func(sid int, ch chan struct{}) {
					defer func() {
						s.activeSeriesMu.Lock()
						delete(s.activeSeries, sid)
						close(ch)
						s.activeSeriesMu.Unlock()
					}()
					_ = s.onActivation(sid)
				}(seriesID, waitCh)
			}
		} else {
			s.activeSeriesMu.Unlock()
			// Another concurrent request is already running activation for this series!
			// Wait for it to finish so we redirect to the real stream instead of streaming the bumper!
			select {
			case <-waitCh:
				// Leader activation completed!
				if s.database != nil {
					targetURL, _ = s.database.GetSeriesTargetURL(seriesID, seasonNum, epNum, variantKey)
				}
			case <-r.Context().Done():
				return
			case <-time.After(30 * time.Second):
				log.Printf("[Proxy] Timeout waiting for concurrent activation of Series %d", seriesID)
			}
		}
	}

	// 2. If targetURL is available for the requested season/episode, redirect immediately!
	if targetURL != "" {
		finalRedirectURL := s.formatRedirect(targetURL)
		log.Printf("[Proxy] Series %s (Resolved ID: %d) S%02dE%02d (Variant '%s') -> HTTP 302 Redirect: %s", rawSeriesID, seriesID, seasonNum, epNum, variantKey, finalRedirectURL)

		// Trigger predictive fetch-ahead for next episode(s) in background (500ms Tap & Bail) ONLY if streamcache is enabled
		if s.cfg.StreamcacheURL != "" && s.cfg.FetchAheadCount > 0 && s.database != nil {
			go s.triggerFetchAhead(seriesID, seasonNum, epNum, variantKey, s.cfg.FetchAheadCount)
		}

		http.Redirect(w, r, finalRedirectURL, http.StatusFound)
		return
	}

	// 3. Only show the activation bumper video if activation was successful!
	// (e.g. show only has Season 2+ upstream and user played the S01E01 placeholder).
	// If the series was not activated or there is an error, fail playback cleanly so Emby reports the error.
	seriesActivated := false
	if s.database != nil {
		if st, _ := s.database.GetSeriesState(seriesID); st != nil && st.IsActivated {
			seriesActivated = true
		}
	}

	if seriesActivated {
		log.Printf("[Proxy] Series %s (ID: %d) S%02dE%02d: Series activated, but target episode not present upstream. Streaming 5s Activation Bumper to client...", rawSeriesID, seriesID, seasonNum, epNum)
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "activated.mp4", time.Unix(1700000000, 0), bytes.NewReader(assets.ActivationBumperMP4))
		return
	}

	// 4. Series is not activated and no target URL exists -> fail playback cleanly
	log.Printf("[Proxy] Series %s (ID: %d) S%02dE%02d: Target episode not available and series unactivated. Rejecting playback to let Emby fail.", rawSeriesID, seriesID, seasonNum, epNum)
	http.Error(w, "Target episode not available and series unactivated", http.StatusBadGateway)
}

func (s *Server) triggerFetchAhead(seriesID, seasonNum, epNum int, variantKey string, count int) {
	nextEps, err := s.database.GetNextSeriesEpisodes(seriesID, seasonNum, epNum, variantKey, count)
	if err != nil {
		log.Printf("[Proxy] Fetch-ahead error querying upcoming episodes for series %d: %v", seriesID, err)
		return
	}
	if len(nextEps) == 0 {
		return
	}

	for _, ep := range nextEps {
		if ep.TargetURL == "" {
			continue
		}
		tapURL := s.formatRedirect(ep.TargetURL)
		log.Printf("[Proxy] Fetch-ahead tap (5s): Series %d S%02dE%02d -> %s", seriesID, ep.SeasonNum, ep.EpisodeNum, tapURL)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, tapURL, nil)
		if err != nil {
			cancel()
			log.Printf("[Proxy] Fetch-ahead error building request: %v", err)
			continue
		}
		req.Header.Set("User-Agent", "dp2strm-fetch-ahead/1.0")

		resp, pErr := http.DefaultClient.Do(req)
		if pErr != nil {
			log.Printf("[Proxy] Fetch-ahead tap request failed for Series %d S%02dE%02d: %v", seriesID, ep.SeasonNum, ep.EpisodeNum, pErr)
			cancel()
			continue
		}

		// Read a small initial buffer (64 KB) to ensure streamcache establishes the upstream
		// hydration worker and begins background caching before we disconnect.
		buf := make([]byte, 64*1024)
		n, _ := io.ReadFull(resp.Body, buf)
		_ = resp.Body.Close()
		cancel()

		log.Printf("[Proxy] Fetch-ahead tap latched: Series %d S%02dE%02d (status: %d, read %d bytes)", seriesID, ep.SeasonNum, ep.EpisodeNum, resp.StatusCode, n)
	}
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
		s.activeSeriesMu.Lock()
		waitCh, inProgress := s.activeSeries[seriesID]
		if !inProgress {
			waitCh = make(chan struct{})
			s.activeSeries[seriesID] = waitCh
			s.activeSeriesMu.Unlock()

			if targetURL == "" {
				actErr := s.onActivation(seriesID)
				s.activeSeriesMu.Lock()
				delete(s.activeSeries, seriesID)
				close(waitCh)
				s.activeSeriesMu.Unlock()

				if actErr != nil {
					log.Printf("[Proxy] Series %d activation failed: %v", seriesID, actErr)
					http.Error(w, fmt.Sprintf("Failed to activate series: %v", actErr), http.StatusBadGateway)
					return
				}

				if s.database != nil {
					targetURL, _ = s.database.GetSeriesTargetURL(seriesID, 1, 1, "base")
				}
			} else {
				go func(sid int, ch chan struct{}) {
					defer func() {
						s.activeSeriesMu.Lock()
						delete(s.activeSeries, sid)
						close(ch)
						s.activeSeriesMu.Unlock()
					}()
					_ = s.onActivation(sid)
				}(seriesID, waitCh)
			}
		} else {
			s.activeSeriesMu.Unlock()
			select {
			case <-waitCh:
				if s.database != nil {
					targetURL, _ = s.database.GetSeriesTargetURL(seriesID, 1, 1, "base")
				}
			case <-r.Context().Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}

	if targetURL == "" {
		http.Error(w, "Target episode not available and series unactivated", http.StatusBadGateway)
		return
	}
	finalRedirectURL := s.formatRedirect(targetURL)

	// Sub-50ms Immediate 302 Redirect!
	http.Redirect(w, r, finalRedirectURL, http.StatusFound)
}
