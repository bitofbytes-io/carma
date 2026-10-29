package server

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func TestVehicleCurrentOdometerCreateEditAndValidation(t *testing.T) {
	f := setup(t)
	b, ct := formBody(t, map[string]string{"nickname": "Ranger", "year": "2026", "make": "Ford", "model": "Ranger", "current_odometer": "842"}, "", nil)
	response := f.do(t, http.MethodPost, "/vehicles", b, ct)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	vehicles, _ := f.store.ListVehicles(t.Context(), false)
	if len(vehicles) != 1 || vehicles[0].CurrentOdometer == nil || *vehicles[0].CurrentOdometer != 842 || vehicles[0].LatestOdometer == nil || *vehicles[0].LatestOdometer != 842 {
		t.Fatalf("vehicle mileage not persisted: %+v", vehicles)
	}
	v := vehicles[0]

	for _, invalid := range []string{"-1", "1.5", "999999999999999999999"} {
		b, ct = formBody(t, map[string]string{"nickname": "Ranger", "current_odometer": invalid}, "", nil)
		response = f.do(t, http.MethodPost, "/vehicles/"+v.ID.String(), b, ct)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Current odometer must be a nonnegative whole number.") {
			t.Fatalf("odometer %q status=%d body=%s", invalid, response.Code, response.Body.String())
		}
	}

	b, ct = formBody(t, map[string]string{"nickname": "Ranger", "current_odometer": "910"}, "", nil)
	response = f.do(t, http.MethodPost, "/vehicles/"+v.ID.String(), b, ct)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("edit status=%d body=%s", response.Code, response.Body.String())
	}
	updated, _ := f.store.GetVehicle(t.Context(), v.ID)
	if updated.CurrentOdometer == nil || *updated.CurrentOdometer != 910 || updated.LatestOdometer == nil || *updated.LatestOdometer != 910 {
		t.Fatalf("updated vehicle=%+v", updated)
	}
}

func TestVehicleEditShowsEffectiveRecordMileage(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	odometer := int64(45123)
	_, _ = f.store.CreateRecord(t.Context(), model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC), OdometerMiles: &odometer, CreatedAt: time.Now()}, nil)

	response := f.do(t, http.MethodGet, "/vehicles/"+v.ID.String()+"/edit", nil, "")
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `name="current_odometer"`) || !strings.Contains(body, `value="45123"`) {
		t.Fatalf("edit form did not show effective mileage: %d %s", response.Code, body)
	}
}

