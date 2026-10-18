package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
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
	db := s.dbCheck.start(s.readyTimeout, s.store.Ping)
	files := s.assetCheck.start(s.readyTimeout, s.assets.Ping)
	result := readiness{Status: readyOK, DB: db.wait(ctx, "db"), Assets: files.wait(ctx, "assets")}
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

// singleCheck runs one dependency check at a time. Probes that arrive while a
// check is still running wait on it instead of starting another, so a stat
// stuck in the kernel on a hung NFS mount ties up one goroutine, not one per
// probe.
type singleCheck struct {
	mu      sync.Mutex
	running *checkRun
}

// checkRun is one check's outcome; err is set before done is closed.
type checkRun struct {
	done chan struct{}
	err  error
}

// start returns the check already running, or runs check bounded by timeout.
// The run is detached from any request, so a probe that gives up early does
// not fail it for the probes waiting on it.
func (c *singleCheck) start(timeout time.Duration, check func(context.Context) error) *checkRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running != nil {
		return c.running
	}
	run := &checkRun{done: make(chan struct{})}
	c.running = run
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		run.err = check(ctx)
		cancel()
		c.mu.Lock()
		c.running = nil
		c.mu.Unlock()
		close(run.done)
	}()
	return run
}

// wait reports the run's outcome, or an error once ctx ends first, even if the
// check ignores its own deadline. A run that already finished reports its own
// outcome, though ctx may have ended while an earlier wait held the probe.
func (r *checkRun) wait(ctx context.Context, name string) string {
	select {
	case <-r.done:
	case <-ctx.Done():
	}
	var err error
	select {
	case <-r.done:
		err = r.err
	default:
		err = ctx.Err()
	}
	if err != nil {
		slog.Warn("readiness check failed", "check", name, "error", err)
		return readyError
	}
	return readyOK
}
