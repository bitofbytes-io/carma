package server

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

type fixture struct {
	s      *Server
	router http.Handler
	store  *repository.Memory
	cookie *http.Cookie
	user   model.User
}

type fakeGoogle struct {
	claims  auth.Claims
	allowed bool
}

func (f fakeGoogle) AuthURL(state string) string {
	return "https://accounts.example/auth?state=" + state
}
func (f fakeGoogle) Exchange(_ context.Context, _ string) (auth.Claims, error) { return f.claims, nil }
func (f fakeGoogle) Allowed(_ string) bool                                     { return f.allowed }

func setup(t *testing.T) fixture {
	t.Helper()
	store := repository.NewMemory()
	a, e := assets.NewLocalStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	cfg := &config.Config{AppEnv: "development", AuthMode: config.AuthDevelopment, SessionTTL: 90 * 24 * time.Hour, MaxUploadBytes: 25 << 20}
	svc := auth.NewService(store, cfg.SessionTTL)
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
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if filename != "" {
		p, e := mw.CreateFormFile("receipts", filename)
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
func TestRecordWithPDFRangeAndFilteredCSV(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	oil := types[0]
	for _, x := range types {
		if x.Name == "Oil change" {
			oil = x
		}
	}
	pdf := []byte("%PDF-1.7\nreceipt fixture bytes")
	b, ct := formBody(t, map[string]string{"occurred_on": "2026-07-30", "service_type_id": oil.ID.String(), "odometer": "62410", "cost": "89.00", "vendor": "Jiffy Lube", "notes": "full synthetic"}, "receipt.pdf", pdf)
	w := f.do(t, "POST", "/vehicles/"+v.ID.String()+"/records", b, ct)
	if w.Code != 303 {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}
	rows, _ := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &v.ID, Search: "Jiffy", ServiceTypeID: &oil.ID})
	if len(rows) != 1 || rows[0].AttachmentCount != 1 {
		t.Fatalf("rows=%+v", rows)
	}
	_, as, _ := f.store.GetRecord(t.Context(), rows[0].ID)
	detail := f.do(t, "GET", "/records/"+rows[0].ID.String(), nil, "")
	if strings.Count(detail.Body.String(), "hx-confirm=") < 2 {
		t.Fatal("record and receipt deletes must both confirm")
	}
	req := httptest.NewRequest("GET", "http://example.com/attachments/"+as[0].ID.String(), nil)
	req.AddCookie(f.cookie)
	req.Header.Set("Range", "bytes=0-4")
	rw := httptest.NewRecorder()
	f.router.ServeHTTP(rw, req)
	if rw.Code != http.StatusPartialContent || rw.Body.String() != "%PDF-" {
		t.Fatalf("range: %d %q", rw.Code, rw.Body.String())
	}
	csvW := f.do(t, "GET", "/vehicles/"+v.ID.String()+"/export.csv?q=Jiffy&type="+oil.ID.String(), nil, "")
	parsed, e := csv.NewReader(strings.NewReader(strings.TrimPrefix(csvW.Body.String(), "\ufeff"))).ReadAll()
	if e != nil || len(parsed) != 2 || parsed[1][5] != "Jiffy Lube" {
		t.Fatalf("csv=%v err=%v", parsed, e)
	}
}

func TestMultipartTotalLimitIsEnforced(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	f.s.cfg.MaxMultipartBytes = 1024
	types, _ := f.store.ListServiceTypes(t.Context())
	fields := map[string]string{"occurred_on": "2026-07-30", "service_type_id": types[0].ID.String()}
	b, ct := formBody(t, fields, "large.pdf", append([]byte("%PDF-"), bytes.Repeat([]byte("x"), 2048)...))
	w := f.do(t, "POST", "/vehicles/"+v.ID.String()+"/records", b, ct)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	rows, _ := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &v.ID})
	if len(rows) != 0 {
		t.Fatal("oversized request created a record")
	}
}