func TestVehiclePhotoIsPreservedWithoutUploadAndReplacedWithUpload(t *testing.T) {
	f := setup(t)
	originalPhoto := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
	b, ct := multipartFormBody(t, map[string]string{"nickname": "Outback"}, "photo", "original.jpg", originalPhoto)
	response := f.do(t, http.MethodPost, "/vehicles", b, ct)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	vehicles, err := f.store.ListVehicles(t.Context(), false)
	if err != nil || len(vehicles) != 1 || vehicles[0].PhotoKey == "" {
		t.Fatalf("created vehicle photo not persisted: vehicles=%+v err=%v", vehicles, err)
	}
	vehicle := vehicles[0]
	originalKey := vehicle.PhotoKey

	b, ct = multipartFormBody(t, map[string]string{"nickname": "Updated Outback", "notes": "No replacement"}, "photo", "", nil)
	response = f.do(t, http.MethodPost, "/vehicles/"+vehicle.ID.String(), b, ct)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("no-upload update status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err := f.store.GetVehicle(t.Context(), vehicle.ID)
	if err != nil || updated.PhotoKey != originalKey || updated.Nickname != "Updated Outback" {
		t.Fatalf("no-upload update did not preserve photo: vehicle=%+v err=%v", updated, err)
	}
	object, err := f.s.assets.Open(t.Context(), originalKey)
	if err != nil {
		t.Fatalf("preserved photo cannot be opened: %v", err)
	}
	preservedPhoto, readErr := io.ReadAll(object)
	closeErr := object.Close()
	if readErr != nil {
		t.Fatalf("preserved photo cannot be read: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("closing preserved photo: %v", closeErr)
	}
	if !bytes.Equal(preservedPhoto, originalPhoto) {
		t.Fatalf("preserved photo content=%q, want %q", preservedPhoto, originalPhoto)
	}

	replacementPhoto := append([]byte("\x89PNG\r\n\x1a\n"), []byte("replacement")...)
	b, ct = multipartFormBody(t, map[string]string{"nickname": "Updated Outback", "notes": "Replacement"}, "photo", "replacement.png", replacementPhoto)
	response = f.do(t, http.MethodPost, "/vehicles/"+vehicle.ID.String(), b, ct)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("replacement update status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err = f.store.GetVehicle(t.Context(), vehicle.ID)
	if err != nil || updated.PhotoKey == "" || updated.PhotoKey == originalKey {
		t.Fatalf("replacement photo not persisted: vehicle=%+v err=%v", updated, err)
	}
	if object, err = f.s.assets.Open(t.Context(), originalKey); err == nil {
		_ = object.Close()
		t.Fatalf("original photo remains after replacement at %q", originalKey)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking removed original photo: %v", err)
	}
	object, err = f.s.assets.Open(t.Context(), updated.PhotoKey)
	if err != nil {
		t.Fatalf("replacement photo cannot be opened: %v", err)
	}
	_ = object.Close()
	photoResponse := f.do(t, http.MethodGet, "/vehicles/"+vehicle.ID.String()+"/photo", nil, "")
	if photoResponse.Code != http.StatusOK || photoResponse.Header().Get("Content-Type") != "image/png" || !bytes.Equal(photoResponse.Body.Bytes(), replacementPhoto) {
		t.Fatalf("replacement photo response status=%d content-type=%q body=%q", photoResponse.Code, photoResponse.Header().Get("Content-Type"), photoResponse.Body.Bytes())
	}
}

func TestVehiclePhotoValidationRerenderUsesPersistedPhoto(t *testing.T) {
	upload := append([]byte("\x89PNG\r\n\x1a\n"), []byte("transient")...)

	t.Run("vehicle without existing photo", func(t *testing.T) {
		f := setup(t)
		vehicle := createVehicle(t, f)
		tracker := &trackingAssetStore{Store: f.s.assets}
		f.s.assets = tracker

		b, ct := multipartFormBody(t, map[string]string{"nickname": "Outback", "year": "invalid"}, "photo", "transient.png", upload)
		response := f.do(t, http.MethodPost, "/vehicles/"+vehicle.ID.String(), b, ct)
		body := response.Body.String()
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, body)
		}
		for _, stale := range []string{`class="existing-photo"`, "This photo is already saved.", `/vehicles/` + vehicle.ID.String() + `/photo`} {
			if strings.Contains(body, stale) {
				t.Fatalf("validation rerender contains stale photo marker %q: %s", stale, body)
			}
		}
		if !strings.Contains(body, "Optional. Add a photo of your vehicle.") {
			t.Fatalf("validation rerender is missing no-photo guidance: %s", body)
		}
		persisted, err := f.store.GetVehicle(t.Context(), vehicle.ID)
		if err != nil || persisted.PhotoKey != "" {
			t.Fatalf("validation changed persisted photo: vehicle=%+v err=%v", persisted, err)
		}
		if tracker.savedKey == "" || tracker.deletedKey != tracker.savedKey {
			t.Fatalf("transient upload cleanup saved=%q deleted=%q", tracker.savedKey, tracker.deletedKey)
		}
		if object, err := tracker.Open(t.Context(), tracker.savedKey); err == nil {
			_ = object.Close()
			t.Fatalf("transient upload remains at %q", tracker.savedKey)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("checking transient upload cleanup: %v", err)
		}
	})

	t.Run("vehicle with existing photo", func(t *testing.T) {
		f := setup(t)
		originalPhoto := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
		b, ct := multipartFormBody(t, map[string]string{"nickname": "Outback"}, "photo", "original.jpg", originalPhoto)
		response := f.do(t, http.MethodPost, "/vehicles", b, ct)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
		}
		vehicles, err := f.store.ListVehicles(t.Context(), false)
		if err != nil || len(vehicles) != 1 || vehicles[0].PhotoKey == "" {
			t.Fatalf("created vehicle photo not persisted: vehicles=%+v err=%v", vehicles, err)
		}
		vehicle := vehicles[0]
		tracker := &trackingAssetStore{Store: f.s.assets}
		f.s.assets = tracker

		b, ct = multipartFormBody(t, map[string]string{"nickname": "Outback", "year": "invalid"}, "photo", "transient.png", upload)
		response = f.do(t, http.MethodPost, "/vehicles/"+vehicle.ID.String(), b, ct)
		body := response.Body.String()
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, body)
		}
		for _, current := range []string{`class="existing-photo"`, "This photo is already saved.", `/vehicles/` + vehicle.ID.String() + `/photo`} {
			if !strings.Contains(body, current) {
				t.Fatalf("validation rerender is missing persisted photo marker %q: %s", current, body)
			}
		}
		persisted, err := f.store.GetVehicle(t.Context(), vehicle.ID)
		if err != nil || persisted.PhotoKey != vehicle.PhotoKey {
			t.Fatalf("validation changed persisted photo: vehicle=%+v err=%v", persisted, err)
		}
		if tracker.savedKey == "" || tracker.savedKey == vehicle.PhotoKey || tracker.deletedKey != tracker.savedKey {
			t.Fatalf("transient upload cleanup saved=%q deleted=%q original=%q", tracker.savedKey, tracker.deletedKey, vehicle.PhotoKey)
		}
		object, err := tracker.Open(t.Context(), vehicle.PhotoKey)
		if err != nil {
			t.Fatalf("persisted photo cannot be opened: %v", err)
		}
		_ = object.Close()
	})
}

func TestArchivedVehiclesPageListsOnlyArchivedVehicles(t *testing.T) {
	f := setup(t)
	empty := f.do(t, "GET", "/vehicles/archived", nil, "").Body.String()
	if !strings.Contains(empty, "No archived vehicles.") {
		t.Fatalf("empty archived page: %s", empty)
	}
	v := createVehicle(t, f)
	if err := f.store.ArchiveVehicle(t.Context(), v.ID); err != nil {
		t.Fatal(err)
	}
	w := f.do(t, "GET", "/vehicles/archived", nil, "")
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `<h1>Archived vehicles</h1>`) || !strings.Contains(body, `href="/vehicles/`+v.ID.String()+`"`) || strings.Contains(body, "No archived vehicles.") {
		t.Fatalf("archived page: %d %s", w.Code, body)
	}
	if strings.Contains(body, `class="filters"`) || strings.Contains(body, "Export CSV") {
		t.Fatalf("archived page rendered record controls: %s", body)
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
