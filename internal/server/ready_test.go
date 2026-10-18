package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/repository"
)

// pingStore is a database that answers Ping with err, or blocks until
// released when hang is set.
type pingStore struct {
	repository.Store
	err  error
	hang chan struct{}
}

func (s pingStore) Ping(context.Context) error {
	if s.hang != nil {
		<-s.hang
	}
	return s.err
}

func readyServer(t *testing.T, store repository.Store) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	assetStore, err := assets.NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AppEnv: "development", AuthMode: config.AuthDevelopment, SessionTTL: time.Hour}
	srv, err := New(cfg, store, assetStore, auth.NewService(store, cfg.SessionTTL, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, root
}

func getReady(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://example.com/ready", nil))
	return w
}

func assertReady(t *testing.T, w *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if w.Code != code || strings.TrimSpace(w.Body.String()) != body {
		t.Fatalf("got %d %q, want %d %q", w.Code, w.Body.String(), code, body)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control %q", got)
	}
}

func TestReadyOKWithoutSession(t *testing.T) {
	srv, _ := readyServer(t, pingStore{})
	assertReady(t, getReady(t, srv), http.StatusOK, `{"status":"ok","db":"ok","assets":"ok"}`)
}

func TestReadyDegradedWhenDatabasePingFails(t *testing.T) {
	srv, _ := readyServer(t, pingStore{err: errors.New("dial tcp 10.0.0.5:5432: password=hunter2 refused")})
	w := getReady(t, srv)
	assertReady(t, w, http.StatusServiceUnavailable, `{"status":"degraded","db":"error","assets":"ok"}`)
	if strings.Contains(w.Body.String(), "hunter2") || strings.Contains(w.Body.String(), "5432") {
		t.Fatalf("error text leaked: %q", w.Body.String())
	}
}

func TestReadyDegradedWhenDatabaseHangs(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	srv, _ := readyServer(t, pingStore{hang: hang})
	srv.readyTimeout = 50 * time.Millisecond
	started := time.Now()
	assertReady(t, getReady(t, srv), http.StatusServiceUnavailable, `{"status":"degraded","db":"error","assets":"ok"}`)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hung check held the probe for %v", elapsed)
	}
}

func TestReadyDegradedWhenAssetVolumeIsGone(t *testing.T) {
	srv, root := readyServer(t, pingStore{})
	if err := os.RemoveAll(filepath.Join(root, "objects")); err != nil {
		t.Fatal(err)
	}
	assertReady(t, getReady(t, srv), http.StatusServiceUnavailable, `{"status":"degraded","db":"ok","assets":"error"}`)
}

// stalledAssetStore counts Ping calls, each blocking until release closes, like
// a stat on a hung NFS mount.
type stalledAssetStore struct {
	assets.Store
	pings   *atomic.Int32
	release chan struct{}
}

func (s stalledAssetStore) Ping(context.Context) error {
	s.pings.Add(1)
	<-s.release
	return nil
}

func TestReadyRunsOneAssetCheckWhileItIsStalled(t *testing.T) {
	srv, _ := readyServer(t, pingStore{})
	srv.readyTimeout = 20 * time.Millisecond
	var pings atomic.Int32
	release := make(chan struct{})
	srv.assets = stalledAssetStore{Store: srv.assets, pings: &pings, release: release}
	for range 5 {
		assertReady(t, getReady(t, srv), http.StatusServiceUnavailable, `{"status":"degraded","db":"ok","assets":"error"}`)
	}
	if got := pings.Load(); got != 1 {
		t.Fatalf("stalled mount got %d concurrent stats, want 1", got)
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		srv.assetCheck.mu.Lock()
		running := srv.assetCheck.running
		srv.assetCheck.mu.Unlock()
		if running == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stalled check never finished after release")
		}
		time.Sleep(time.Millisecond)
	}
	srv.readyTimeout = time.Second
	assertReady(t, getReady(t, srv), http.StatusOK, `{"status":"ok","db":"ok","assets":"ok"}`)
	if got := pings.Load(); got != 2 {
		t.Fatalf("recovered mount got %d stats, want a fresh one (2)", got)
	}
}

func TestReadyAgainstPostgres(t *testing.T) {
	f := setup(t)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://example.com/ready", nil))
	assertReady(t, w, http.StatusOK, `{"status":"ok","db":"ok","assets":"ok"}`)

	f.store.Close()
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://example.com/ready", nil))
	assertReady(t, w, http.StatusServiceUnavailable, `{"status":"degraded","db":"error","assets":"ok"}`)
}
