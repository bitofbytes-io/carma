package server

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/bitofbytes-io/carma/internal/metrics"
	"github.com/google/uuid"
)

// savePhoto stores an uploaded vehicle photo and returns its key, or "" when
// none was uploaded. If the form or the photo is invalid it stores nothing and
// returns the validation message.
func (s *Server) savePhoto(request *http.Request, validation string) (string, string) {
	file, _, err := request.FormFile("photo")
	if err != nil {
		return "", validation
	}
	defer file.Close()
	object, err := s.assets.Save(request.Context(), file, s.cfg.MaxUploadBytes)
	if err != nil {
		s.metrics.Upload(uploadVehiclePhoto, uploadResult(err))
		return "", "Photo must be JPEG, PNG, WebP, or HEIC and within the upload limit."
	}
	if !strings.HasPrefix(object.ContentType, "image/") {
		validation = "Vehicle photo must be an image."
	}
	if validation != "" {
		// A photo discarded with an invalid form is refused, not stored.
		s.metrics.Upload(uploadVehiclePhoto, metrics.ResultRejected)
		_ = s.assets.Delete(request.Context(), object.Key)
		return "", validation
	}
	return object.Key, ""
}

// deferPhotoCleanup keeps a new photo whose vehicle write failed: the write may
// have committed even when its result was not received. Orphan cleanup checks
// references before pruning retained uploads.
func deferPhotoCleanup(vehicleID uuid.UUID, photoKey string, err error) {
	if photoKey != "" {
		slog.Warn("vehicle photo cleanup deferred", "vehicle_id", vehicleID, "storage_key", photoKey, "error", err)
	}
}

func (s *Server) vehiclePhoto(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil || vehicle.PhotoKey == "" {
		http.NotFound(response, request)
		return
	}
	s.serveObject(response, request, vehicle.PhotoKey, filepath.Base(vehicle.PhotoKey), contentTypeForKey(vehicle.PhotoKey))
}
