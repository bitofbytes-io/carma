package server

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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

func TestLogAgainPrefillsNewRecordAndLeavesSourceUnchanged(t *testing.T) {
	f := setup(t)
	vehicle := createVehicleWithOdometer(t, f, "Outback", "70100")
	other := createVehicleWithOdometer(t, f, "Tacoma", "12000")
	oil := serviceTypeNamed(t, f, "Oil change")
	notes := "0W-20 full synthetic, filter #7317"
	body, contentType := formBody(t, map[string]string{
		"occurred_on":     "2026-06-12",
		"service_type_id": oil.ID.String(),
		"odometer":        "62410",
		"cost":            "89.00",
		"vendor":          "Jiffy Lube",
		"notes":           notes,
	}, "receipt.pdf", []byte("%PDF-1.7\nreceipt"))
	created := f.do(t, http.MethodPost, "/vehicles/"+vehicle.ID.String()+"/records", body, contentType)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create record: %d %s", created.Code, created.Body.String())
	}
	source := onlyRecord(t, f, vehicle.ID)
	before, beforeFiles, err := f.store.GetRecord(t.Context(), source.ID)
	if err != nil || len(beforeFiles) != 1 || beforeFiles[0].OriginalFilename != "receipt.pdf" {
		t.Fatalf("source=%+v files=%+v err=%v", before, beforeFiles, err)
	}
	if current, err := f.store.GetVehicle(t.Context(), vehicle.ID); err != nil || current.CurrentOdometer == nil || *current.CurrentOdometer != 70100 {
		t.Fatalf("vehicle current odometer=%v err=%v", current.CurrentOdometer, err)
	}

	detail := f.do(t, http.MethodGet, "/records/"+source.ID.String(), nil, "")
	logAgain := "/vehicles/" + vehicle.ID.String() + "/records/new?from=" + source.ID.String()
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `<a class="secondary" href="`+logAgain+`">Log again</a>`) {
		t.Fatalf("record page missing Log again: %d %s", detail.Code, detail.Body.String())
	}

	form := f.do(t, http.MethodGet, logAgain, nil, "")
	if form.Code != http.StatusOK {
		t.Fatalf("log again: %d %s", form.Code, form.Body.String())
	}
	page := form.Body.String()
	for _, want := range []string{
		`<h1>Add record · Outback</h1>`,
		`enctype="multipart/form-data"`,
		`action="/vehicles/` + vehicle.ID.String() + `/records"`,
		`name="occurred_on" required value="2026-07-31"`,
		`<option value="` + oil.ID.String() + `" selected>Oil change</option>`,
		`name="odometer" value="70100"`,
		`name="cost" placeholder="89.00" value="$89.00"`,
		`<textarea name="notes">` + notes + `</textarea>`,
		`name="return_to" value="` + logAgain + `"`,
		`>Save record</button>`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("prefilled form missing %s\n%s", want, page)
		}
	}
	if !vendorInputValue(page, "Jiffy Lube") {
		t.Fatalf("vendor was not prefilled:\n%s", page)
	}
	for _, absent := range []string{"2026-06-12", "62410", "receipt.pdf", `action="/records/` + source.ID.String() + `"`} {
		if strings.Contains(page, absent) {
			t.Fatalf("prefilled form copied %s\n%s", absent, page)
		}
	}

	blank := f.do(t, http.MethodGet, "/vehicles/"+vehicle.ID.String()+"/records/new", nil, "").Body.String()
	if !strings.Contains(blank, `name="odometer" value=""`) || vendorInputValue(blank, "Jiffy Lube") || strings.Contains(blank, "70100") || !strings.Contains(blank, `value="/vehicles/`+vehicle.ID.String()+`/records/new"`) {
		t.Fatalf("blank form was prefilled:\n%s", blank)
	}

	rows, err := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &vehicle.ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("log again saved a record: %+v err=%v", rows, err)
	}
	after, afterFiles, err := f.store.GetRecord(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameRecord(t, before, after, beforeFiles, afterFiles)

	for _, path := range []string{
		"/vehicles/" + vehicle.ID.String() + "/records/new?from=" + uuid.NewString(),
		"/vehicles/" + vehicle.ID.String() + "/records/new?from=custom",
		"/vehicles/" + other.ID.String() + "/records/new?from=" + source.ID.String(),
	} {
		if response := f.do(t, http.MethodGet, path, nil, ""); response.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	rows, err = f.store.ListRecords(t.Context(), model.RecordQuery{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rejected log again changed records: %+v err=%v", rows, err)
	}
	after, afterFiles, err = f.store.GetRecord(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameRecord(t, before, after, beforeFiles, afterFiles)

	again, contentType := formBody(t, map[string]string{
		"occurred_on":     "2026-07-31",
		"service_type_id": oil.ID.String(),
		"odometer":        "70100",
		"cost":            "$89.00",
		"vendor":          "Jiffy Lube",
		"notes":           notes,
	}, "", nil)
	saved := f.do(t, http.MethodPost, "/vehicles/"+vehicle.ID.String()+"/records", again, contentType)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("submit: %d %s", saved.Code, saved.Body.String())
	}
	newID, err := uuid.Parse(strings.TrimPrefix(saved.Header().Get("Location"), "/records/"))
	if err != nil || newID == source.ID {
		t.Fatalf("location=%q err=%v", saved.Header().Get("Location"), err)
	}
	repeat, repeatFiles, err := f.store.GetRecord(t.Context(), newID)
	if err != nil || repeat.ServiceTypeID != oil.ID || repeat.Vendor != "Jiffy Lube" || repeat.Notes != notes || repeat.OccurredOn.Format("2006-01-02") != "2026-07-31" || repeat.OdometerMiles == nil || *repeat.OdometerMiles != 70100 || repeat.CostCents == nil || *repeat.CostCents != 8900 || len(repeatFiles) != 0 || repeat.AttachmentCount != 0 {
		t.Fatalf("saved repeat=%+v files=%+v err=%v", repeat, repeatFiles, err)
	}
	after, afterFiles, err = f.store.GetRecord(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameRecord(t, before, after, beforeFiles, afterFiles)
}

func TestLogAgainLeavesOdometerEmptyWhenVehicleHasNone(t *testing.T) {
	f := setup(t)
	vehicle := createVehicle(t, f)
	oil := serviceTypeNamed(t, f, "Oil change")
	miles := int64(62410)
	source := model.Record{ID: uuid.New(), VehicleID: vehicle.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC), OdometerMiles: &miles, Vendor: "Jiffy Lube", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if _, err := f.store.CreateRecord(t.Context(), source, nil); err != nil {
		t.Fatal(err)
	}
	before, _, err := f.store.GetRecord(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	form := f.do(t, http.MethodGet, "/vehicles/"+vehicle.ID.String()+"/records/new?from="+source.ID.String(), nil, "")
	if form.Code != http.StatusOK || !strings.Contains(form.Body.String(), `name="odometer" value=""`) || strings.Contains(form.Body.String(), "62410") || !vendorInputValue(form.Body.String(), "Jiffy Lube") {
		t.Fatalf("missing mileage was copied from the record: %d %s", form.Code, form.Body.String())
	}
	rows, err := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &vehicle.ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("records=%+v err=%v", rows, err)
	}
	after, afterFiles, err := f.store.GetRecord(t.Context(), source.ID)
	if err != nil || len(afterFiles) != 0 {
		t.Fatalf("after=%+v files=%+v err=%v", after, afterFiles, err)
	}
	if after.Vendor != before.Vendor || after.OdometerMiles == nil || *after.OdometerMiles != 62410 || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("source changed before=%+v after=%+v", before, after)
	}
}

func createVehicleWithOdometer(t *testing.T, f fixture, nickname, miles string) model.Vehicle {
	t.Helper()
	body, contentType := formBody(t, map[string]string{"nickname": nickname, "current_odometer": miles}, "", nil)
	response := f.do(t, http.MethodPost, "/vehicles", body, contentType)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create vehicle: %d %s", response.Code, response.Body.String())
	}
	vehicles, err := f.store.ListVehicles(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, vehicle := range vehicles {
		if vehicle.Nickname == nickname {
			return vehicle
		}
	}
	t.Fatalf("vehicle %s not stored", nickname)
	return model.Vehicle{}
}

func serviceTypeNamed(t *testing.T, f fixture, name string) model.ServiceType {
	t.Helper()
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, serviceType := range types {
		if serviceType.Name == name {
			return serviceType
		}
	}
	t.Fatalf("missing service type %s", name)
	return model.ServiceType{}
}

func onlyRecord(t *testing.T, f fixture, vehicleID uuid.UUID) model.Record {
	t.Helper()
	rows, err := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &vehicleID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("records=%+v err=%v", rows, err)
	}
	return rows[0]
}

func vendorInputValue(body, value string) bool {
	for _, tag := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(body, -1) {
		if strings.Contains(tag, `name="vendor"`) && strings.Contains(tag, `value="`+value+`"`) {
			return true
		}
	}
	return false
}

func assertSameRecord(t *testing.T, before, after model.Record, beforeFiles, afterFiles []model.Attachment) {
	t.Helper()
	if after.ID != before.ID || after.VehicleID != before.VehicleID || after.ServiceTypeID != before.ServiceTypeID || !after.OccurredOn.Equal(before.OccurredOn) || !sameInt64(after.OdometerMiles, before.OdometerMiles) || !sameInt64(after.CostCents, before.CostCents) || after.Vendor != before.Vendor || after.Notes != before.Notes || !after.CreatedAt.Equal(before.CreatedAt) || !after.UpdatedAt.Equal(before.UpdatedAt) || after.AttachmentCount != before.AttachmentCount {
		t.Fatalf("source record changed\nbefore=%+v\nafter=%+v", before, after)
	}
	if len(afterFiles) != len(beforeFiles) {
		t.Fatalf("attachments=%d, want %d", len(afterFiles), len(beforeFiles))
	}
	for i := range beforeFiles {
		if afterFiles[i].ID != beforeFiles[i].ID || afterFiles[i].RecordID != beforeFiles[i].RecordID || afterFiles[i].OriginalFilename != beforeFiles[i].OriginalFilename || afterFiles[i].StorageKey != beforeFiles[i].StorageKey || afterFiles[i].ByteSize != beforeFiles[i].ByteSize {
			t.Fatalf("attachment changed\nbefore=%+v\nafter=%+v", beforeFiles[i], afterFiles[i])
		}
	}
}

func sameInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
