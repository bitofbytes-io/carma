package server

import (
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) addAttachments(response http.ResponseWriter, request *http.Request) {
	record, _, err := s.getRecord(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	if err = s.parseMultipart(response, request, 16<<20); err != nil {
		s.multipartError(response, err)
		return
	}
	attachments, keys, err := s.saveUploads(request, record.ID, "receipts")
	if err != nil {
		s.cleanup(request, keys)
		http.Error(response, err.Error(), 400)
		return
	}
	if len(attachments) == 0 {
		http.Error(response, "Choose at least one receipt", 400)
		return
	}
	if err = s.store.AddAttachments(request.Context(), record.ID, attachments); err != nil {
		s.cleanup(request, keys)
		s.fail(response, err)
		return
	}
	http.Redirect(response, request, "/records/"+record.ID.String(), 303)
}

func (s *Server) saveUploads(request *http.Request, recordID uuid.UUID, field string) ([]model.Attachment, []string, error) {
	if request.MultipartForm == nil {
		return nil, nil, nil
	}
	var attachments []model.Attachment
	var keys []string
	for _, header := range request.MultipartForm.File[field] {
		file, err := header.Open()
		if err != nil {
			return attachments, keys, err
		}
		object, err := s.assets.Save(request.Context(), file, s.cfg.MaxUploadBytes)
		_ = file.Close()
		if err != nil {
			return attachments, keys, fmt.Errorf("%s: %w", safeFilename(header.Filename), err)
		}
		keys = append(keys, object.Key)
		attachments = append(attachments, model.Attachment{
			ID:               uuid.New(),
			RecordID:         recordID,
			OriginalFilename: safeFilename(header.Filename),
			ContentType:      object.ContentType,
			ByteSize:         object.Size,
			StorageKey:       object.Key,
			CreatedAt:        s.now(),
		})
	}
	return attachments, keys, nil
}

func (s *Server) attachment(response http.ResponseWriter, request *http.Request) {
	_, attachment, err := s.findAttachment(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	s.serveObject(response, request, attachment.StorageKey, attachment.OriginalFilename, attachment.ContentType)
}

func (s *Server) deleteAttachment(response http.ResponseWriter, request *http.Request) {
	record, a, err := s.findAttachment(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	key, err := s.store.DeleteAttachment(request.Context(), a.ID)
	if err != nil {
		s.notFound(response, err)
		return
	}
	if err = s.assets.Delete(request.Context(), key); err != nil {
		slog.Warn("attachment asset cleanup deferred", "storage_key", key, "error", err)
	}
	http.Redirect(response, request, "/records/"+record.ID.String(), 303)
}

func (s *Server) findAttachment(request *http.Request) (model.Record, model.Attachment, error) {
	id, err := uuid.Parse(chi.URLParam(request, "attachmentID"))
	if err != nil {
		return model.Record{}, model.Attachment{}, repository.ErrNotFound
	}
	return s.store.GetAttachment(request.Context(), id)
}

func (s *Server) serveObject(response http.ResponseWriter, request *http.Request, key, name, contentType string) {
	file, err := s.assets.Open(request.Context(), key)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	defer file.Close()
	end, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		s.fail(response, err)
		return
	}
	_, _ = file.Seek(0, io.SeekStart)
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": safeFilename(name)}))
	http.ServeContent(response, request, name, time.Time{}, file)
	_ = end
}

func (s *Server) cleanup(request *http.Request, keys []string) {
	for _, key := range keys {
		if err := s.assets.Delete(request.Context(), key); err != nil {
			slog.Warn("asset cleanup failed", "key", key, "error", err)
		}
	}
}

func safeFilename(value string) string {
	value = filepath.Base(strings.ReplaceAll(value, "\\", "/"))
	value = strings.Map(func(character rune) rune {
		if character < 32 || character == 127 {
			return -1
		}
		return character
	}, value)
	if strings.TrimSpace(value) == "" {
		return "receipt"
	}
	return value
}

func contentTypeForKey(key string) string {
	switch strings.ToLower(filepath.Ext(key)) {
	case ".jpg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".heic":
		return "image/heic"
	default:
		return "application/octet-stream"
	}
}
