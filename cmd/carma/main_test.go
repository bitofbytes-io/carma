package main

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerTimeoutsAllowLargeMobileUploads(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := newHTTPServer("4700", handler)

	if server.Addr != ":4700" || server.Handler == nil {
		t.Fatalf("server configuration: %+v", server)
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
