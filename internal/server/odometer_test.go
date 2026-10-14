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
	if !strings.Contains(page, "~5,000 mi left · add a mileage reading to estimate") {
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
