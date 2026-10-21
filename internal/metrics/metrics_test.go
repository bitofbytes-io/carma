package metrics

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("scrape status=%d body=%s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func assertSeries(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, series := range want {
		if !strings.Contains(body, series) {
			t.Errorf("missing series %q", series)
		}
	}
}

func TestMiddlewareLabelsRoutePatternsAndSkipsProbes(t *testing.T) {
	m := New()
	router := chi.NewRouter()
	router.Use(m.Middleware)
	ok := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }
	router.Get("/health", ok)
	router.Get("/ready", ok)
	router.Get("/vehicles/{vehicleID}", ok)
	router.Post("/records/{recordID}/delete", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusSeeOther)
	})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/vehicles/7d0c5e9a-1111-4f00-9b8e-000000000001?tab=records", nil),
		httptest.NewRequest(http.MethodGet, "/vehicles/7d0c5e9a-1111-4f00-9b8e-000000000002", nil),
		httptest.NewRequest(http.MethodPost, "/records/abc/delete", nil),
		httptest.NewRequest(http.MethodGet, "/no/such/page/123", nil),
		httptest.NewRequest(http.MethodGet, "/health", nil),
		httptest.NewRequest(http.MethodGet, "/ready", nil),
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
		httptest.NewRequest("BREW", "/vehicles/x", nil),
	} {
		router.ServeHTTP(httptest.NewRecorder(), request)
	}

	body := scrape(t, m)
	assertSeries(t, body,
		`http_requests_total{code="200",method="GET",route="/vehicles/{vehicleID}"} 2`,
		`http_requests_total{code="303",method="POST",route="/records/{recordID}/delete"} 1`,
		`http_requests_total{code="404",method="GET",route="unmatched"} 1`,
		`http_requests_total{code="405",method="OTHER",route="unmatched"} 1`,
		`http_request_duration_seconds_count{code="200",method="GET",route="/vehicles/{vehicleID}"} 2`,
		`http_request_duration_seconds_bucket{code="200",method="GET",route="/vehicles/{vehicleID}",le="+Inf"} 2`,
		"go_goroutines ",
	)
	for _, leaked := range []string{"7d0c5e9a", "abc", "/no/such", "tab=", `route="/health"`, `route="/ready"`, `route="/metrics"`, "BREW"} {
		if strings.Contains(body, leaked) {
			t.Errorf("metrics leak %q", leaked)
		}
	}
}

func TestDomainCountersAndPoolStats(t *testing.T) {
	// The pool connects lazily, so its stats are readable without a database.
	poolConfig, err := pgxpool.ParseConfig("postgres://carma@127.0.0.1:1/carma?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = 7
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m := New()
	m.RegisterPool(pool.Stat)
	m.LoginFailure("not_invited")
	m.OAuthFailure("google", "state")
	m.OAuthFailure("google", "state")
	m.Upload("receipt", ResultSuccess)
	m.Upload("vehicle_photo", ResultRejected)
	m.Job("recall_check", nil)
	m.Job("recall_check", errors.New("nhtsa down"))

	assertSeries(t, scrape(t, m),
		`carma_login_failures_total{reason="not_invited"} 1`,
		`carma_oauth_failures_total{provider="google",reason="state"} 2`,
		`carma_uploads_total{kind="receipt",result="success"} 1`,
		`carma_uploads_total{kind="vehicle_photo",result="rejected"} 1`,
		`carma_jobs_total{job="recall_check",result="success"} 1`,
		`carma_jobs_total{job="recall_check",result="failure"} 1`,
		"pgxpool_acquired_conns 0",
		"pgxpool_idle_conns 0",
		"pgxpool_total_conns 0",
		"pgxpool_max_conns 7",
		"pgxpool_acquires_total 0",
		"pgxpool_empty_acquire_wait_seconds_total 0",
	)
}

func TestNilMetricsRecordNothing(t *testing.T) {
	var m *Metrics
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if handler := m.Middleware(next); handler == nil {
		t.Fatal("nil metrics dropped the handler")
	}
	m.RegisterPool(func() *pgxpool.Stat { return nil })
	m.LoginFailure("error")
	m.OAuthFailure("google", "exchange")
	m.Upload("receipt", ResultFailure)
	m.Job("asset_cleanup", nil)
}

func TestStartServesOnlyMetricsAndShutsDown(t *testing.T) {
	m := New()
	m.Upload("receipt", ResultSuccess)
	server := Start("127.0.0.1:0", m.Handler(), slog.New(slog.DiscardHandler))
	if server == nil {
		t.Fatal("listener not started")
	}
	get := func(path string) (int, string) {
		t.Helper()
		response, err := http.Get("http://" + server.Addr() + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	if code, body := get("/metrics"); code != http.StatusOK || !strings.Contains(body, `carma_uploads_total{kind="receipt",result="success"} 1`) {
		t.Fatalf("metrics status=%d body=%s", code, body)
	}
	if code, _ := get("/"); code != http.StatusNotFound {
		t.Fatalf("metrics listener served / with %d", code)
	}
	if err := server.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get("http://" + server.Addr() + "/metrics"); err == nil {
		t.Fatal("listener still serving after shutdown")
	}
}

func TestStartIsDisabledByOffAndSurvivesBindFailure(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	for _, addr := range []string{"off", "OFF", " Off ", " oFf "} {
		if server := Start(addr, New().Handler(), log); server != nil {
			t.Fatalf("METRICS_ADDR=%q started a listener", addr)
		}
	}
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	if server := Start(taken.Addr().String(), New().Handler(), log); server != nil {
		t.Fatal("bound an address already in use")
	}
	var disabled *Server
	if err := disabled.Shutdown(t.Context()); err != nil {
		t.Fatalf("nil server shutdown: %v", err)
	}
}

func TestLeaderLockGaugeIsBoundedFreshAndOmittedOnQueryFailure(t *testing.T) {
	m := New()
	held := true
	var queryErr error
	m.RegisterLeaderLocks(func(ctx context.Context) (map[string]bool, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("snapshot query has no deadline")
		}
		return map[string]bool{"asset_cleanup": held, "reminder_email": false, "reminder_digest": false, "recall_check": false, "unexpected_pid": true}, queryErr
	})
	body := scrape(t, m)
	assertSeries(t, body, `carma_leader_locks_held{purpose="asset_cleanup"} 1`, `carma_leader_locks_held{purpose="reminder_email"} 0`, `carma_leader_locks_held{purpose="reminder_digest"} 0`, `carma_leader_locks_held{purpose="recall_check"} 0`)
	if strings.Contains(body, "unexpected_pid") {
		t.Fatal("unbounded purpose exported")
	}
	held = false
	assertSeries(t, scrape(t, m), `carma_leader_locks_held{purpose="asset_cleanup"} 0`)
	queryErr = errors.New("database unavailable")
	if strings.Contains(scrape(t, m), "carma_leader_locks_held") {
		t.Fatal("query failure exported stale or false gauge")
	}
}