func TestHTMXRecordPartialAndFilterAttributes(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	now := time.Now()
	for i, vendor := range []string{"Alpha Shop", "Beta Garage"} {
		_, _ = f.store.CreateRecord(t.Context(), model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[i].ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC), Vendor: vendor, CreatedAt: now.Add(time.Duration(i) * time.Second)}, nil)
	}
	full := f.do(t, "GET", "/vehicles/"+v.ID.String(), nil, "")
	body := full.Body.String()
	for _, want := range []string{`hx-trigger="submit, input changed delay:300ms, change"`, `hx-push-url="true"`, `hx-target="#records-section"`, `aria-label="Search records"`, `aria-label="Filter by service type"`, `aria-label="From date"`, `aria-label="To date"`, `aria-label="Sort records by"`, `aria-label="Sort direction"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}
	global := f.do(t, "GET", "/records", nil, "").Body.String()
	for _, name := range []string{"Search records", "Filter by service type", "From date", "To date", "Sort records by", "Sort direction"} {
		if !strings.Contains(global, `aria-label="`+name+`"`) {
			t.Fatalf("global records missing accessible name %q", name)
		}
	}
	req := httptest.NewRequest("GET", "http://example.com/vehicles/"+v.ID.String()+"?q=Beta", nil)
	req.AddCookie(f.cookie)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	body = w.Body.String()
	if strings.Contains(body, "<html") || !strings.Contains(body, `id="records-list"`) || !strings.Contains(body, types[1].Name) || strings.Count(body, `class="record-row" href=`) != 1 || strings.Contains(body, "ZgotmplZ") || !strings.Contains(body, "export.csv?q=Beta") {
		t.Fatalf("bad partial: %s", body)
	}
}

func TestReminderDeleteHTMXAndConfirmations(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	months := 6
	rm, _ := f.store.UpsertReminder(t.Context(), model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMonths: &months, Enabled: true, CreatedAt: time.Now()})
	page := f.do(t, "GET", "/vehicles/"+v.ID.String()+"/reminders", nil, "")
	body := page.Body.String()
	for _, want := range []string{"hx-post=", "hx-target=\"#reminder-list\"", "hx-confirm=\"Delete this reminder?\"", rm.ID.String() + "/delete", `aria-label="Service type for ` + types[0].Name + ` reminder"`, `aria-label="Interval months for ` + types[0].Name + ` reminder"`, `aria-label="Interval miles for ` + types[0].Name + ` reminder"`, `aria-label="Enable ` + types[0].Name + ` reminder"`, `aria-label="Service type for new reminder"`, `aria-label="Interval months for new reminder"`, `aria-label="Interval miles for new reminder"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in reminders page", want)
		}
	}
	update := url.Values{"service_type_id": {types[0].ID.String()}, "months": {"7"}, "enabled": {"true"}}.Encode()
	updateReq := httptest.NewRequest("POST", "http://example.com/vehicles/"+v.ID.String()+"/reminders", strings.NewReader(update))
	updateReq.AddCookie(f.cookie)
	updateReq.Header.Set("Origin", "http://example.com")
	updateReq.Header.Set("HX-Request", "true")
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateW := httptest.NewRecorder()
	f.router.ServeHTTP(updateW, updateReq)
	updated, _ := f.store.ListReminders(t.Context(), &v.ID, true)
	if updateW.Code != 200 || len(updated) != 1 || updated[0].IntervalMonths == nil || *updated[0].IntervalMonths != 7 || strings.Contains(updateW.Body.String(), "<html") {
		t.Fatalf("inline update failed: %d %+v %s", updateW.Code, updated, updateW.Body.String())
	}
	req := httptest.NewRequest("POST", "http://example.com/vehicles/"+v.ID.String()+"/reminders/"+rm.ID.String()+"/delete", strings.NewReader(""))
	req.AddCookie(f.cookie)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "<html") || !strings.Contains(w.Body.String(), `id="reminder-list"`) {
		t.Fatalf("bad delete partial: %d %s", w.Code, w.Body.String())
	}
	rows, _ := f.store.ListReminders(t.Context(), &v.ID, true)
	if len(rows) != 0 {
		t.Fatal("reminder not deleted")
	}
	edit := f.do(t, "GET", "/vehicles/"+v.ID.String()+"/edit", nil, "")
	if !strings.Contains(edit.Body.String(), `hx-confirm="Archive this vehicle?"`) {
		t.Fatal("archive confirmation missing")
	}
}
func TestReminderOverdueThenMatchingRecordClears(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	var oil model.ServiceType
	for _, x := range types {
		if x.Name == "Oil change" {
			oil = x
		}
	}
	m := 6
	mi := int64(5000)
	odo := int64(10000)
	baseline := model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), OdometerMiles: &odo, CreatedAt: time.Now()}
	_, _ = f.store.CreateRecord(t.Context(), baseline, nil)
	_, _ = f.store.UpsertReminder(t.Context(), model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: oil.ID, IntervalMonths: &m, IntervalMiles: &mi, Enabled: true, CreatedAt: time.Now()})
	w := f.do(t, "GET", "/", nil, "")
	if !strings.Contains(w.Body.String(), "OVERDUE") {
		t.Fatalf("dashboard missing overdue: %s", w.Body.String())
	}
	newOdo := int64(11000)
	_, _ = f.store.CreateRecord(t.Context(), model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC), OdometerMiles: &newOdo, CreatedAt: time.Now().Add(time.Second)}, nil)
	w = f.do(t, "GET", "/", nil, "")
	if strings.Contains(w.Body.String(), "OVERDUE") {
		t.Fatal("matching record did not clear reminder")
	}
}
func TestSharedGarageAndMobileInputs(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	svc := f.s.auth
	u2, token, e := svc.Login(t.Context(), auth.Claims{Subject: "second", Email: "second@example.com", Name: "Second", EmailVerified: true})
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", "http://example.com/vehicles/"+v.ID.String(), nil)
	req.AddCookie(&http.Cookie{Name: middleware.CookieName, Value: token})
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Dan&#39;s Outback") || !strings.Contains(w.Body.String(), "Export CSV") || u2.ID == f.user.ID {
		t.Fatalf("shared view failed: %d", w.Code)
	}
	req = httptest.NewRequest("GET", "http://example.com/vehicles/"+v.ID.String()+"/records/new", nil)
	req.AddCookie(f.cookie)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "capture=\"environment\"") || !strings.Contains(body, "multiple") || !strings.Contains(body, "viewport") {
		t.Fatal("mobile camera-capable form metadata missing")
	}
}
func TestHealthIsPublic(t *testing.T) {
	f := setup(t)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", "http://example.com/health", nil))
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}
func TestCostParser(t *testing.T) {
	for in, want := range map[string]int64{"$89.00": 8900, "0.5": 50, "12": 1200} {
		got, e := parseCents(in)
		if e != nil || got != want {
			t.Fatalf("%s: %d %v", in, got, e)
		}
	}
	for _, bad := range []string{"-1", "1.234", "x", "999999999999999999"} {
		if _, e := parseCents(bad); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
func TestQueryRoundTrip(t *testing.T) {
	r := httptest.NewRequest("GET", "http://x/?q=oil&from=2026-01-01&direction=asc", nil)
	q := recordQuery(r, nil)
	if q.Search != "oil" || q.Desc || q.From == nil {
		t.Fatalf("%+v", q)
	}
	_ = url.Values{}
}

func TestGoogleCallbackAcceptsAllowlistedAndFriendlyRejectsOthers(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		allowed, verified, wantSession bool
	}{
		{"allowlisted", true, true, true}, {"not allowlisted", false, true, false}, {"unverified", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := repository.NewMemory()
			a, _ := assets.NewLocalStore(t.TempDir())
			cfg := &config.Config{AppEnv: "production", AuthMode: config.AuthGoogle, SessionTTL: 90 * 24 * time.Hour, MaxUploadBytes: 25 << 20}
			svc := auth.NewService(store, cfg.SessionTTL)
			g := fakeGoogle{claims: auth.Claims{Subject: "sub", Email: "person@example.com", Name: "Person", EmailVerified: tc.verified}, allowed: tc.allowed}
			srv, e := New(cfg, store, a, svc, g)
			if e != nil {
				t.Fatal(e)
			}
			router := srv.Router()
			start := httptest.NewRecorder()
			router.ServeHTTP(start, httptest.NewRequest("GET", "https://carma.example/api/auth/google", nil))
			var state *http.Cookie
			for _, c := range start.Result().Cookies() {
				if c.Name == oauthStateCookie {
					state = c
				}
			}
			if state == nil {
				t.Fatal("missing state cookie")
			}
			cb := httptest.NewRequest("GET", "https://carma.example/api/auth/google/callback?state="+url.QueryEscape(state.Value)+"&code=ok", nil)
			cb.AddCookie(state)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, cb)
			hasSession := false
			for _, c := range w.Result().Cookies() {
				if c.Name == middleware.CookieName && c.Value != "" {
					hasSession = true
					if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
						t.Fatalf("insecure cookie: %+v", c)
					}
				}
			}
			if hasSession != tc.wantSession {
				t.Fatalf("session=%v location=%s", hasSession, w.Header().Get("Location"))
			}
			if !tc.wantSession && !strings.Contains(w.Header().Get("Location"), "not+invited") {
				t.Fatalf("unfriendly rejection: %s", w.Header().Get("Location"))
			}
		})
	}
}
