package server

import (
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func warrantyQuery(vehicleID uuid.UUID, search string) model.RecordQuery {
	return model.RecordQuery{VehicleID: &vehicleID, Search: search}
}

// warrantyRecord posts a new record for the vehicle and returns the response.
func warrantyRecord(t *testing.T, f fixture, vehicleID, typeID string, fields map[string]string) *http.Response {
	t.Helper()
	body := map[string]string{"occurred_on": "2026-07-01", "service_type_id": typeID}
	for key, value := range fields {
		body[key] = value
	}
	b, ct := formBody(t, body, "", nil)
	return f.do(t, http.MethodPost, "/vehicles/"+vehicleID+"/records", b, ct).Result()
}

func TestRecordWarrantySavesDateMilesBothOrNeither(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	for _, tc := range []struct {
		name, date, miles string
		wantDate          string
		wantMiles         int64
	}{
		{name: "date only", date: "2028-07-01", wantDate: "2028-07-01", wantMiles: -1},
		{name: "miles only", miles: "36,000", wantMiles: 36000},
		{name: "both", date: "2026-07-01", miles: "12000", wantDate: "2026-07-01", wantMiles: 12000},
		{name: "neither", wantMiles: -1},
		{name: "zero miles", miles: "0", wantMiles: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := warrantyRecord(t, f, v.ID.String(), types[0].ID.String(), map[string]string{
				"vendor": tc.name, "warranty_until": tc.date, "warranty_miles": tc.miles,
			})
			if response.StatusCode != http.StatusSeeOther {
				t.Fatalf("status=%d", response.StatusCode)
			}
			records, err := f.store.ListRecords(t.Context(), warrantyQuery(v.ID, tc.name))
			if err != nil || len(records) != 1 {
				t.Fatalf("records=%v err=%v", records, err)
			}
			record := records[0]
			if tc.wantDate == "" && record.WarrantyUntilOn != nil {
				t.Fatalf("unexpected warranty date %v", record.WarrantyUntilOn)
			}
			if tc.wantDate != "" && (record.WarrantyUntilOn == nil || record.WarrantyUntilOn.Format("2006-01-02") != tc.wantDate) {
				t.Fatalf("warranty date=%v want %s", record.WarrantyUntilOn, tc.wantDate)
			}
			if tc.wantMiles < 0 && record.WarrantyUntilMiles != nil {
				t.Fatalf("unexpected warranty miles %d", *record.WarrantyUntilMiles)
			}
			if tc.wantMiles >= 0 && (record.WarrantyUntilMiles == nil || *record.WarrantyUntilMiles != tc.wantMiles) {
				t.Fatalf("warranty miles=%v want %d", record.WarrantyUntilMiles, tc.wantMiles)
			}
		})
	}
}

func TestRecordWarrantyRejectsInvalidValues(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	for _, tc := range []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{name: "before service date", fields: map[string]string{"warranty_until": "2026-06-30"}, want: "Warranty end date cannot be before the service date."},
		{name: "not a date", fields: map[string]string{"warranty_until": "next year"}, want: "Warranty end date must be a valid date."},
		{name: "negative miles", fields: map[string]string{"warranty_miles": "-5"}, want: "Warranty mileage must be a non-negative whole number."},
		{name: "fractional miles", fields: map[string]string{"warranty_miles": "12.5"}, want: "Warranty mileage must be a non-negative whole number."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := warrantyRecord(t, f, v.ID.String(), types[0].ID.String(), tc.fields)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d want 400", response.StatusCode)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), tc.want) {
				t.Fatalf("missing %q in %s", tc.want, body)
			}
		})
	}
	records, err := f.store.ListRecords(t.Context(), warrantyQuery(v.ID, ""))
	if err != nil || len(records) != 0 {
		t.Fatalf("invalid warranty saved records=%v err=%v", records, err)
	}
}

func TestRecordWarrantyEditCanSetAndClear(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	if response := warrantyRecord(t, f, v.ID.String(), types[0].ID.String(), nil); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	records, _ := f.store.ListRecords(t.Context(), warrantyQuery(v.ID, ""))
	id := records[0].ID.String()

	edit := func(date, miles string) int {
		form := strings.NewReader("occurred_on=2026-07-01&service_type_id=" + types[0].ID.String() + "&warranty_until=" + date + "&warranty_miles=" + miles)
		return f.do(t, http.MethodPost, "/records/"+id, form, "application/x-www-form-urlencoded").Code
	}
	if code := edit("2027-01-01", "20000"); code != http.StatusSeeOther {
		t.Fatalf("set status=%d", code)
	}
	form := f.do(t, http.MethodGet, "/records/"+id+"/edit", nil, "").Body.String()
	for _, want := range []string{`name="warranty_until" value="2027-01-01"`, `name="warranty_miles" value="20000"`} {
		if !strings.Contains(form, want) {
			t.Fatalf("edit form missing %q: %s", want, form)
		}
	}
	if code := edit("", ""); code != http.StatusSeeOther {
		t.Fatalf("clear status=%d", code)
	}
	cleared, _, err := f.store.GetRecord(t.Context(), records[0].ID)
	if err != nil || cleared.HasWarranty() {
		t.Fatalf("warranty not cleared: %+v err=%v", cleared, err)
	}
}

