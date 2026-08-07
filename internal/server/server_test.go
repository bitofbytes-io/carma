package server

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
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

type deleteSessionErrorStore struct {
	repository.Store
	err error
}

func (s deleteSessionErrorStore) DeleteSession(context.Context, uuid.UUID) error { return s.err }

type recordingAssetStore struct {
	assets.Store
	events    *[]string
	deleteErr error
}

func (s recordingAssetStore) Delete(ctx context.Context, key string) error {
	*s.events = append(*s.events, "asset")
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.Store.Delete(ctx, key)
}

type trackingAssetStore struct {
	assets.Store
	savedKey, deletedKey string
}

func (s *trackingAssetStore) Save(ctx context.Context, source io.Reader, max int64) (assets.Object, error) {
	object, err := s.Store.Save(ctx, source, max)
	if err == nil {
		s.savedKey = object.Key
	}
	return object, err
}

func (s *trackingAssetStore) Delete(ctx context.Context, key string) error {
	s.deletedKey = key
	return s.Store.Delete(ctx, key)
}

type recordingAttachmentStore struct {
	repository.Store
	events    *[]string
	deleteErr error
	key       string
}

func (s recordingAttachmentStore) DeleteAttachment(ctx context.Context, id uuid.UUID) (string, error) {
	*s.events = append(*s.events, "metadata")
	if s.deleteErr != nil {
		return "", s.deleteErr
	}
	if s.key != "" {
		if _, err := s.Store.DeleteAttachment(ctx, id); err != nil {
			return "", err
		}
		return s.key, nil
	}
	return s.Store.DeleteAttachment(ctx, id)
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

func TestLogoutRetainsCookieWhenSessionRevocationFails(t *testing.T) {
	f := setup(t)
	revocationErr := errors.New("session revocation failed")
	f.s.auth = auth.NewService(deleteSessionErrorStore{Store: f.store, err: revocationErr}, f.s.cfg.SessionTTL)
	f.router = f.s.Router()

	response := f.do(t, http.MethodPost, "/logout", nil, "")

	if response.Code != http.StatusInternalServerError || response.Header().Get("Location") != "" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if cookies := response.Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("revocation failure changed browser cookie: %v", cookies)
	}
	if !strings.Contains(response.Body.String(), "Something went wrong") {
		t.Fatalf("revocation failure presented as success: %q", response.Body.String())
	}
	user, err := f.s.auth.Validate(t.Context(), f.cookie.Value)
	if err != nil || user == nil {
		t.Fatalf("session was not reusable after failed revocation: user=%v err=%v", user, err)
	}
}

func TestLogoutRevokesSessionAndClearsCookie(t *testing.T) {
	f := setup(t)

	response := f.do(t, http.MethodPost, "/logout", nil, "")

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != middleware.CookieName || cookies[0].Value != "" || cookies[0].MaxAge >= 0 {
		t.Fatalf("logout did not clear browser cookie: %+v", cookies)
	}
	user, err := f.s.auth.Validate(t.Context(), f.cookie.Value)
	if err != nil || user != nil {
		t.Fatalf("session remains valid after logout: user=%v err=%v", user, err)
	}
}

func formBody(t *testing.T, fields map[string]string, filename string, file []byte) (*bytes.Buffer, string) {
	return multipartFormBody(t, fields, "receipts", filename, file)
}

func multipartFormBody(t *testing.T, fields map[string]string, fileField, filename string, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if filename != "" {
		p, e := mw.CreateFormFile(fileField, filename)
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

func createAttachment(t *testing.T, f fixture) (model.Record, model.Attachment) {
	t.Helper()
	v := createVehicle(t, f)
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil || len(types) == 0 {
		t.Fatalf("service types=%v err=%v", types, err)
	}
	b, ct := formBody(t, map[string]string{
		"occurred_on":     "2026-07-30",
		"service_type_id": types[0].ID.String(),
		"odometer":        "62410",
		"cost":            "89.00",
		"vendor":          "Test Shop",
	}, "receipt.pdf", []byte("%PDF-1.7\nfixture"))
	response := f.do(t, http.MethodPost, "/vehicles/"+v.ID.String()+"/records", b, ct)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create record: %d %s", response.Code, response.Body.String())
	}
	records, err := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &v.ID})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	record, attachments, err := f.store.GetRecord(t.Context(), records[0].ID)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("record=%v attachments=%v err=%v", record, attachments, err)
	}
	return record, attachments[0]
}

func TestDeleteAttachmentDefersAssetCleanupAfterMetadataDelete(t *testing.T) {
	f := setup(t)
	_, attachment := createAttachment(t, f)
	events := []string{}
	f.s.assets = recordingAssetStore{Store: f.s.assets, events: &events, deleteErr: errors.New("NFS unavailable")}
	f.s.store = recordingAttachmentStore{Store: f.store, events: &events}
	f.router = f.s.Router()

	response := f.do(t, http.MethodPost, "/attachments/"+attachment.ID.String()+"/delete", strings.NewReader(""), "application/x-www-form-urlencoded")

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") == "" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if len(events) != 2 || events[0] != "metadata" || events[1] != "asset" {
		t.Fatalf("delete events=%v, want [metadata asset]", events)
	}
	if _, _, err := f.store.GetAttachment(t.Context(), attachment.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("attachment metadata remains: %v", err)
	}
	object, err := f.s.assets.Open(t.Context(), attachment.StorageKey)
	if err != nil {
		t.Fatalf("attachment asset was not retained: %v", err)
	}
	_ = object.Close()
}

