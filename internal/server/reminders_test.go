package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func TestCreateVehicleSeedsCheckedReminderPresets(t *testing.T) {
	f := setup(t)
	page := f.do(t, http.MethodGet, "/vehicles/new", nil, "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="reminder_preset" value="Oil change"`) {
		t.Fatalf("new vehicle form status=%d body=%s", page.Code, page.Body.String())
	}

	presetForm := func(fields map[string]string, presets ...string) (*bytes.Buffer, string) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		for k, v := range fields {
			_ = mw.WriteField(k, v)
		}
		for _, preset := range presets {
			_ = mw.WriteField("reminder_preset", preset)
		}
		_ = mw.Close()
		return &b, mw.FormDataContentType()
	}

	b, ct := presetForm(map[string]string{"current_odometer": "1200"}, "Oil change")
	response := f.do(t, http.MethodPost, "/vehicles", b, ct)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `value="Oil change" checked>`) {
		t.Fatalf("invalid form should keep preset checks: %d %s", response.Code, response.Body.String())
	}

	b, ct = presetForm(map[string]string{"nickname": "New car", "current_odometer": "1200"}, "Oil change", "Tire rotation", "oil change", "Repair", "Bogus")
	response = f.do(t, http.MethodPost, "/vehicles", b, ct)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create vehicle: %d %s", response.Code, response.Body.String())
	}
	vehicles, _ := f.store.ListVehicles(t.Context(), false)
	if len(vehicles) != 1 {
		t.Fatalf("vehicles=%+v", vehicles)
	}
	rows, err := f.store.ListReminders(t.Context(), &vehicles[0].ID, true)
	if err != nil || len(rows) != 2 {
		t.Fatalf("reminders=%+v err=%v", rows, err)
	}
	byName := map[string]model.Reminder{}
	for _, row := range rows {
		byName[row.ServiceTypeName] = row
		if !row.Enabled || row.StartingOdometer == nil || *row.StartingOdometer != 1200 || !row.CreatedAt.Equal(f.s.now()) {
			t.Fatalf("preset reminder baseline=%+v", row)
		}
	}
	oil, rotation := byName["Oil change"], byName["Tire rotation"]
	if oil.IntervalMonths == nil || *oil.IntervalMonths != 6 || oil.IntervalMiles == nil || *oil.IntervalMiles != 5000 {
		t.Fatalf("oil change preset=%+v", oil)
	}
	if rotation.IntervalMonths != nil || rotation.IntervalMiles == nil || *rotation.IntervalMiles != 7500 {
		t.Fatalf("tire rotation preset=%+v", rotation)
	}
}

func TestMileagePresetWithoutOdometerStartsAtFirstReading(t *testing.T) {
	f := setup(t)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	_ = mw.WriteField("nickname", "No mileage yet")
	_ = mw.WriteField("reminder_preset", "Tire rotation")
	_ = mw.WriteField("reminder_preset", "Cabin air filter")
	_ = mw.Close()
	if response := f.do(t, http.MethodPost, "/vehicles", &b, mw.FormDataContentType()); response.Code != http.StatusSeeOther {
		t.Fatalf("create vehicle: %d %s", response.Code, response.Body.String())
	}
	vehicles, _ := f.store.ListVehicles(t.Context(), false)
	v := vehicles[0]
	rows, _ := f.store.ListReminders(t.Context(), &v.ID, true)
	pending := map[string]bool{}
	for _, row := range rows {
		if row.StartingOdometer != nil {
			t.Fatalf("preset has a starting odometer without one entered: %+v", row)
		}
		pending[row.ServiceTypeName] = row.StartingOdometerPending
	}
	if len(rows) != 2 || !pending["Tire rotation"] || pending["Cabin air filter"] {
		t.Fatalf("pending flags=%v rows=%+v", pending, rows)
	}

	if response := postOdometer(t, f, v.ID, url.Values{"odometer_miles": {"30000"}}, false); response.Code != http.StatusSeeOther {
		t.Fatalf("odometer: %d %s", response.Code, response.Body.String())
	}
	rows, _ = f.store.ListReminders(t.Context(), &v.ID, true)
	for _, row := range rows {
		if row.ServiceTypeName == "Tire rotation" && (row.StartingOdometerPending || row.StartingOdometer == nil || *row.StartingOdometer != 30000) {
			t.Fatalf("first reading did not start the mileage preset: %+v", row)
		}
	}
}

func TestNewReminderSnapshotsVehicleMileageAndIsNotImmediatelyOverdue(t *testing.T) {
	f := setup(t)
	b, ct := formBody(t, map[string]string{"nickname": "New car", "current_odometer": "1200"}, "", nil)
	response := f.do(t, http.MethodPost, "/vehicles", b, ct)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create vehicle: %d %s", response.Code, response.Body.String())
	}
	vehicles, _ := f.store.ListVehicles(t.Context(), false)
	v := vehicles[0]
	types, _ := f.store.ListServiceTypes(t.Context())
	values := url.Values{"service_type_id": {types[0].ID.String()}, "months": {"6"}, "miles": {"5000"}, "enabled": {"true"}}
	response = f.do(t, http.MethodPost, "/vehicles/"+v.ID.String()+"/reminders", strings.NewReader(values.Encode()), "application/x-www-form-urlencoded")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create reminder: %d %s", response.Code, response.Body.String())
	}
	rows, _ := f.store.ListReminders(t.Context(), &v.ID, true)
	if len(rows) != 1 || rows[0].StartingOdometer == nil || *rows[0].StartingOdometer != 1200 || rows[0].StartingOdometerPending || !rows[0].CreatedAt.Equal(f.s.now()) {
		t.Fatalf("reminder snapshot=%+v", rows)
	}
	dashboard := f.do(t, http.MethodGet, "/", nil, "")
	if strings.Contains(dashboard.Body.String(), "OVERDUE") || !strings.Contains(dashboard.Body.String(), "Nothing needs attention right now") {
		t.Fatalf("new reminder was immediately due: %s", dashboard.Body.String())
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
	assertHTMXConfirmedForm(t, edit.Body.String(), "/vehicles/"+v.ID.String()+"/archive", "Archive this vehicle?")
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

func TestTodayFollowsConfiguredTimeZone(t *testing.T) {
	f := setup(t)
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	f.s.cfg.Location = newYork
	// 23:30 on July 30 in New York is 03:30 on July 31 in UTC.
	now := time.Date(2026, 7, 30, 23, 30, 0, 0, newYork)
	f.s.now = func() time.Time { return now.UTC() }
	v := createVehicle(t, f)
	form := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/records/new", nil, "")
	if !strings.Contains(form.Body.String(), `value="2026-07-30"`) {
		t.Fatalf("new record did not default to the local date: %s", form.Body.String())
	}
	types, _ := f.store.ListServiceTypes(t.Context())
	months := 6
	baseline := model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), CreatedAt: now}
	if _, err = f.store.CreateRecord(t.Context(), baseline, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpsertReminder(t.Context(), model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMonths: &months, Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if body := f.do(t, http.MethodGet, "/", nil, "").Body.String(); strings.Contains(body, "OVERDUE") || !strings.Contains(body, "DUE SOON") {
		t.Fatalf("reminder due July 31 local was not due soon at 23:30 July 30 local: %s", body)
	}
	f.s.now = func() time.Time { return now.Add(time.Hour).UTC() }
	if body := f.do(t, http.MethodGet, "/", nil, "").Body.String(); !strings.Contains(body, "OVERDUE") {
		t.Fatalf("reminder due July 31 local was not overdue at 00:30 July 31 local: %s", body)
	}
}
