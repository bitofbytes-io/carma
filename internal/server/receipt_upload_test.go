package server

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

type receiptWriteErrorStore struct {
	repository.Store
	persist bool
	err     error
	cancel  context.CancelFunc
	called  bool
}

func (s *receiptWriteErrorStore) CreateRecord(ctx context.Context, record model.Record, attachments []model.Attachment) (model.Record, error) {
	s.called = true
	if s.persist {
		if _, err := s.Store.CreateRecord(ctx, record, attachments); err != nil {
			return record, err
		}
	}
	if s.cancel != nil {
		s.cancel()
	}
	return record, s.err
}

func (s *receiptWriteErrorStore) AddAttachments(ctx context.Context, recordID uuid.UUID, attachments []model.Attachment) error {
	s.called = true
	if s.persist {
		if err := s.Store.AddAttachments(ctx, recordID, attachments); err != nil {
			return err
		}
	}
	if s.cancel != nil {
		s.cancel()
	}
	return s.err
}

func TestReceiptRetainedAfterPersistenceError(t *testing.T) {
	for _, operation := range []string{"create record", "add attachments"} {
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
					vehicle := createVehicle(t, f)
					types, err := f.store.ListServiceTypes(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					fields := map[string]string{}
					path := "/vehicles/" + vehicle.ID.String() + "/records"
					if operation == "create record" {
						fields = map[string]string{"occurred_on": "2026-07-30", "service_type_id": types[0].ID.String()}
					} else {
						existing := model.Record{ID: uuid.New(), VehicleID: vehicle.ID, ServiceTypeID: types[0].ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC), CreatedAt: time.Now()}
						if _, err = f.store.CreateRecord(t.Context(), existing, nil); err != nil {
							t.Fatal(err)
						}
						path = "/records/" + existing.ID.String() + "/attachments"
					}
					tracker := &trackingAssetStore{Store: localAssets}
					f.s.assets = tracker
					writeStore := &receiptWriteErrorStore{Store: f.store, persist: persist, err: errors.New("connection lost")}
					f.s.store = writeStore
					body, contentType := formBody(t, fields, "receipt.pdf", []byte("%PDF-1.7\nreceipt"))
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

					records, err := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &vehicle.ID})
					if err != nil {
						t.Fatal(err)
					}
					referenced := map[string]struct{}{}
					for _, record := range records {
						_, attachments, err := f.store.GetRecord(t.Context(), record.ID)
						if err != nil {
							t.Fatal(err)
						}
						for _, attachment := range attachments {
							referenced[attachment.StorageKey] = struct{}{}
						}
					}
					if _, ok := referenced[tracker.savedKey]; ok != persist {
						t.Fatalf("receipt referenced=%t, want %t", ok, persist)
					}

					// After the retention period, existing cleanup preserves a committed
					// receipt and removes uploads whose writes never committed.
					if _, err := localAssets.Prune(t.Context(), referenced, time.Now().Add(time.Hour)); err != nil {
						t.Fatal(err)
					}
					receipt, err := tracker.Open(t.Context(), tracker.savedKey)
					if persist {
						if err != nil {
							t.Fatalf("cleanup removed committed receipt: %v", err)
						}
						_ = receipt.Close()
					} else if !errors.Is(err, os.ErrNotExist) {
						if receipt != nil {
							_ = receipt.Close()
						}
						t.Fatalf("unreferenced upload was not pruned: %v", err)
					}
				})
			}
		}
	}
}

func TestInvalidReceiptUploadCleansSavedFilesWithoutWriting(t *testing.T) {
	f := setup(t)
	vehicle := createVehicle(t, f)
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tracker := &trackingAssetStore{Store: f.s.assets}
	f.s.assets = tracker
	writeStore := &receiptWriteErrorStore{Store: f.store, err: errors.New("must not write")}
	f.s.store = writeStore
	body, contentType := multipartFormBodyFiles(t, map[string]string{"occurred_on": "2026-07-30", "service_type_id": types[0].ID.String()},
		"receipts", []namedFile{{"good.pdf", []byte("%PDF-1.7\nreceipt")}, {"bad.txt", []byte("not a receipt")}})
	response := f.do(t, http.MethodPost, "/vehicles/"+vehicle.ID.String()+"/records", body, contentType)
	if response.Code != http.StatusBadRequest || writeStore.called {
		t.Fatalf("status=%d called=%t", response.Code, writeStore.called)
	}
	if tracker.savedKey == "" || tracker.deletedKey != tracker.savedKey {
		t.Fatalf("saved=%q deleted=%q", tracker.savedKey, tracker.deletedKey)
	}
}

type namedFile struct {
	name string
	data []byte
}

func multipartFormBodyFiles(t *testing.T, fields map[string]string, fileField string, files []namedFile) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		_ = writer.WriteField(key, value)
	}
	for _, file := range files {
		part, err := writer.CreateFormFile(fileField, file.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(file.data)
	}
	_ = writer.Close()
	return &body, writer.FormDataContentType()
}