func TestDeleteAttachmentRemovesMetadataBeforeAuthoritativeAssetKey(t *testing.T) {
	f := setup(t)
	record, attachment := createAttachment(t, f)
	events := []string{}
	f.s.assets = recordingAssetStore{Store: f.s.assets, events: &events}
	f.s.store = recordingAttachmentStore{Store: f.store, events: &events, key: attachment.StorageKey}
	f.router = f.s.Router()

	response := f.do(t, http.MethodPost, "/attachments/"+attachment.ID.String()+"/delete", strings.NewReader(""), "application/x-www-form-urlencoded")

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/records/"+record.ID.String() {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if len(events) != 2 || events[0] != "metadata" || events[1] != "asset" {
		t.Fatalf("delete events=%v, want [metadata asset]", events)
	}
	if _, _, err := f.store.GetAttachment(t.Context(), attachment.ID); err != repository.ErrNotFound {
		t.Fatalf("attachment metadata remains: %v", err)
	}
	if object, err := f.s.assets.Open(t.Context(), attachment.StorageKey); err == nil {
		_ = object.Close()
		t.Fatalf("attachment asset remains at %q", attachment.StorageKey)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking removed attachment asset: %v", err)
	}
	if err := f.s.assets.Delete(t.Context(), attachment.StorageKey); err != nil {
		t.Fatalf("retrying asset deletion: %v", err)
	}
}

func TestDeleteAttachmentDatabaseFailureLeavesAssetIntact(t *testing.T) {
	f := setup(t)
	_, attachment := createAttachment(t, f)
	events := []string{}
	f.s.assets = recordingAssetStore{Store: f.s.assets, events: &events}
	f.s.store = recordingAttachmentStore{Store: f.store, events: &events, deleteErr: errors.New("database unavailable")}
	f.router = f.s.Router()

	response := f.do(t, http.MethodPost, "/attachments/"+attachment.ID.String()+"/delete", strings.NewReader(""), "application/x-www-form-urlencoded")

	if response.Code != http.StatusInternalServerError || response.Header().Get("Location") != "" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if len(events) != 1 || events[0] != "metadata" {
		t.Fatalf("delete events=%v, want metadata only", events)
	}
	if _, stored, err := f.store.GetAttachment(t.Context(), attachment.ID); err != nil || stored.StorageKey != attachment.StorageKey {
		t.Fatalf("metadata changed: attachment=%+v err=%v", stored, err)
	}
	object, err := f.s.assets.Open(t.Context(), attachment.StorageKey)
	if err != nil {
		t.Fatalf("asset missing: %v", err)
	}
	_ = object.Close()
}

func TestDeleteAttachmentMissingObjectCleanupIsIdempotent(t *testing.T) {
	f := setup(t)
	record, attachment := createAttachment(t, f)
	if err := f.s.assets.Delete(t.Context(), attachment.StorageKey); err != nil {
		t.Fatal(err)
	}

	response := f.do(t, http.MethodPost, "/attachments/"+attachment.ID.String()+"/delete", strings.NewReader(""), "application/x-www-form-urlencoded")

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/records/"+record.ID.String() {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if _, _, err := f.store.GetAttachment(t.Context(), attachment.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("metadata remains: %v", err)
	}
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
	unauth := httptest.NewRecorder()
	f.router.ServeHTTP(unauth, httptest.NewRequest("GET", "http://example.com/attachments/"+as[0].ID.String(), nil))
	if unauth.Code != http.StatusSeeOther || !strings.HasPrefix(unauth.Header().Get("Location"), "/login?redirect=") {
		t.Fatalf("attachment auth: %d %s", unauth.Code, unauth.Header().Get("Location"))
	}
	notFound := f.do(t, "GET", "/attachments/"+uuid.NewString(), nil, "")
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("attachment not found: %d", notFound.Code)
	}
	deleted := f.do(t, "POST", "/attachments/"+as[0].ID.String()+"/delete", strings.NewReader(""), "application/x-www-form-urlencoded")
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/records/"+rows[0].ID.String() {
		t.Fatalf("attachment delete redirect: %d %s", deleted.Code, deleted.Header().Get("Location"))
	}
	if _, _, err := f.store.GetAttachment(t.Context(), as[0].ID); err != repository.ErrNotFound {
		t.Fatalf("attachment metadata remains: %v", err)
	}
	if object, err := f.s.assets.Open(t.Context(), as[0].StorageKey); err == nil {
		_ = object.Close()
		t.Fatalf("attachment asset remains at %q", as[0].StorageKey)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking removed attachment asset: %v", err)
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
			if !tc.wantSession && w.Header().Get("Location") != "/login?error="+loginErrorNotInvited {
				t.Fatalf("unfriendly rejection: %s", w.Header().Get("Location"))
			}
		})
	}
}

