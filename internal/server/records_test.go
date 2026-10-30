package server

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

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

func TestRecordEditUsesBrowserFormEncoding(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	odo := int64(100)
	cost := int64(500)
	rec := model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), OdometerMiles: &odo, CostCents: &cost, Vendor: "Before", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_, _ = f.store.CreateRecord(t.Context(), rec, nil)
	form := f.do(t, "GET", "/records/"+rec.ID.String()+"/edit", nil, "")
	if form.Code != 200 || strings.Contains(form.Body.String(), `enctype="multipart/form-data"`) {
		t.Fatalf("edit form must be urlencoded: %d %s", form.Code, form.Body.String())
	}
	values := url.Values{"occurred_on": {"2026-02-03"}, "service_type_id": {types[1].ID.String()}, "odometer": {"250"}, "cost": {"12.34"}, "vendor": {"After"}, "notes": {"Edited through form"}}
	w := f.do(t, "POST", "/records/"+rec.ID.String(), strings.NewReader(values.Encode()), "application/x-www-form-urlencoded")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	updated, _, err := f.store.GetRecord(t.Context(), rec.ID)
	if err != nil || updated.Vendor != "After" || updated.Notes != "Edited through form" || updated.ServiceTypeID != types[1].ID || updated.OdometerMiles == nil || *updated.OdometerMiles != 250 || updated.CostCents == nil || *updated.CostCents != 1234 || updated.OccurredOn.Format("2006-01-02") != "2026-02-03" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestCreateServiceTypeAllowsOnlySafeReturnTargets(t *testing.T) {
	f := setup(t)
	for i, tc := range []struct {
		name, target, want string
	}{
		{name: "safe relative", target: "/vehicles/123/records/new?from=custom", want: "/vehicles/123/records/new?from=custom"},
		{name: "external", target: "https://evil.example/steal", want: "/"},
		{name: "scheme relative", target: "//evil.example/steal", want: "/"},
		{name: "encoded slash", target: `/%2f%2fevil.example/steal`, want: "/"},
		{name: "backslash", target: `/\evil.example/steal`, want: "/"},
		{name: "encoded backslash", target: `/%5c%5cevil.example/steal`, want: "/"},
		{name: "malformed", target: `/%zz`, want: "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := url.Values{"name": {"Return Target " + strconv.Itoa(i)}, "return_to": {tc.target}}
			response := f.do(t, http.MethodPost, "/service-types", strings.NewReader(values.Encode()), "application/x-www-form-urlencoded")
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != tc.want {
				t.Fatalf("target=%q status=%d location=%q", tc.target, response.Code, response.Header().Get("Location"))
			}
		})
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
	for _, want := range []string{`<h1>All records</h1>`, `hx-get="/records"`, `href="/records/export.csv?"`, `href="/records/export.xlsx?"`} {
		if !strings.Contains(global, want) {
			t.Fatalf("global records missing %s", want)
		}
	}
	req := httptest.NewRequest("GET", "http://example.com/vehicles/"+v.ID.String()+"?q=Beta", nil)
	req.AddCookie(f.cookie)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	body = w.Body.String()
	if strings.Contains(body, "<html") || !strings.Contains(body, `id="records-list"`) || !strings.Contains(body, types[1].Name) || strings.Count(body, `class="record-row" href=`) != 1 || strings.Contains(body, "ZgotmplZ") || !strings.Contains(body, "export.csv?q=Beta") || !strings.Contains(body, "export.xlsx?q=Beta") {
		t.Fatalf("bad partial: %s", body)
	}
}

