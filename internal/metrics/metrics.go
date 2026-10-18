// Package metrics exports Prometheus metrics on a separate internal listener:
// HTTP RED metrics for the public router, database pool stats, and Carma's
// domain counters. A nil *Metrics records nothing, so tests and tools can skip it.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// DefaultAddr is the metrics listener address when METRICS_ADDR is unset.
	DefaultAddr = ":9464"
	// Off disables the metrics listener when used as METRICS_ADDR.
	Off = "off"

	// UnmatchedRoute labels requests that matched no route, so unknown paths
	// cannot add series.
	UnmatchedRoute = "unmatched"

	ResultSuccess  = "success"
	ResultFailure  = "failure"
	ResultRejected = "rejected"
)

// Metrics owns a private registry and the collectors Carma records into.
type Metrics struct {
	registry      *prometheus.Registry
	requests      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	loginFailures *prometheus.CounterVec
	oauthFailures *prometheus.CounterVec
	uploads       *prometheus.CounterVec
	jobs          *prometheus.CounterVec
}

// New returns metrics registered on their own registry together with the Go
// runtime and process collectors.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests handled by the public router, by method, route pattern, and status code.",
		}, []string{"method", "route", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency on the public router, by method, route pattern, and status code.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route", "code"}),
		loginFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "carma_login_failures_total",
			Help: "Sign-ins refused or failed after the identity provider step, by reason.",
		}, []string{"reason"}),
		oauthFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "carma_oauth_failures_total",
			Help: "OAuth callbacks that failed state validation or code exchange, by provider and reason.",
		}, []string{"provider", "reason"}),
		uploads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "carma_uploads_total",
			Help: "Uploaded files stored or refused, by kind and result.",
		}, []string{"kind", "result"}),
		jobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "carma_jobs_total",
			Help: "Background job runs, by job and result.",
		}, []string{"job", "result"}),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.requests, m.duration, m.loginFailures, m.oauthFailures, m.uploads, m.jobs,
	)
	return m
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// RegisterPool exports stat's pgxpool fields as pgxpool_* metrics.
func (m *Metrics) RegisterPool(stat func() *pgxpool.Stat) {
	if m == nil {
		return
	}
	m.registry.MustRegister(newPoolCollector(stat))
}

// Middleware records RED metrics for each request, labelled by the chi route
// pattern rather than the raw path. Health, readiness, and metrics probes are
// not recorded.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/ready", "/metrics":
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			labels := prometheus.Labels{"method": method(r.Method), "route": route(r), "code": strconv.Itoa(status)}
			m.requests.With(labels).Inc()
			m.duration.With(labels).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(ww, r)
	})
}

func route(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return UnmatchedRoute
}

// method keeps arbitrary request methods out of the label set.
func method(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return m
	}
	return "OTHER"
}

// LoginFailure counts a sign-in that failed after the identity provider step,
// such as an account that is not invited.
func (m *Metrics) LoginFailure(reason string) {
	if m != nil {
		m.loginFailures.WithLabelValues(reason).Inc()
	}
}

// OAuthFailure counts an OAuth callback rejected by state validation or code
// exchange.
func (m *Metrics) OAuthFailure(provider, reason string) {
	if m != nil {
		m.oauthFailures.WithLabelValues(provider, reason).Inc()
	}
}

// Upload counts one uploaded file of kind with a Result* outcome.
func (m *Metrics) Upload(kind, result string) {
	if m != nil {
		m.uploads.WithLabelValues(kind, result).Inc()
	}
}

// Job counts one run of a background job, failed when err is non-nil.
func (m *Metrics) Job(job string, err error) {
	if m == nil {
		return
	}
	result := ResultSuccess
	if err != nil {
		result = ResultFailure
	}
	m.jobs.WithLabelValues(job, result).Inc()
}

// Server is the internal metrics listener. A nil *Server is a disabled one.
type Server struct {
	http *http.Server
	addr string
	done chan struct{}
}

// Start serves handler at GET /metrics on addr in the background. It returns
// nil when addr is Off, and logs and returns nil when addr cannot be bound:
// metrics never stop the app from serving.
func Start(addr string, handler http.Handler, log *slog.Logger) *Server {
	if strings.EqualFold(addr, Off) {
		log.Info("metrics listener disabled")
		return nil
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("metrics listener not started", "addr", addr, "error", err)
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", handler)
	s := &Server{
		http: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
		},
		addr: listener.Addr().String(),
		done: make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		log.Info("metrics listening", "addr", s.addr)
		if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics listener stopped", "addr", addr, "error", err)
		}
	}()
	return s
}

// Addr is the address the listener is bound to.
func (s *Server) Addr() string { return s.addr }

// Shutdown gracefully stops the listener. It is a no-op on a nil Server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	err := s.http.Shutdown(ctx)
	<-s.done
	return err
}
