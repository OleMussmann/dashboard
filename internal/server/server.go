// Package server implements the HTTP API for the dashboard.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ole/dashboard-api/internal/scheduler"
)

// Server is the HTTP API server.
type Server struct {
	sched  *scheduler.Scheduler
	logger *slog.Logger
	mux    *http.ServeMux
}

// New creates a new API server.
func New(sched *scheduler.Scheduler, logger *slog.Logger) *Server {
	s := &Server{
		sched:  sched,
		logger: logger,
		mux:    http.NewServeMux(),
	}
	s.routes()
	return s
}

// Handler returns the HTTP handler for the server.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/v1/status/", s.handleStatusByHost)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.sched.GetSnapshot()
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleStatusByHost(w http.ResponseWriter, r *http.Request) {
	// Extract hostname from path: /api/v1/status/{hostname}
	hostname := strings.TrimPrefix(r.URL.Path, "/api/v1/status/")
	hostname = strings.TrimSuffix(hostname, "/")

	if hostname == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hostname required"})
		return
	}

	state := s.sched.GetMachineState(hostname)
	if state == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "machine not found"})
		return
	}

	writeJSON(w, http.StatusOK, state)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
