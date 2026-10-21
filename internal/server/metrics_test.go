package server

import (
	"bytes"
	"errors"
	"fmt"
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

func assertUploadCounts(t *testing.T, m *metrics.Metrics, kind string, success, failure, rejected int) {
	t.Helper()
	body := scrapeMetrics(t, m)
	for result, want := range map[string]int{"success": success, "failure": failure, "rejected": rejected} {
		prefix := fmt.Sprintf(`carma_uploads_total{kind="%s",result="%s"} `, kind, result)
		found := false
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, prefix) {
				found = true
				if line != fmt.Sprintf("%s%d", prefix, want) {
					t.Errorf("metric=%s, want %d", line, want)
				}
			}
		}
		if !found && want != 0 {
			t.Errorf("missing %s%d", prefix, want)
		}
	}
}

func TestUploadsCountOnceAfterEachSuccessfulDatabaseWrite(t *testing.T) {
	f := setup(t)
	m := metrics.New()
	f.s.EnableMetrics(m)
	photo := []byte("\x89PNG\r\n\x1a\nphoto")
	b, ct := multipartFormBody(t, map[string]string{"nickname": "Outback"}, "photo", "car.png", photo)
	if w := f.do(t, http.MethodPost, "/vehicles", b, ct); w.Code != http.StatusSeeOther {
		t.Fatal(w.Body.String())
	}
	assertUploadCounts(t, m, "vehicle_photo", 1, 0, 0)
	vehicles, err := f.store.ListVehicles(t.Context(), false)
	if err != nil || len(vehicles) != 1 {
		t.Fatalf("vehicles=%v err=%v", vehicles, err)
	}
	path := "/vehicles/" + vehicles[0].ID.String()
	b, ct = multipartFormBody(t, map[string]string{"nickname": "Updated"}, "photo", "new.png", photo)
	if w := f.do(t, http.MethodPost, path, b, ct); w.Code != http.StatusSeeOther {
		t.Fatal(w.Body.String())
	}
	assertUploadCounts(t, m, "vehicle_photo", 2, 0, 0)
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil || len(types) == 0 {
		t.Fatalf("types=%v err=%v", types, err)
	}
	files := []namedFile{{"first.pdf", []byte("%PDF-1.7\nfirst")}, {"second.pdf", []byte("%PDF-1.7\nsecond")}}
	b, ct = multipartFormBodyFiles(t, map[string]string{"occurred_on": "2026-07-30", "service_type_id": types[0].ID.String()}, "receipts", files)
	w := f.do(t, http.MethodPost, path+"/records", b, ct)
	if w.Code != http.StatusSeeOther {
		t.Fatal(w.Body.String())
	}
	assertUploadCounts(t, m, "receipt", 2, 0, 0)
	b, ct = multipartFormBodyFiles(t, nil, "receipts", files)
	if w = f.do(t, http.MethodPost, w.Header().Get("Location")+"/attachments", b, ct); w.Code != http.StatusSeeOther {
		t.Fatal(w.Body.String())
	}
	assertUploadCounts(t, m, "receipt", 4, 0, 0)
}
