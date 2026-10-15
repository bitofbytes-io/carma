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

func postOdometer(t *testing.T, f fixture, vehicleID uuid.UUID, fields url.Values, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://example.com/vehicles/"+vehicleID.String()+"/odometer", strings.NewReader(fields.Encode()))
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

func TestOdometerUpdateFromDashboardSwapsCardAndAttention(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	miles := int64(5000)
	if _, err := f.store.UpsertReminder(t.Context(), model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMiles: &miles, StartingOdometerPending: true, Enabled: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AddOdometerReading(t.Context(), model.OdometerReading{ID: uuid.New(), VehicleID: v.ID, ReadOn: f.s.today(), OdometerMiles: 10000, CreatedBy: f.user.ID, CreatedAt: time.Now()}, false); err != nil {
		t.Fatal(err)
	}

	dashboard := f.do(t, http.MethodGet, "/", nil, "")
	body := dashboard.Body.String()
	for _, want := range []string{`id="attention"`, `id="vehicle-card-` + v.ID.String() + `"`, `hx-post="/vehicles/` + v.ID.String() + `/odometer"`, `hx-target="#vehicle-card-` + v.ID.String() + `"`, `name="odometer_miles"`, "Nothing needs attention right now"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %s: %s", want, body)
		}
	}

	response := postOdometer(t, f, v.ID, url.Values{"odometer_miles": {"15200"}, "return": {"dashboard"}}, true)
	body = response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "<html") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	for _, want := range []string{`id="vehicle-card-` + v.ID.String() + `"`, "15200 mi", `hx-swap-oob="innerHTML:#attention"`, "OVERDUE"} {
		if !strings.Contains(body, want) {
			t.Fatalf("htmx response missing %s: %s", want, body)
		}
	}
	updated, _ := f.store.GetVehicle(t.Context(), v.ID)
	if updated.LatestOdometer == nil || *updated.LatestOdometer != 15200 {
		t.Fatalf("vehicle mileage=%v, want 15200", updated.LatestOdometer)
	}
}

func TestMileageProjectionOnDashboardAndReminders(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	miles := int64(5000)
	if _, err := f.store.UpsertReminder(t.Context(), model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMiles: &miles, StartingOdometerPending: true, Enabled: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	today := f.s.today()
	addReading := func(readOn time.Time, miles int64) {
		t.Helper()
		if _, err := f.store.AddOdometerReading(t.Context(), model.OdometerReading{ID: uuid.New(), VehicleID: v.ID, ReadOn: readOn, OdometerMiles: miles, CreatedBy: f.user.ID, CreatedAt: time.Now()}, false); err != nil {
			t.Fatal(err)
		}
	}
	// The first reading starts the 5000 mi interval at 10000 mi.
	addReading(today.AddDate(0, 0, -60), 10000)
	page := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/reminders", nil, "").Body.String()
	if !strings.Contains(page, "~5,000 mi left · log mileage again to estimate a date") {
		t.Fatalf("reminders page missing estimate hint: %s", page)
	}

	// Service records count as mileage history too. Fitting 10000, 13000, and
	// 14000 mi at 60, 45, and 0 days ago gives about 56 mi a day, so the last
	// 1000 mi are about 18 days away: due soon by date.
	recordMiles := int64(13000)
	other := model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[1].ID, CreatedBy: f.user.ID, OccurredOn: today.AddDate(0, 0, -45), OdometerMiles: &recordMiles, CreatedAt: time.Now()}
	if _, err := f.store.CreateRecord(t.Context(), other, nil); err != nil {
		t.Fatal(err)
	}
	addReading(today, 14000)
	want := "~1,000 mi left, est. " + today.AddDate(0, 0, 18).Format("Jan 2")
	dashboard := f.do(t, http.MethodGet, "/", nil, "").Body.String()
	if !strings.Contains(dashboard, "DUE SOON") || !strings.Contains(dashboard, want) {
		t.Fatalf("dashboard missing projection %q: %s", want, dashboard)
	}
	page = f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/reminders", nil, "").Body.String()
	if !strings.Contains(page, want) || !strings.Contains(page, "due-soon") {
		t.Fatalf("reminders page missing projection %q: %s", want, page)
	}
}

