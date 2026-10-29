package server

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/google/uuid"
)

type fixture struct {
	s      *Server
	router http.Handler
	store  *repository.Postgres
	cookie *http.Cookie
	user   model.User
}

type trackingAssetStore struct {
	assets.Store
	savedKey, deletedKey string
}

func (s *trackingAssetStore) Save(ctx context.Context, source io.Reader, max int64) (assets.Object, error) {
	object, err := s.Store.Save(ctx, source, max)
	if err == nil {
		s.savedKey = object.Key
	}
	return object, err
}

func (s *trackingAssetStore) Delete(ctx context.Context, key string) error {
	s.deletedKey = key
	return s.Store.Delete(ctx, key)
}

type deleteSessionErrorStore struct {
	repository.Store
	err error
}

func (s deleteSessionErrorStore) DeleteSession(context.Context, uuid.UUID) error { return s.err }

func setup(t *testing.T) fixture {
	t.Helper()
	store := testdb.Store(t)
	a, e := assets.NewLocalStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	cfg := &config.Config{AppEnv: "development", AuthMode: config.AuthDevelopment, SessionTTL: 90 * 24 * time.Hour, MaxUploadBytes: 25 << 20}
	svc := auth.NewService(store, cfg.SessionTTL, nil)
	u, token, e := svc.DevLogin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	srv, e := New(cfg, store, a, svc, nil)
	if e != nil {
		t.Fatal(e)
	}
	srv.now = func() time.Time { return time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC) }
	return fixture{srv, srv.Router(), store, &http.Cookie{Name: middleware.CookieName, Value: token}, u}
}

func (f fixture) do(t *testing.T, method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://example.com"+path, body)
	r.AddCookie(f.cookie)
	if method != "GET" {
		r.Header.Set("Origin", "http://example.com")
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func formBody(t *testing.T, fields map[string]string, filename string, file []byte) (*bytes.Buffer, string) {
	return multipartFormBody(t, fields, "receipts", filename, file)
}

func multipartFormBody(t *testing.T, fields map[string]string, fileField, filename string, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if filename != "" {
		p, e := mw.CreateFormFile(fileField, filename)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = p.Write(file)
	}
	_ = mw.Close()
	return &b, mw.FormDataContentType()
}

func createVehicle(t *testing.T, f fixture) model.Vehicle {
	b, ct := formBody(t, map[string]string{"nickname": "Dan's Outback", "year": "2019", "make": "Subaru", "model": "Outback"}, "", nil)
	w := f.do(t, "POST", "/vehicles", b, ct)
	if w.Code != 303 {
		t.Fatalf("create vehicle: %d %s", w.Code, w.Body.String())
	}
	vs, e := f.store.ListVehicles(t.Context(), false)
	if e != nil || len(vs) != 1 {
		t.Fatalf("vehicles=%v err=%v", vs, e)
	}
	return vs[0]
}

func createAttachment(t *testing.T, f fixture) (model.Record, model.Attachment) {
	t.Helper()
	v := createVehicle(t, f)
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil || len(types) == 0 {
		t.Fatalf("service types=%v err=%v", types, err)
	}
	b, ct := formBody(t, map[string]string{
		"occurred_on":     "2026-07-30",
		"service_type_id": types[0].ID.String(),
		"odometer":        "62410",
		"cost":            "89.00",
		"vendor":          "Test Shop",
	}, "receipt.pdf", []byte("%PDF-1.7\nfixture"))
	response := f.do(t, http.MethodPost, "/vehicles/"+v.ID.String()+"/records", b, ct)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create record: %d %s", response.Code, response.Body.String())
	}
	records, err := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &v.ID})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	record, attachments, err := f.store.GetRecord(t.Context(), records[0].ID)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("record=%v attachments=%v err=%v", record, attachments, err)
	}
	return record, attachments[0]
}

// assertHTMXConfirmedForm checks that the form posting to action is submitted by
// htmx, because htmx only shows hx-confirm prompts for requests it issues.
func assertHTMXConfirmedForm(t *testing.T, body, action, prompt string) {
	t.Helper()
	for _, tag := range regexp.MustCompile(`<form\b[^>]*>`).FindAllString(body, -1) {
		if !strings.Contains(tag, `action="`+action+`"`) {
			continue
		}
		if !strings.Contains(tag, `hx-post="`+action+`"`) || !strings.Contains(tag, `hx-confirm="`+prompt+`"`) {
			t.Fatalf("form %s is not an htmx-confirmed post", tag)
		}
		return
	}
	t.Fatalf("no form posts to %s", action)
}