func TestGoogleOAuthPreservesOnlySafeDeepLinks(t *testing.T) {
	store := repository.NewMemory()
	a, err := assets.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		AppEnv:             "production",
		AuthMode:           config.AuthGoogle,
		GoogleClientSecret: "state-signing-secret",
		SessionTTL:         90 * 24 * time.Hour,
		MaxUploadBytes:     25 << 20,
	}
	svc := auth.NewService(store, cfg.SessionTTL)
	g := fakeGoogle{
		claims:  auth.Claims{Subject: "sub", Email: "person@example.com", Name: "Person", EmailVerified: true},
		allowed: true,
	}
	srv, err := New(cfg, store, a, svc, g)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.Router()

	completeLogin := func(t *testing.T, requestedTarget string) *httptest.ResponseRecorder {
		t.Helper()
		start := httptest.NewRecorder()
		startRequest := httptest.NewRequest("GET", "https://carma.example/api/auth/google?redirect="+url.QueryEscape(requestedTarget), nil)
		router.ServeHTTP(start, startRequest)

		var state *http.Cookie
		for _, cookie := range start.Result().Cookies() {
			if cookie.Name == oauthStateCookie {
				state = cookie
				break
			}
		}
		if state == nil {
			t.Fatal("missing OAuth state cookie")
		}

		callback := httptest.NewRequest("GET", "https://carma.example/api/auth/google/callback?state="+url.QueryEscape(state.Value)+"&code=ok", nil)
		callback.AddCookie(state)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, callback)
		return response
	}

	deepLink := "/vehicles/123?sort=cost&direction=asc"
	login := httptest.NewRecorder()
	router.ServeHTTP(login, httptest.NewRequest("GET", "https://carma.example/login?redirect="+url.QueryEscape(deepLink), nil))
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), "/api/auth/google?redirect=%2Fvehicles%2F123%3Fsort%3Dcost%26direction%3Dasc") {
		t.Fatalf("login did not preserve deep link: %d %s", login.Code, login.Body.String())
	}

	if response := completeLogin(t, deepLink); response.Code != http.StatusSeeOther || response.Header().Get("Location") != deepLink {
		t.Fatalf("safe redirect: %d %q", response.Code, response.Header().Get("Location"))
	}

	for _, unsafe := range []string{
		"https://evil.example/steal",
		"//evil.example/steal",
		`/%2f%2fevil.example/steal`,
		`/\evil.example/steal`,
		`/%5c%5cevil.example/steal`,
		`/%zz`,
	} {
		t.Run("reject "+unsafe, func(t *testing.T) {
			response := completeLogin(t, unsafe)
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
				t.Fatalf("unsafe redirect %q: %d %q", unsafe, response.Code, response.Header().Get("Location"))
			}
		})
	}
}

func TestGoogleOAuthRejectsTamperedState(t *testing.T) {
	store := repository.NewMemory()
	a, err := assets.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AppEnv: "production", AuthMode: config.AuthGoogle, GoogleClientSecret: "state-signing-secret", SessionTTL: 90 * 24 * time.Hour, MaxUploadBytes: 25 << 20}
	svc := auth.NewService(store, cfg.SessionTTL)
	g := fakeGoogle{claims: auth.Claims{Subject: "sub", Email: "person@example.com", Name: "Person", EmailVerified: true}, allowed: true}
	srv, err := New(cfg, store, a, svc, g)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.Router()

	state, err := signedOAuthState("nonce", "/vehicles/123", cfg.GoogleClientSecret)
	if err != nil {
		t.Fatal(err)
	}
	tampered := "A" + state[1:]
	if tampered == state {
		tampered = "B" + state[1:]
	}
	callback := httptest.NewRequest("GET", "https://carma.example/api/auth/google/callback?state="+url.QueryEscape(tampered)+"&code=ok", nil)
	callback.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: tampered})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, callback)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login?error="+loginErrorExpired {
		t.Fatalf("tampered state accepted: %d %q", response.Code, response.Header().Get("Location"))
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == middleware.CookieName && cookie.Value != "" {
			t.Fatal("tampered state created a session")
		}
	}
}

func TestLoginPageAllowsOnlyKnownErrorCodes(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		code, message string
	}{
		{loginErrorExpired, "Sign-in expired. Please try again."},
		{loginErrorOAuth, "Google sign-in could not be completed. Please try again."},
		{loginErrorNotInvited, "This verified Google account is not invited to Carma."},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://example.com/login?error="+url.QueryEscape(tc.code), nil)
		f.router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), tc.message) {
			t.Fatalf("known code %q: status=%d body=%s", tc.code, response.Code, response.Body.String())
		}
	}

	attackerText := `<script>ATTACKER-CONTROLLED</script>`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.com/login?error="+url.QueryEscape(attackerText), nil)
	f.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "ATTACKER-CONTROLLED") {
		t.Fatalf("unknown error rendered attacker text: status=%d body=%s", response.Code, response.Body.String())
	}
}
