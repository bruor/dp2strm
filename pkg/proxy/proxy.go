package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dp2strm/pkg/db"
	"dp2strm/pkg/models"
)

// ActivationCallback is a function signature for background series activation processing.
type ActivationCallback func(seriesID int)

// MovieActivationCallback is a function signature for background movie activation and provider resolution.
type MovieActivationCallback func(movieID int) (baseTargetURL string, err error)

// Server is the HTTP client playback handler (issues HTTP 302 redirects to Dispatcharr / streamcache).
type Server struct {
	cfg               *models.Config
	database          *db.DB
	onActivation      ActivationCallback
	onMovieActivation MovieActivationCallback
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
	mux.HandleFunc("/healthz", srv.handleHealth)

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
		variantKey = parts[1]
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
		variantKey = parts[3]
	}

	// Check if this series is activated in DB
	if s.database != nil {
		st, _ := s.database.GetSeriesState(seriesID)
		if st == nil || !st.IsActivated {
			// Trigger async background activation worker while playback starts!
			if s.onActivation != nil {
				go s.onActivation(seriesID)
			}
		}
	}

	// 1. Query SQLite database for 302 target URL
	targetURL := ""
	if s.database != nil {
		targetURL, _ = s.database.GetSeriesTargetURL(seriesID, seasonNum, epNum, variantKey)
	}

	// 2. Fallback to direct Dispatcharr URL if not found in database
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

	// 1. Immediate sub-50ms 302 redirect to Episode 1 stream!
	targetURL := ""
	if s.database != nil {
		targetURL, _ = s.database.GetSeriesTargetURL(seriesID, 1, 1, "base")
	}
	if targetURL == "" {
		targetURL = fmt.Sprintf("%s/series/%s/%s/%d.mkv", strings.TrimRight(s.cfg.DispatcharrURL, "/"), s.cfg.XCUser, s.cfg.XCPassword, seriesID)
	}
	finalRedirectURL := format302Target(targetURL, s.cfg.StreamcacheURL)

	// 2. Trigger async background activation worker while player starts streaming!
	if s.onActivation != nil {
		go s.onActivation(seriesID)
	}

	// Sub-50ms Immediate 302 Redirect!
	http.Redirect(w, r, finalRedirectURL, http.StatusFound)
}
