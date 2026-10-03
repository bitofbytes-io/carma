package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
)

type vehicleWriteErrorStore struct {
	repository.Store
	persist bool
	err     error
	cancel  context.CancelFunc
	called  bool
}

func (s *vehicleWriteErrorStore) CreateVehicle(ctx context.Context, vehicle model.Vehicle) (model.Vehicle, error) {
	s.called = true
	if s.persist {
		if _, err := s.Store.CreateVehicle(ctx, vehicle); err != nil {
			return vehicle, err
		}
	}
	if s.cancel != nil {
		s.cancel()
	}
	return vehicle, s.err
}

func (s *vehicleWriteErrorStore) UpdateVehicle(ctx context.Context, vehicle model.Vehicle) (model.Vehicle, string, error) {
	s.called = true
	if s.persist {
		if _, _, err := s.Store.UpdateVehicle(ctx, vehicle); err != nil {
			return vehicle, "", err
		}
	}
	if s.cancel != nil {
		s.cancel()
	}
	return vehicle, "", s.err
}

func TestVehiclePhotoRetainedAfterPersistenceError(t *testing.T) {
	for _, editing := range []bool{false, true} {
		operation := "create"
		if editing {
			operation = "update"
		}
		for _, persist := range []bool{false, true} {
			outcome := "failed"
			if persist {
				outcome = "committed"
			}
			for _, canceled := range []bool{false, true} {
				failure := "connection error"
				if canceled {
					failure = "canceled request"
				}
				t.Run(operation+"/"+outcome+"/"+failure, func(t *testing.T) {
					f := setup(t)
					localAssets := f.s.assets.(*assets.LocalStore)
					path := "/vehicles"
					var old model.Vehicle
					if editing {
						old = createVehicle(t, f)
						photo, err := localAssets.Save(t.Context(), strings.NewReader("\x89PNG\r\n\x1a\nold"), 1024)
						if err != nil {
							t.Fatal(err)
						}
						old.PhotoKey = photo.Key
						if _, _, err = f.store.UpdateVehicle(t.Context(), old); err != nil {
							t.Fatal(err)
						}
						path += "/" + old.ID.String()
					}
					tracker := &trackingAssetStore{Store: localAssets}
					f.s.assets = tracker
					writeStore := &vehicleWriteErrorStore{Store: f.store, persist: persist, err: errors.New("connection lost")}
					f.s.store = writeStore
					body, contentType := multipartFormBody(t, map[string]string{"nickname": "Updated"}, "photo", "new.png", []byte("\x89PNG\r\n\x1a\nnew"))
					request := httptest.NewRequest(http.MethodPost, "http://example.com"+path, body)
					request.AddCookie(f.cookie)
					request.Header.Set("Origin", "http://example.com")
					request.Header.Set("Content-Type", contentType)
					if canceled {
						ctx, cancel := context.WithCancel(request.Context())
						defer cancel()
						request = request.WithContext(ctx)
						writeStore.cancel = cancel
						writeStore.err = context.Canceled
					}
					response := httptest.NewRecorder()
					f.router.ServeHTTP(response, request)
					if response.Code != http.StatusInternalServerError || !writeStore.called {
						t.Fatalf("status=%d called=%t body=%s", response.Code, writeStore.called, response.Body.String())
					}
					if tracker.savedKey == "" || tracker.deletedKey != "" {
						t.Fatalf("saved=%q deleted=%q", tracker.savedKey, tracker.deletedKey)
					}
					assertPhotoExists(t, tracker, tracker.savedKey)
					if editing {
						assertPhotoExists(t, tracker, old.PhotoKey)
					}
					vehicles, err := f.store.ListVehicles(t.Context(), false)
					if err != nil {
						t.Fatal(err)
					}
					if persist && (len(vehicles) != 1 || vehicles[0].PhotoKey != tracker.savedKey) {
						t.Fatalf("committed photo reference was lost: %+v", vehicles)
					}
					if !persist && editing && vehicles[0].PhotoKey != old.PhotoKey {
						t.Fatalf("failed update changed photo: %+v", vehicles[0])
					}

					// After the retention period, existing cleanup preserves a committed
					// photo and removes uploads whose writes never committed.
					referenced := make(map[string]struct{}, len(vehicles))
					for _, vehicle := range vehicles {
						referenced[vehicle.PhotoKey] = struct{}{}
					}
					if _, err := localAssets.Prune(t.Context(), referenced, time.Now().Add(time.Hour)); err != nil {
						t.Fatal(err)
					}
					photo, err := tracker.Open(t.Context(), tracker.savedKey)
					if persist {
						if err != nil {
							t.Fatalf("cleanup removed committed photo: %v", err)
						}
						_ = photo.Close()
					} else if !errors.Is(err, os.ErrNotExist) {
						if photo != nil {
							_ = photo.Close()
						}
						t.Fatalf("unreferenced upload was not pruned: %v", err)
					}
				})
			}
		}
	}
}

