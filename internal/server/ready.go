package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// readyTimeout bounds the readiness checks, so a hung database or asset mount
// reports degraded instead of stalling the probe.
const readyTimeout = 2 * time.Second

const (
	readyOK    = "ok"
	readyError = "error"
)

// readiness is the /ready body. Checks report only "ok" or "error"; the
// cause is logged, never returned.
type readiness struct {
	Status string `json:"status"`
	DB     string `json:"db"`
	Assets string `json:"assets"`
}

// ready reports whether the app's dependencies are up: the database answers a
// ping and the asset volume is reachable. /health only says the process runs.
func (s *Server) ready(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), s.readyTimeout)
	defer cancel()
	db, files := probe(ctx, "db", s.store.Ping), probe(ctx, "assets", s.assets.Ping)
	result := readiness{Status: readyOK, DB: <-db, Assets: <-files}
	status := http.StatusOK
	if result.DB != readyOK || result.Assets != readyOK {
		result.Status = "degraded"
		status = http.StatusServiceUnavailable
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(result)
}

// probe runs check in the background and reports its outcome, giving up when
// ctx ends even if check ignores it: a stat on a hung NFS mount blocks in the
// kernel.
func probe(ctx context.Context, name string, check func(context.Context) error) <-chan string {
	done := make(chan error, 1)
	go func() { done <- check(ctx) }()
	result := make(chan string, 1)
	go func() {
		var err error
		select {
		case err = <-done:
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err != nil {
			slog.Warn("readiness check failed", "check", name, "error", err)
			result <- readyError
			return
		}
		result <- readyOK
	}()
	return result
}
