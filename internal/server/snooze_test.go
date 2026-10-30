package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func TestSnoozeHidesOverdueReminderUntilMatchingRecord(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var oil, tires model.ServiceType
	for _, serviceType := range types {
		switch serviceType.Name {
		case "Oil change":
			oil = serviceType
		case "Tire rotation":
			tires = serviceType
		}
	}
	months := 1
	created := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	rm, err := f.store.UpsertReminder(t.Context(), model.Reminder{
		ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: oil.ID, IntervalMonths: &months,
		Enabled: true, CreatedAt: created, UpdatedAt: created,
	})
	if err != nil {
		t.Fatal(err)
	}
	if body := f.do(t, http.MethodGet, "/", nil, "").Body.String(); !strings.Contains(body, "OVERDUE") || !strings.Contains(body, oil.Name) {
		t.Fatalf("dashboard missing overdue reminder: %s", body)
	}
	page := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/reminders", nil, "").Body.String()
	for _, want := range []string{`class="status overdue"`, `aria-label="Snooze Oil change reminder"`, `value="2w"`, `value="1m"`, `value="500mi"`, `value="custom"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("reminders page missing %q", want)
		}
	}

	snooze := url.Values{"preset": {"2w"}}
	response := f.do(t, http.MethodPost, "/vehicles/"+v.ID.String()+"/reminders/"+rm.ID.String()+"/snooze", strings.NewReader(snooze.Encode()), "application/x-www-form-urlencoded")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("snooze: %d %s", response.Code, response.Body.String())
	}
	page = f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/reminders", nil, "").Body.String()
	if !strings.Contains(page, "snoozed until Aug 14, 2026") || !strings.Contains(page, `class="status snoozed"`) || !strings.Contains(page, "Clear snooze") {
		t.Fatalf("snoozed reminder: %s", page)
	}
	vehiclePage := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String(), nil, "").Body.String()
	if !strings.Contains(vehiclePage, "snoozed until Aug 14, 2026") || strings.Contains(vehiclePage, `class="status overdue"`) {
		t.Fatalf("vehicle page: %s", vehiclePage)
	}
	if body := f.do(t, http.MethodGet, "/", nil, "").Body.String(); strings.Contains(body, "OVERDUE") || !strings.Contains(body, "Nothing needs attention right now") {
		t.Fatalf("snoozed reminder stayed on the dashboard: %s", body)
	}

	update := url.Values{"service_type_id": {oil.ID.String()}, "months": {"2"}, "enabled": {"true"}}
	updateReq := httptest.NewRequest(http.MethodPost, "http://example.com/vehicles/"+v.ID.String()+"/reminders", strings.NewReader(update.Encode()))
	updateReq.AddCookie(f.cookie)
	updateReq.Header.Set("Origin", "http://example.com")
	updateReq.Header.Set("HX-Request", "true")
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateW := httptest.NewRecorder()
	f.router.ServeHTTP(updateW, updateReq)
	rows, err := f.store.ListReminders(t.Context(), &v.ID, true)
	if err != nil || updateW.Code != http.StatusOK || len(rows) != 1 || rows[0].IntervalMonths == nil || *rows[0].IntervalMonths != 2 || rows[0].SnoozedUntilOn == nil || rows[0].SnoozedUntilOn.Format(time.DateOnly) != "2026-08-14" {
		t.Fatalf("saving the interval cleared the snooze: code=%d rows=%+v err=%v body=%s", updateW.Code, rows, err, updateW.Body.String())
	}

	other := model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: tires.ID, CreatedBy: f.user.ID, OccurredOn: created, CreatedAt: f.s.now(), UpdatedAt: f.s.now()}
	if _, err = f.store.CreateRecord(t.Context(), other, nil); err != nil {
		t.Fatal(err)
	}
	rows, _ = f.store.ListReminders(t.Context(), &v.ID, true)
	if len(rows) != 1 || rows[0].SnoozedUntilOn == nil {
		t.Fatalf("unrelated record cleared the snooze: %+v", rows)
	}

	match := model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: created, CreatedAt: f.s.now().Add(time.Second), UpdatedAt: f.s.now().Add(time.Second)}
	if _, err = f.store.CreateRecord(t.Context(), match, nil); err != nil {
		t.Fatal(err)
	}
	rows, _ = f.store.ListReminders(t.Context(), &v.ID, true)
	if len(rows) != 1 || rows[0].SnoozedUntilOn != nil || rows[0].SnoozedUntilMiles != nil {
		t.Fatalf("matching record left the snooze in place: %+v", rows)
	}
	if body := f.do(t, http.MethodGet, "/", nil, "").Body.String(); !strings.Contains(body, "OVERDUE") {
		t.Fatalf("cleared snooze did not return the overdue reminder: %s", body)
	}
}

func TestSnoozePresetsCustomAndValidation(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	if response := postOdometer(t, f, v.ID, url.Values{"odometer_miles": {"10000"}}, false); response.Code != http.StatusSeeOther {
		t.Fatalf("odometer: %d %s", response.Code, response.Body.String())
	}
	types, _ := f.store.ListServiceTypes(t.Context())
	miles := int64(1000)
	start := int64(1000)
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	rm, err := f.store.UpsertReminder(t.Context(), model.Reminder{
		ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMiles: &miles,
		StartingOdometer: &start, Enabled: true, CreatedAt: created, UpdatedAt: created,
	})
	if err != nil {
		t.Fatal(err)
	}
	snoozeURL := "/vehicles/" + v.ID.String() + "/reminders/" + rm.ID.String() + "/snooze"
	post := func(values url.Values, htmx bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://example.com"+snoozeURL, strings.NewReader(values.Encode()))
		request.AddCookie(f.cookie)
		request.Header.Set("Origin", "http://example.com")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if htmx {
			request.Header.Set("HX-Request", "true")
		}
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, request)
		return response
	}

	missing := post(url.Values{"preset": {"nope"}}, false)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "Choose a snooze option.") {
		t.Fatalf("unknown preset: %d %s", missing.Code, missing.Body.String())
	}
	empty := post(url.Values{"preset": {"custom"}}, true)
	if empty.Code != http.StatusOK || strings.Contains(empty.Body.String(), "<html") || !strings.Contains(empty.Body.String(), "Choose a date or a mileage.") || !strings.Contains(empty.Body.String(), `id="snooze-`+rm.ID.String()+`" aria-controls="snooze-panel-`+rm.ID.String()+`" checked`) {
		t.Fatalf("empty custom snooze: %d %s", empty.Code, empty.Body.String())
	}
	low := post(url.Values{"preset": {"custom"}, "until_miles": {"10000"}}, false)
	if low.Code != http.StatusBadRequest || !strings.Contains(low.Body.String(), "Snooze mileage must be above the current reading.") {
		t.Fatalf("low mileage: %d %s", low.Code, low.Body.String())
	}
	past := post(url.Values{"preset": {"custom"}, "until_on": {"2026-07-31"}}, false)
	if past.Code != http.StatusBadRequest || !strings.Contains(past.Body.String(), "Choose a snooze date after today.") {
		t.Fatalf("past date: %d %s", past.Code, past.Body.String())
	}

	plus := post(url.Values{"preset": {"500mi"}}, false)
	if plus.Code != http.StatusSeeOther {
		t.Fatalf("+500: %d %s", plus.Code, plus.Body.String())
	}
	page := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/reminders", nil, "").Body.String()
	if !strings.Contains(page, "snoozed until 10,500 mi") {
		t.Fatalf("+500 result: %s", page)
	}
	if body := f.do(t, http.MethodGet, "/", nil, "").Body.String(); strings.Contains(body, "OVERDUE") {
		t.Fatalf("mileage snooze stayed overdue: %s", body)
	}

	custom := post(url.Values{"preset": {"custom"}, "until_on": {"2026-11-13"}, "until_miles": {"15500"}}, false)
	if custom.Code != http.StatusSeeOther {
		t.Fatalf("custom: %d %s", custom.Code, custom.Body.String())
	}
	rows, _ := f.store.ListReminders(t.Context(), &v.ID, true)
	if len(rows) != 1 || rows[0].SnoozedUntilOn == nil || rows[0].SnoozedUntilOn.Format(time.DateOnly) != "2026-11-13" || rows[0].SnoozedUntilMiles == nil || *rows[0].SnoozedUntilMiles != 15500 {
		t.Fatalf("custom snooze stored %+v", rows)
	}
	page = f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/reminders", nil, "").Body.String()
	if !strings.Contains(page, "snoozed until Nov 13, 2026 and 15,500 mi") {
		t.Fatalf("custom detail: %s", page)
	}

	cleared := post(url.Values{"preset": {"clear"}}, true)
	if cleared.Code != http.StatusOK || strings.Contains(cleared.Body.String(), "snoozed until") {
		t.Fatalf("clear: %d %s", cleared.Code, cleared.Body.String())
	}
	rows, _ = f.store.ListReminders(t.Context(), &v.ID, true)
	if len(rows) != 1 || rows[0].SnoozedUntilOn != nil || rows[0].SnoozedUntilMiles != nil {
		t.Fatalf("clear left %+v", rows)
	}
	if response := f.do(t, http.MethodPost, "/vehicles/"+v.ID.String()+"/reminders/"+uuid.NewString()+"/snooze", strings.NewReader(url.Values{"preset": {"2w"}}.Encode()), "application/x-www-form-urlencoded"); response.Code != http.StatusNotFound {
		t.Fatalf("unknown reminder: %d", response.Code)
	}
}