func TestConfirmedDestructivePostsRedirectHTMXRequests(t *testing.T) {
	f := setup(t)
	record, attachment := createAttachment(t, f)
	for _, test := range []struct{ path, target string }{
		{"/attachments/" + attachment.ID.String() + "/delete", "/records/" + record.ID.String()},
		{"/records/" + record.ID.String() + "/delete", "/vehicles/" + record.VehicleID.String()},
		{"/vehicles/" + record.VehicleID.String() + "/archive", "/"},
	} {
		request := httptest.NewRequest(http.MethodPost, "http://example.com"+test.path, nil)
		request.AddCookie(f.cookie)
		request.Header.Set("Origin", "http://example.com")
		request.Header.Set("HX-Request", "true")
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("HX-Redirect") != test.target || response.Header().Get("Location") != "" {
			t.Fatalf("%s: status=%d hx-redirect=%q location=%q", test.path, response.Code, response.Header().Get("HX-Redirect"), response.Header().Get("Location"))
		}
	}
	if _, _, err := f.store.GetRecord(t.Context(), record.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("record not deleted: %v", err)
	}
	if archived, _ := f.store.ListVehicles(t.Context(), true); len(archived) != 1 {
		t.Fatalf("vehicle not archived: %+v", archived)
	}
}

func TestRecordFormSuggestsPreviousVendors(t *testing.T) {
	f := setup(t)
	outback := createVehicle(t, f)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tacoma := model.Vehicle{ID: uuid.New(), Nickname: "Tacoma", CreatedAt: now, UpdatedAt: now}
	if _, err := f.store.CreateVehicle(t.Context(), tacoma, nil); err != nil {
		t.Fatal(err)
	}
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil || len(types) < 2 {
		t.Fatalf("service types=%v err=%v", types, err)
	}
	oil, repair := types[0], types[1]
	quoted := `O'Reilly & "Sons"`
	latestJiffy := uuid.New()
	for _, row := range []model.Record{
		{ID: latestJiffy, VehicleID: outback.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), Vendor: "Jiffy Lube", CreatedAt: now.Add(4 * time.Hour)},
		{ID: uuid.New(), VehicleID: outback.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), Vendor: "Jiffy Lube", CreatedAt: now.Add(time.Hour)},
		{ID: uuid.New(), VehicleID: outback.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC), Vendor: "jiffy lube downtown", CreatedAt: now.Add(2 * time.Hour)},
		{ID: uuid.New(), VehicleID: outback.ID, ServiceTypeID: repair.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), Vendor: "Tire Barn", CreatedAt: now.Add(3 * time.Hour)},
		{ID: uuid.New(), VehicleID: tacoma.ID, ServiceTypeID: repair.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Vendor: "Quick Lane", CreatedAt: now.Add(3 * time.Hour)},
		{ID: uuid.New(), VehicleID: outback.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Vendor: "   ", CreatedAt: now.Add(5 * time.Hour)},
		{ID: uuid.New(), VehicleID: outback.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Vendor: quoted, CreatedAt: now},
	} {
		if _, err := f.store.CreateRecord(t.Context(), row, nil); err != nil {
			t.Fatal(err)
		}
	}

	body := f.do(t, http.MethodGet, "/vehicles/"+outback.ID.String()+"/records/new", nil, "").Body.String()
	list := `<datalist id="vendor-list">` +
		`<option value="Jiffy Lube"></option>` +
		`<option value="Tire Barn"></option>` +
		`<option value="Quick Lane"></option>` +
		`<option value="jiffy lube downtown"></option>` +
		`<option value="O&#39;Reilly &amp; &#34;Sons&#34;"></option>` +
		`</datalist>`
	if !strings.Contains(body, `<input name="vendor" list="vendor-list" autocomplete="off" value="">`) || !strings.Contains(body, list) {
		t.Fatalf("new record form missing vendor suggestions: %s", body)
	}

	edit := f.do(t, http.MethodGet, "/records/"+latestJiffy.String()+"/edit", nil, "").Body.String()
	if !strings.Contains(edit, `<input name="vendor" list="vendor-list" autocomplete="off" value="Jiffy Lube">`) || !strings.Contains(edit, list) {
		t.Fatalf("edit form missing vendor suggestions: %s", edit)
	}
}

func TestCostParser(t *testing.T) {
	for in, want := range map[string]int64{"$89.00": 8900, "0.5": 50, "12": 1200} {
		got, e := model.ParseCents(in)
		if e != nil || got != want {
			t.Fatalf("%s: %d %v", in, got, e)
		}
	}
	for _, bad := range []string{"-1", "-0.50", "1.234", "x", "999999999999999999"} {
		if _, e := model.ParseCents(bad); e == nil {
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
