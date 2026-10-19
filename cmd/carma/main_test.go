package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type recordingHandler struct{ called bool }

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.called = true
	w.WriteHeader(http.StatusNoContent)
}

func TestHTTPServerTimeoutsAllowLargeMobileUploads(t *testing.T) {
	handler := &recordingHandler{}
	server := newHTTPServer("4700", handler)

	if server.Addr != ":4700" || server.Handler != handler {
		t.Fatalf("server configuration: %+v", server)
	}
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.com/health", nil))
	if !handler.called || response.Code != http.StatusNoContent {
		t.Fatalf("supplied handler invoked=%v status=%d", handler.called, response.Code)
	}
	if server.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("read header timeout = %v", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 5*time.Minute || server.WriteTimeout != 5*time.Minute {
		t.Fatalf("request timeouts read=%v write=%v", server.ReadTimeout, server.WriteTimeout)
	}
	if server.IdleTimeout != 2*time.Minute {
		t.Fatalf("idle timeout = %v", server.IdleTimeout)
	}
}

func TestCloseStoreAfterSchedulersWaitsThenCloses(t *testing.T) {
	var schedulers sync.WaitGroup
	schedulers.Add(1)
	workerDone := make(chan struct{})
	go func() {
		defer schedulers.Done()
		close(workerDone)
	}()
	<-workerDone
	closed := false

	err := closeStoreAfterSchedulers(&schedulers, time.Second, func() { closed = true })
	if err != nil || !closed {
		t.Fatalf("close error=%v store closed=%t", err, closed)
	}
}

func TestCloseStoreAfterSchedulersTimeoutSkipsClose(t *testing.T) {
	var schedulers sync.WaitGroup
	schedulers.Add(1)
	closed := false

	err := closeStoreAfterSchedulers(&schedulers, 10*time.Millisecond, func() { closed = true })
	if !errors.Is(err, errSchedulerShutdownTimeout) {
		t.Fatalf("close error=%v", err)
	}
	if closed {
		t.Fatal("store closed while scheduler may still be using it")
	}
	// Release the helper's waiter goroutine after proving the timeout decision.
	schedulers.Done()
}

func TestRunJobEndsABlockedRunAtTheDeadline(t *testing.T) {
	finished := make(chan struct{})
	var timedOut bool
	var err error
	go func() {
		defer close(finished)
		// Stands in for a job waiting on a pool connection that never frees.
		timedOut, err = runJob(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("job blocked past its deadline")
	}
	if !timedOut || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed out=%t error=%v", timedOut, err)
	}
}

func TestRunJobCountsADeadlineAsAFailure(t *testing.T) {
	// The job stops early at the deadline but reports no error.
	timedOut, err := runJob(context.Background(), time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	if !timedOut || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed out=%t error=%v", timedOut, err)
	}
}

func TestRunJobShutdownIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	timedOut, err := runJob(ctx, time.Minute, func(ctx context.Context) error { return ctx.Err() })
	if timedOut || !errors.Is(err, context.Canceled) {
		t.Fatalf("timed out=%t error=%v", timedOut, err)
	}
}

func TestRunJobPassesThroughAFinishedRun(t *testing.T) {
	failure := errors.New("job failed")
	var deadline time.Time
	timedOut, err := runJob(context.Background(), time.Minute, func(ctx context.Context) error {
		deadline, _ = ctx.Deadline()
		return failure
	})
	if timedOut || !errors.Is(err, failure) {
		t.Fatalf("timed out=%t error=%v", timedOut, err)
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Minute {
		t.Fatalf("job deadline in %v, want within a minute", remaining)
	}
}