func TestWarrantyBadgeFollowsDateAndOdometer(t *testing.T) {
	f := setup(t)
	v := createVehicleWithOdometer(t, f, "Wagon", "30000")
	types, _ := f.store.ListServiceTypes(t.Context())
	response := warrantyRecord(t, f, v.ID.String(), types[0].ID.String(), map[string]string{
		"warranty_until": "2026-12-31", "warranty_miles": "36000",
	})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	records, _ := f.store.ListRecords(t.Context(), warrantyQuery(v.ID, ""))
	recordPage := "/records/" + records[0].ID.String()
	vehiclePage := "/vehicles/" + v.ID.String()
	const badge = "Under warranty until Dec 31, 2026 or 36,000 mi"

	// now is 2026-07-31 and the odometer is 30,000: both limits are open.
	for _, path := range []string{vehiclePage, recordPage} {
		if body := f.do(t, http.MethodGet, path, nil, "").Body.String(); !strings.Contains(body, badge) {
			t.Fatalf("%s missing active badge: %s", path, body)
		}
	}

	// The odometer passes the mileage limit.
	f.s.now = func() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }
	if w := f.do(t, http.MethodPost, vehiclePage+"/odometer", strings.NewReader("odometer_miles=36001&return=vehicle"), "application/x-www-form-urlencoded"); w.Code >= 400 {
		t.Fatalf("odometer update status=%d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{vehiclePage, recordPage} {
		if body := f.do(t, http.MethodGet, path, nil, "").Body.String(); strings.Contains(body, "Under warranty") {
			t.Fatalf("%s still shows badge after the odometer passed the limit: %s", path, body)
		}
	}
	if body := f.do(t, http.MethodGet, recordPage, nil, "").Body.String(); !strings.Contains(body, "Expired") {
		t.Fatalf("record page does not say the warranty expired: %s", body)
	}
}

func TestWarrantyBadgeEndsAfterDateInHouseholdTimezone(t *testing.T) {
	f := setup(t)
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	f.s.cfg.Location = loc
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	if response := warrantyRecord(t, f, v.ID.String(), types[0].ID.String(), map[string]string{"warranty_until": "2026-07-31"}); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	vehiclePage := "/vehicles/" + v.ID.String()

	// 2026-08-01 02:00 UTC is still July 31 in Los Angeles: the last covered day.
	f.s.now = func() time.Time { return time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC) }
	if body := f.do(t, http.MethodGet, vehiclePage, nil, "").Body.String(); !strings.Contains(body, "Under warranty until Jul 31, 2026") {
		t.Fatalf("badge missing on the last day: %s", body)
	}
	// 2026-08-01 08:00 UTC is August 1 in Los Angeles.
	f.s.now = func() time.Time { return time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC) }
	if body := f.do(t, http.MethodGet, vehiclePage, nil, "").Body.String(); strings.Contains(body, "Under warranty") {
		t.Fatalf("badge still shown after the date passed: %s", body)
	}
}

func TestWarrantyAppearsInExportsAndPrintableHistory(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	if response := warrantyRecord(t, f, v.ID.String(), types[0].ID.String(), map[string]string{
		"vendor": "Dealer", "warranty_until": "2028-03-03", "warranty_miles": "36000",
	}); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status=%d", response.StatusCode)
	}

	csvBody := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/export.csv", nil, "")
	rows, err := csv.NewReader(strings.NewReader(csvBody.Body.String())).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("csv rows=%v err=%v", rows, err)
	}
	if got := strings.Join(rows[0][9:], "|"); got != "warranty until date|warranty until miles" {
		t.Fatalf("csv warranty headers=%q", got)
	}
	if got := strings.Join(rows[1][9:], "|"); got != "2028-03-03|36000" {
		t.Fatalf("csv warranty values=%q", got)
	}

	if xlsx := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/export.xlsx", nil, ""); xlsx.Code != http.StatusOK {
		t.Fatalf("xlsx status=%d", xlsx.Code)
	}
	for _, path := range []string{"/history/print", "/history"} {
		body := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+path, nil, "").Body.String()
		for _, want := range []string{">Warranty</th>", "Until Mar 3, 2028 or 36,000 mi", ">Active<"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %q: %s", path, want, body)
			}
		}
	}
}
