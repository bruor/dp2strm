package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"dp2strm/pkg/models"
)

// Server is the embedded HTTP playback and activation proxy.
type Server struct {
	cfg        *models.Config
	httpServer *http.Server
}

// NewServer creates a new HTTP proxy server.
func NewServer(cfg *models.Config) *Server {
	srv := &Server{
		cfg: cfg,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/play/movie/", srv.handleMoviePlay)
	mux.HandleFunc("/play/series/", srv.handleSeriesPlay)
	mux.HandleFunc("/activate", srv.handleActivate)
	mux.HandleFunc("/healthz", srv.handleHealth)

	srv.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.ProxyPort),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return srv
}

// Start launches the HTTP server asynchronously.
func (s *Server) Start() {
	go func() {
		log.Printf("[Proxy] Starting HTTP Proxy Server on port %d...", s.cfg.ProxyPort)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[Proxy] HTTP Server error: %v", err)
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

func (s *Server) handleMoviePlay(w http.ResponseWriter, r *http.Request) {
	// Path format: /play/movie/{stream_id}.{ext}
	path := strings.TrimPrefix(r.URL.Path, "/play/movie/")
	if path == "" {
		http.Error(w, "Stream ID required", http.StatusBadRequest)
		return
	}

	parts := strings.Split(path, ".")
	streamID := parts[0]
	ext := "mkv"
	if len(parts) > 1 {
		ext = parts[1]
	}

	// Construct Dispatcharr XC playback URL
	targetURL := fmt.Sprintf("%s/movie/agy/agypassword/%s.%s", strings.TrimRight(s.cfg.DispatcharrURL, "/"), streamID, ext)

	log.Printf("[Proxy] Movie Stream %s -> HTTP 302 Redirect: %s", streamID, targetURL)
	http.Redirect(w, r, targetURL, http.StatusFound)
}

func (s *Server) handleSeriesPlay(w http.ResponseWriter, r *http.Request) {
	// Path format: /play/series/{series_id}/{stream_id}.{ext}
	path := strings.TrimPrefix(r.URL.Path, "/play/series/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid series stream path", http.StatusBadRequest)
		return
	}

	fileParts := strings.Split(parts[1], ".")
	streamID := fileParts[0]
	ext := "mp4"
	if len(fileParts) > 1 {
		ext = fileParts[1]
	}

	targetURL := fmt.Sprintf("%s/series/agy/agypassword/%s.%s", strings.TrimRight(s.cfg.DispatcharrURL, "/"), streamID, ext)

	log.Printf("[Proxy] Series Stream %s -> HTTP 302 Redirect: %s", streamID, targetURL)
	http.Redirect(w, r, targetURL, http.StatusFound)
}

func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	seriesID := r.URL.Query().Get("series_id")
	log.Printf("[Proxy] Activation dummy episode triggered for Series ID: %s", seriesID)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Series activation event received"))
}