func TestVehicleMileageFeedsProjection(t *testing.T) {
	f := setup(t)
	saveVehicle := func(path, mileage string) {
		t.Helper()
		b, ct := formBody(t, map[string]string{"nickname": "Ranger", "year": "2026", "make": "Ford", "model": "Ranger", "current_odometer": mileage}, "", nil)
		if response := f.do(t, http.MethodPost, path, b, ct); response.Code != http.StatusSeeOther {
			t.Fatalf("save vehicle status=%d body=%s", response.Code, response.Body.String())
		}
	}
	saveVehicle("/vehicles", "1102")
	vehicles, _ := f.store.ListVehicles(t.Context(), false)
	v := vehicles[0]
	types, _ := f.store.ListServiceTypes(t.Context())
	miles := int64(10000)
	if _, err := f.store.UpsertReminder(t.Context(), model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMiles: &miles, StartingOdometer: v.LatestOdometer, Enabled: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	reminders := func() string {
		t.Helper()
		return f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/reminders", nil, "").Body.String()
	}
	today := f.s.today()
	// The vehicle's mileage is one dated point, so the hint says when a second
	// one can give a driving rate.
	want := "~10,000 mi left · log mileage again on or after " + today.AddDate(0, 0, 14).Format("Jan 2") + " to estimate a date"
	if page := reminders(); !strings.Contains(page, want) {
		t.Fatalf("reminders page missing %q: %s", want, page)
	}

	// An older service record pairs with the vehicle's mileage: 900 mi in 30
	// days is 30 mi a day, so 10,000 mi are about 333 days away.
	recordMiles := int64(202)
	record := model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[1].ID, CreatedBy: f.user.ID, OccurredOn: today.AddDate(0, 0, -30), OdometerMiles: &recordMiles, CreatedAt: time.Now()}
	if _, err := f.store.CreateRecord(t.Context(), record, nil); err != nil {
		t.Fatal(err)
	}
	want = "~10,000 mi left, est. " + today.AddDate(0, 0, 333).Format("Jan 2, 2006")
	if page := reminders(); !strings.Contains(page, want) {
		t.Fatalf("reminders page missing %q: %s", want, page)
	}

	// Saving the vehicle later without changing its mileage keeps its date.
	now := f.s.now
	f.s.now = func() time.Time { return now().AddDate(0, 0, 10) }
	saveVehicle("/vehicles/"+v.ID.String(), "1102")
	points, err := f.store.ListMileagePoints(t.Context(), []uuid.UUID{v.ID}, today.AddDate(-1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || !points[1].On.Equal(today) || points[1].Miles != 1102 {
		t.Fatalf("points after unchanged save=%+v", points)
	}
	// Changing it dates it on the day it was entered and keeps the old point.
	saveVehicle("/vehicles/"+v.ID.String(), "1402")
	points, _ = f.store.ListMileagePoints(t.Context(), []uuid.UUID{v.ID}, today.AddDate(-1, 0, 0))
	if len(points) != 3 || !points[1].On.Equal(today) || points[1].Miles != 1102 || !points[2].On.Equal(today.AddDate(0, 0, 10)) || points[2].Miles != 1402 {
		t.Fatalf("points after mileage change=%+v", points)
	}

	// A reading that raises the mileage moves its date, keeps the old point,
	// and is not counted twice.
	if _, err := f.store.AddOdometerReading(t.Context(), model.OdometerReading{ID: uuid.New(), VehicleID: v.ID, ReadOn: today.AddDate(0, 0, 12), OdometerMiles: 1500, CreatedBy: f.user.ID, CreatedAt: time.Now()}, false); err != nil {
		t.Fatal(err)
	}
	points, _ = f.store.ListMileagePoints(t.Context(), []uuid.UUID{v.ID}, today.AddDate(-1, 0, 0))
	if len(points) != 4 || points[2].Miles != 1402 || !points[3].On.Equal(today.AddDate(0, 0, 12)) || points[3].Miles != 1500 {
		t.Fatalf("points after reading=%+v", points)
	}
}

func TestCheckInAfterVehicleMileageGivesProjection(t *testing.T) {
	f := setup(t)
	b, ct := formBody(t, map[string]string{"nickname": "Ranger", "current_odometer": "1102"}, "", nil)
	if response := f.do(t, http.MethodPost, "/vehicles", b, ct); response.Code != http.StatusSeeOther {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	vehicles, _ := f.store.ListVehicles(t.Context(), false)
	v := vehicles[0]
	types, _ := f.store.ListServiceTypes(t.Context())
	miles := int64(10000)
	if _, err := f.store.UpsertReminder(t.Context(), model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMiles: &miles, StartingOdometer: v.LatestOdometer, Enabled: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	created := f.s.today()

	// Following the hint: check in 20 days later. 600 mi in 20 days is 30 mi
	// a day, so the remaining 9,400 mi are about 313 days away.
	now := f.s.now
	f.s.now = func() time.Time { return now().AddDate(0, 0, 20) }
	if response := postOdometer(t, f, v.ID, url.Values{"odometer_miles": {"1702"}}, false); response.Code != http.StatusSeeOther {
		t.Fatalf("check-in status=%d body=%s", response.Code, response.Body.String())
	}
	points, err := f.store.ListMileagePoints(t.Context(), []uuid.UUID{v.ID}, created.AddDate(-1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || !points[0].On.Equal(created) || points[0].Miles != 1102 || points[1].Miles != 1702 {
		t.Fatalf("points=%+v, want the vehicle's 1102 mi and the 1702 mi check-in", points)
	}
	today := f.s.today()
	want := "~9,400 mi left, est. " + today.AddDate(0, 0, 313).Format("Jan 2, 2006")
	if page := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/reminders", nil, "").Body.String(); !strings.Contains(page, want) {
		t.Fatalf("reminders page missing %q: %s", want, page)
	}
}

func TestVehicleSaveDatesMileageCopiedFromRecord(t *testing.T) {
	f := setup(t)
	saveVehicle := func(path, mileage string) {
		t.Helper()
		b, ct := formBody(t, map[string]string{"nickname": "Ranger", "current_odometer": mileage}, "", nil)
		if response := f.do(t, http.MethodPost, path, b, ct); response.Code != http.StatusSeeOther {
			t.Fatalf("save vehicle status=%d body=%s", response.Code, response.Body.String())
		}
	}
	saveVehicle("/vehicles", "1000")
	vehicles, _ := f.store.ListVehicles(t.Context(), false)
	v := vehicles[0]
	today := f.s.today()
	types, _ := f.store.ListServiceTypes(t.Context())
	recordMiles := int64(1500)
	recordOn := today.AddDate(0, 0, 20)
	if _, err := f.store.CreateRecord(t.Context(), model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, CreatedBy: f.user.ID, OccurredOn: recordOn, OdometerMiles: &recordMiles, CreatedAt: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	// The edit form shows the record's higher mileage; saving it a week later
	// must date it by the record, not keep the 1000 mi date or use today.
	now := f.s.now
	f.s.now = func() time.Time { return now().AddDate(0, 0, 27) }
	saveVehicle("/vehicles/"+v.ID.String(), "1500")
	points, err := f.store.ListMileagePoints(t.Context(), []uuid.UUID{v.ID}, today.AddDate(-1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || !points[0].On.Equal(today) || points[0].Miles != 1000 || !points[1].On.Equal(recordOn) || points[1].Miles != 1500 {
		t.Fatalf("points=%+v, want 1000 mi on %s and the record's 1500 mi on %s", points, today.Format("Jan 2"), recordOn.Format("Jan 2"))
	}
}

func TestOdometerUpdateRejectsLowerReadingUntilConfirmed(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	if response := postOdometer(t, f, v.ID, url.Values{"odometer_miles": {"62410"}}, false); response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
		t.Fatalf("first reading status=%d location=%q", response.Code, response.Header().Get("Location"))
	}

	response := postOdometer(t, f, v.ID, url.Values{"odometer_miles": {"6241"}, "return": {"dashboard"}}, true)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "lower than the latest 62410 mi") || !strings.Contains(body, `name="confirm" value="1"`) || !strings.Contains(body, `value="6241"`) || !strings.Contains(body, "Save anyway") {
		t.Fatalf("lower htmx reading status=%d body=%s", response.Code, body)
	}

	response = postOdometer(t, f, v.ID, url.Values{"odometer_miles": {"6241"}, "return": {"vehicle"}}, false)
	body = response.Body.String()
	if response.Code != http.StatusConflict || !strings.Contains(body, "<html") || !strings.Contains(body, `name="confirm" value="1"`) || !strings.Contains(body, `href="/vehicles/`+v.ID.String()+`"`) {
		t.Fatalf("lower reading page status=%d body=%s", response.Code, body)
	}

	response = postOdometer(t, f, v.ID, url.Values{"odometer_miles": {"6241"}, "return": {"vehicle"}, "confirm": {"1"}}, false)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/vehicles/"+v.ID.String() {
		t.Fatalf("confirmed reading status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	updated, _ := f.store.GetVehicle(t.Context(), v.ID)
	if updated.LatestOdometer == nil || *updated.LatestOdometer != 62410 {
		t.Fatalf("confirmed lower reading changed mileage to %v", updated.LatestOdometer)
	}
}

func TestOdometerUpdateFromVehiclePage(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	page := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String(), nil, "")
	if !strings.Contains(page.Body.String(), `action="/vehicles/`+v.ID.String()+`/odometer"`) || !strings.Contains(page.Body.String(), `name="return" value="vehicle"`) {
		t.Fatalf("vehicle page missing mileage form: %s", page.Body.String())
	}

	response := postOdometer(t, f, v.ID, url.Values{"odometer_miles": {"oops"}, "return": {"vehicle"}}, true)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Mileage must be a nonnegative whole number.") || strings.Contains(body, "vehicle-card") || strings.Contains(body, "<html") {
		t.Fatalf("invalid reading status=%d body=%s", response.Code, body)
	}
	for _, invalid := range []string{"", "-1", "1.5"} {
		response = postOdometer(t, f, v.ID, url.Values{"odometer_miles": {invalid}}, false)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Mileage must be a nonnegative whole number.") {
			t.Fatalf("odometer %q status=%d", invalid, response.Code)
		}
	}

	response = postOdometer(t, f, v.ID, url.Values{"odometer_miles": {" 1200 "}, "return": {"vehicle"}}, true)
	if response.Code != http.StatusOK || response.Header().Get("HX-Redirect") != "/vehicles/"+v.ID.String() {
		t.Fatalf("status=%d hx-redirect=%q", response.Code, response.Header().Get("HX-Redirect"))
	}
	if response = postOdometer(t, f, uuid.New(), url.Values{"odometer_miles": {"1"}}, false); response.Code != http.StatusNotFound {
		t.Fatalf("missing vehicle status=%d", response.Code)
	}
}
