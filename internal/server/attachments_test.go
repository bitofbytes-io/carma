package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

type recordingAssetStore struct {
	assets.Store
	events     *[]string
	deletedKey *string
	deleteErr  error
}

func (s recordingAssetStore) Delete(ctx context.Context, key string) error {
	*s.events = append(*s.events, "asset")
	if s.deletedKey != nil {
		*s.deletedKey = key
	}
	if s.deleteErr != nil {
		return s.deleteErr
	}
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
	authoritativeObject, err := f.s.assets.Save(t.Context(), strings.NewReader("%PDF-1.7\nauthoritative"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if authoritativeObject.Key == attachment.StorageKey {
		t.Fatal("authoritative key unexpectedly matched stale attachment key")
	}
	events := []string{}
	deletedKey := ""
	f.s.assets = recordingAssetStore{Store: f.s.assets, events: &events, deletedKey: &deletedKey}
	f.s.store = recordingAttachmentStore{Store: f.store, events: &events, key: authoritativeObject.Key}
	f.router = f.s.Router()

	response := f.do(t, http.MethodPost, "/attachments/"+attachment.ID.String()+"/delete", strings.NewReader(""), "application/x-www-form-urlencoded")

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/records/"+record.ID.String() {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if len(events) != 2 || events[0] != "metadata" || events[1] != "asset" {
		t.Fatalf("delete events=%v, want [metadata asset]", events)
	}
	if deletedKey != authoritativeObject.Key {
		t.Fatalf("deleted key=%q, want authoritative key %q", deletedKey, authoritativeObject.Key)
	}
	if _, _, err := f.store.GetAttachment(t.Context(), attachment.ID); err != repository.ErrNotFound {
		t.Fatalf("attachment metadata remains: %v", err)
	}
	if object, err := f.s.assets.Open(t.Context(), authoritativeObject.Key); err == nil {
		_ = object.Close()
		t.Fatalf("authoritative asset remains at %q", authoritativeObject.Key)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking removed authoritative asset: %v", err)
	}
	staleObject, err := f.s.assets.Open(t.Context(), attachment.StorageKey)
	if err != nil {
		t.Fatalf("stale attachment key was deleted instead of authoritative key: %v", err)
	}
	_ = staleObject.Close()
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