func assertPhotoExists(t *testing.T, store assets.Store, key string) {
	t.Helper()
	photo, err := store.Open(t.Context(), key)
	if err != nil {
		t.Fatalf("photo %q missing: %v", key, err)
	}
	defer photo.Close()
	if _, err := io.ReadAll(photo); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidNewVehicleCleansUploadWithoutWriting(t *testing.T) {
	f := setup(t)
	tracker := &trackingAssetStore{Store: f.s.assets}
	f.s.assets = tracker
	writeStore := &vehicleWriteErrorStore{Store: f.store, err: errors.New("must not write")}
	f.s.store = writeStore
	body, contentType := multipartFormBody(t, map[string]string{"nickname": ""}, "photo", "new.png", []byte("\x89PNG\r\n\x1a\nnew"))
	response := f.do(t, http.MethodPost, "/vehicles", body, contentType)
	if response.Code != http.StatusBadRequest || writeStore.called {
		t.Fatalf("status=%d called=%t", response.Code, writeStore.called)
	}
	if tracker.savedKey == "" || tracker.deletedKey != tracker.savedKey {
		t.Fatalf("saved=%q deleted=%q", tracker.savedKey, tracker.deletedKey)
	}
	if photo, err := tracker.Open(t.Context(), tracker.savedKey); !errors.Is(err, os.ErrNotExist) {
		if photo != nil {
			_ = photo.Close()
		}
		t.Fatalf("invalid upload was not deleted: %v", err)
	}
}

// concurrentPhotoStore replaces the vehicle's photo after the handler has
// loaded the vehicle and before its own update runs.
type concurrentPhotoStore struct {
	repository.Store
	concurrentKey string
}

func (s *concurrentPhotoStore) UpdateVehicle(ctx context.Context, vehicle model.Vehicle) (model.Vehicle, string, error) {
	concurrent := vehicle
	concurrent.PhotoKey = s.concurrentKey
	if _, _, err := s.Store.UpdateVehicle(ctx, concurrent); err != nil {
		return vehicle, "", err
	}
	return s.Store.UpdateVehicle(ctx, vehicle)
}

func TestVehicleEditWithoutUploadKeepsConcurrentlyReplacedPhoto(t *testing.T) {
	f := setup(t)
	vehicle := createVehicle(t, f)
	original, err := f.s.assets.Save(t.Context(), strings.NewReader("\x89PNG\r\n\x1a\noriginal"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	vehicle.PhotoKey = original.Key
	if _, _, err = f.store.UpdateVehicle(t.Context(), vehicle); err != nil {
		t.Fatal(err)
	}
	concurrent, err := f.s.assets.Save(t.Context(), strings.NewReader("\x89PNG\r\n\x1a\nconcurrent"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	tracker := &trackingAssetStore{Store: f.s.assets}
	f.s.assets = tracker
	f.s.store = &concurrentPhotoStore{Store: f.store, concurrentKey: concurrent.Key}

	body, contentType := multipartFormBody(t, map[string]string{"nickname": "Renamed"}, "photo", "", nil)
	response := f.do(t, http.MethodPost, "/vehicles/"+vehicle.ID.String(), body, contentType)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	persisted, err := f.store.GetVehicle(t.Context(), vehicle.ID)
	if err != nil || persisted.PhotoKey != concurrent.Key || persisted.Nickname != "Renamed" {
		t.Fatalf("stale edit changed photo: vehicle=%+v err=%v", persisted, err)
	}
	if tracker.deletedKey != "" {
		t.Fatalf("edit without upload deleted %q", tracker.deletedKey)
	}
	assertPhotoExists(t, tracker, concurrent.Key)
}
