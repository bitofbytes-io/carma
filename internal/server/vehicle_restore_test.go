package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func TestRestoreVehicleReturnsArchivedVehicleToGarageWithHistory(t *testing.T) {
	f := setup(t)
	record, _ := createAttachment(t, f)
	v, err := f.store.GetVehicle(t.Context(), record.VehicleID)
	if err != nil {
		t.Fatal(err)
	}
	types, _ := f.store.ListServiceTypes(t.Context())
	months := 6
	if _, err = f.store.UpsertReminder(t.Context(), model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMonths: &months, Enabled: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	remindersBefore, err := f.store.ListReminders(t.Context(), &v.ID, true)
	if err != nil || len(remindersBefore) == 0 {
		t.Fatalf("reminders=%v err=%v", remindersBefore, err)
	}
	if w := f.do(t, "POST", "/vehicles/"+v.ID.String()+"/archive", nil, ""); w.Code != http.StatusSeeOther {
		t.Fatalf("archive: %d", w.Code)
	}

	restoreAction := `action="/vehicles/` + v.ID.String() + `/restore"`
	archivedPage := f.do(t, "GET", "/vehicles/archived", nil, "").Body.String()
	if !strings.Contains(archivedPage, restoreAction) || !strings.Contains(archivedPage, ">Restore</button>") {
		t.Fatalf("archived list has no restore button: %s", archivedPage)
	}
	detail := f.do(t, "GET", "/vehicles/"+v.ID.String(), nil, "").Body.String()
	if !strings.Contains(detail, restoreAction) || !strings.Contains(detail, "Restore vehicle") {
		t.Fatalf("archived vehicle page has no restore button: %s", detail)
	}

	w := f.do(t, "POST", "/vehicles/"+v.ID.String()+"/restore", nil, "")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("restore: %d location=%q", w.Code, w.Header().Get("Location"))
	}
	garage, err := f.store.ListVehicles(t.Context(), false)
	if err != nil || len(garage) != 1 || garage[0].ID != v.ID || garage[0].ArchivedAt != nil {
		t.Fatalf("garage after restore = %+v err=%v", garage, err)
	}
	if archived, _ := f.store.ListVehicles(t.Context(), true); len(archived) != 0 {
		t.Fatalf("still archived: %+v", archived)
	}
	if body := f.do(t, "GET", "/", nil, "").Body.String(); !strings.Contains(body, `href="/vehicles/`+v.ID.String()+`"`) {
		t.Fatalf("garage does not list the restored vehicle: %s", body)
	}
	detail = f.do(t, "GET", "/vehicles/"+v.ID.String(), nil, "").Body.String()
	if strings.Contains(detail, restoreAction) || strings.Contains(detail, "This vehicle is archived") {
		t.Fatalf("restored vehicle page still looks archived: %s", detail)
	}
	records, err := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &v.ID})
	if err != nil || len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("records after restore = %+v err=%v", records, err)
	}
	remindersAfter, err := f.store.ListReminders(t.Context(), &v.ID, true)
	if err != nil || len(remindersAfter) != len(remindersBefore) {
		t.Fatalf("reminders after restore = %+v err=%v, want %d", remindersAfter, err, len(remindersBefore))
	}
}

func TestRestoreVehicleHTMXRedirectsAndIsIdempotent(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	if err := f.store.ArchiveVehicle(t.Context(), v.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "http://example.com/vehicles/"+v.ID.String()+"/restore", nil)
		request.AddCookie(f.cookie)
		request.Header.Set("Origin", "http://example.com")
		request.Header.Set("HX-Request", "true")
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("HX-Redirect") != "/" || response.Header().Get("Location") != "" {
			t.Fatalf("restore: status=%d hx-redirect=%q location=%q", response.Code, response.Header().Get("HX-Redirect"), response.Header().Get("Location"))
		}
	}
	if garage, _ := f.store.ListVehicles(t.Context(), false); len(garage) != 1 {
		t.Fatalf("garage = %+v", garage)
	}
}

func TestRestoreVehicleUnknownOrMalformedIDIsNotFound(t *testing.T) {
	f := setup(t)
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		if w := f.do(t, "POST", "/vehicles/"+id+"/restore", nil, ""); w.Code != http.StatusNotFound {
			t.Fatalf("restore %s: %d", id, w.Code)
		}
	}
}

func TestVehiclePageOffersNoRestoreForActiveVehicle(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	if body := f.do(t, "GET", "/vehicles/"+v.ID.String(), nil, "").Body.String(); strings.Contains(body, "/restore") {
		t.Fatalf("active vehicle offers restore: %s", body)
	}
}
