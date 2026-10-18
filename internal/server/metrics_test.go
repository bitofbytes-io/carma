package server

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/metrics"
)

func scrapeMetrics(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return w.Body.String()
}

func TestPublicRouterRecordsRoutePatternsButNeverServesMetrics(t *testing.T) {
	srv, _ := readyServer(t, pingStore{})
	m := metrics.New()
	srv.EnableMetrics(m)
	router := srv.Router()
	serve := func(method, target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "http://example.com"+target, nil))
		return w
	}

	if w := serve(http.MethodGet, "/metrics"); w.Code != http.StatusNotFound {
		t.Fatalf("public /metrics status=%d", w.Code)
	}
	serve(http.MethodGet, "/health")
	serve(http.MethodGet, "/ready")
	serve(http.MethodGet, "/vehicles/0b7f2a10-6a39-4c3a-9a51-5c0ed8a1e001")
	serve(http.MethodGet, "/api/auth/google/callback?state=forged&code=x")

	body := scrapeMetrics(t, m)
	for _, series := range []string{
		`http_requests_total{code="303",method="GET",route="/vehicles/{vehicleID}"} 1`,
		`http_requests_total{code="303",method="GET",route="/api/auth/google/callback"} 1`,
		`carma_oauth_failures_total{provider="google",reason="state"} 1`,
	} {
		if !strings.Contains(body, series) {
			t.Errorf("missing series %q", series)
		}
	}
	for _, leaked := range []string{"0b7f2a10", "forged", `route="/health"`, `route="/ready"`, `route="/metrics"`, `route="unmatched"`} {
		if strings.Contains(body, leaked) {
			t.Errorf("metrics leak %q", leaked)
		}
	}
}

func TestUploadResultClassifiesSaveErrors(t *testing.T) {
	for err, want := range map[error]string{
		nil:                     metrics.ResultSuccess,
		assets.ErrTooLarge:      metrics.ResultRejected,
		assets.ErrUnsupported:   metrics.ResultRejected,
		errors.New("disk full"): metrics.ResultFailure,
	} {
		if got := uploadResult(err); got != want {
			t.Errorf("uploadResult(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestUploadsAreCountedByKindAndResult(t *testing.T) {
	f := setup(t)
	m := metrics.New()
	f.s.EnableMetrics(m)
	f.router = f.s.Router()

	photo := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
	b, ct := multipartFormBody(t, map[string]string{"nickname": "Outback"}, "photo", "car.jpg", photo)
	if w := f.do(t, http.MethodPost, "/vehicles", b, ct); w.Code != http.StatusSeeOther {
		t.Fatalf("create vehicle status=%d body=%s", w.Code, w.Body.String())
	}
	// A valid photo discarded with an invalid form is not stored.
	b, ct = multipartFormBody(t, map[string]string{"nickname": "Outback", "year": "1800"}, "photo", "car.jpg", photo)
	if w := f.do(t, http.MethodPost, "/vehicles", b, ct); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid vehicle form status=%d", w.Code)
	}
	vehicles, err := f.store.ListVehicles(t.Context(), false)
	if err != nil || len(vehicles) != 1 {
		t.Fatalf("vehicles=%v err=%v", vehicles, err)
	}
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil || len(types) == 0 {
		t.Fatalf("service types=%v err=%v", types, err)
	}
	// The PDF saved before the unsupported file is cleaned up with it.
	var batch bytes.Buffer
	mw := multipart.NewWriter(&batch)
	for k, v := range map[string]string{"occurred_on": "2026-07-30", "service_type_id": types[0].ID.String(), "odometer": "1000"} {
		_ = mw.WriteField(k, v)
	}
	for _, file := range []struct{ name, content string }{{"receipt.pdf", "%PDF-1.7\nreceipt"}, {"notes.txt", "plain text"}} {
		part, err := mw.CreateFormFile("receipts", file.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(file.content))
	}
	_ = mw.Close()
	if w := f.do(t, http.MethodPost, "/vehicles/"+vehicles[0].ID.String()+"/records", &batch, mw.FormDataContentType()); w.Code != http.StatusBadRequest {
		t.Fatalf("unsupported receipt status=%d", w.Code)
	}

	body := scrapeMetrics(t, m)
	for _, series := range []string{
		`carma_uploads_total{kind="vehicle_photo",result="success"} 1`,
		`carma_uploads_total{kind="vehicle_photo",result="rejected"} 1`,
		`carma_uploads_total{kind="receipt",result="rejected"} 2`,
		`http_requests_total{code="303",method="POST",route="/vehicles"} 1`,
	} {
		if !strings.Contains(body, series) {
			t.Errorf("missing series %q in\n%s", series, body)
		}
	}
	if strings.Contains(body, `carma_uploads_total{kind="receipt",result="success"}`) {
		t.Errorf("discarded receipt counted as stored in\n%s", body)
	}
}
