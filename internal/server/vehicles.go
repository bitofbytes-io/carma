package server

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) dashboard(response http.ResponseWriter, request *http.Request) {
	data, err := s.base(request, "Garage")
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Vehicles, err = s.store.ListVehicles(request.Context(), false)
	if err != nil {
		s.fail(response, err)
		return
	}
	reminders, err := s.store.ListReminders(request.Context(), nil, false)
	if err != nil {
		s.fail(response, err)
		return
	}
	today := day(s.now())
	for _, schedule := range reminders {
		result := reminder.Evaluate(schedule, today)
		if result.Status == reminder.Due || result.Status == reminder.Soon {
			data.Attention = append(data.Attention, result)
		}
	}
	s.render(response, http.StatusOK, "dashboard", data)
}

func (s *Server) archivedVehicles(response http.ResponseWriter, request *http.Request) {
	data, err := s.base(request, "Archived vehicles")
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Archived = true
	data.Vehicles, err = s.store.ListVehicles(request.Context(), true)
	if err != nil {
		s.fail(response, err)
		return
	}
	s.render(response, http.StatusOK, "records", data)
}

func (s *Server) newVehicle(response http.ResponseWriter, request *http.Request) {
	data, err := s.base(request, "Add vehicle")
	if err != nil {
		s.fail(response, err)
		return
	}
	s.render(response, http.StatusOK, "vehicle-form", data)
}

func (s *Server) editVehicle(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	data, err := s.base(request, "Edit "+vehicle.Nickname)
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Editing = true
	// Show the effective latest mileage in the editable field, including mileage
	// known only from historical records on vehicles created before this field.
	vehicle.CurrentOdometer = vehicle.LatestOdometer
	data.Vehicle = vehicle
	s.render(response, http.StatusOK, "vehicle-form", data)
}

func (s *Server) createVehicle(response http.ResponseWriter, request *http.Request) {
	s.saveVehicle(response, request, false)
}

func (s *Server) updateVehicle(response http.ResponseWriter, request *http.Request) {
	s.saveVehicle(response, request, true)
}

func (s *Server) saveVehicle(response http.ResponseWriter, request *http.Request, editing bool) {
	if err := s.parseMultipart(response, request, 8<<20); err != nil {
		s.multipartError(response, err)
		return
	}
	var old model.Vehicle
	var err error
	if editing {
		old, err = s.getVehicle(request)
		if err != nil {
			s.notFound(response, err)
			return
		}
	}
	vehicle, validation := vehicleFromForm(request)
	if editing {
		vehicle.ID = old.ID
		vehicle.PhotoKey = old.PhotoKey
		vehicle.CreatedAt = old.CreatedAt
	} else {
		vehicle.ID = uuid.New()
		vehicle.CreatedAt = s.now()
	}
	vehicle.UpdatedAt = s.now()
	var newKey string
	if file, header, err := request.FormFile("photo"); err == nil {
		defer file.Close()
		object, saveErr := s.assets.Save(request.Context(), file, s.cfg.MaxUploadBytes)
		if saveErr != nil {
			validation = "Photo must be JPEG, PNG, WebP, or HEIC and within the upload limit."
		} else if !strings.HasPrefix(object.ContentType, "image/") {
			_ = s.assets.Delete(request.Context(), object.Key)
			validation = "Vehicle photo must be an image."
		} else {
			vehicle.PhotoKey = object.Key
			newKey = object.Key
			_ = header
		}
	}
	if validation != "" {
		if newKey != "" {
			_ = s.assets.Delete(request.Context(), newKey)
		}
		if editing {
			vehicle.PhotoKey = old.PhotoKey
		}
		data, _ := s.base(request, "Vehicle")
		data.Editing = editing
		data.Vehicle = vehicle
		data.Error = validation
		s.render(response, 400, "vehicle-form", data)
		return
	}
	if editing {
		_, err = s.store.UpdateVehicle(request.Context(), vehicle)
	} else {
		_, err = s.store.CreateVehicle(request.Context(), vehicle)
	}
	if err != nil {
		if newKey != "" {
			// The write may have committed even when its result was not received.
			// Orphan cleanup checks references before pruning retained uploads.
			slog.Warn("vehicle photo cleanup deferred", "vehicle_id", vehicle.ID, "storage_key", newKey, "error", err)
		}
		s.fail(response, err)
		return
	}
	if editing && newKey != "" && old.PhotoKey != "" {
		_ = s.assets.Delete(request.Context(), old.PhotoKey)
	}
	http.Redirect(response, request, "/vehicles/"+vehicle.ID.String(), 303)
}

func vehicleFromForm(request *http.Request) (model.Vehicle, string) {
	vehicle := model.Vehicle{
		Nickname:     strings.TrimSpace(request.FormValue("nickname")),
		Make:         strings.TrimSpace(request.FormValue("make")),
		Model:        strings.TrimSpace(request.FormValue("model")),
		VIN:          strings.TrimSpace(request.FormValue("vin")),
		LicensePlate: strings.TrimSpace(request.FormValue("license_plate")),
		Notes:        strings.TrimSpace(request.FormValue("notes")),
	}
	if vehicle.Nickname == "" {
		return vehicle, "Nickname is required."
	}
	if raw := strings.TrimSpace(request.FormValue("year")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1886 || n > 9999 {
			return vehicle, "Year is invalid."
		}
		vehicle.Year = &n
	}
	if raw := strings.TrimSpace(request.FormValue("current_odometer")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return vehicle, "Current odometer must be a nonnegative whole number."
		}
		vehicle.CurrentOdometer = &n
	}
	return vehicle, ""
}

func (s *Server) archiveVehicle(response http.ResponseWriter, request *http.Request) {
	id, err := uuid.Parse(chi.URLParam(request, "vehicleID"))
	if err != nil {
		http.NotFound(response, request)
		return
	}
	if err = s.store.ArchiveVehicle(request.Context(), id); err != nil {
		s.notFound(response, err)
		return
	}
	redirectAfterPost(response, request, "/")
}

func (s *Server) vehiclePhoto(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil || vehicle.PhotoKey == "" {
		http.NotFound(response, request)
		return
	}
	s.serveObject(response, request, vehicle.PhotoKey, filepath.Base(vehicle.PhotoKey), contentTypeForKey(vehicle.PhotoKey))
}

func (s *Server) vehicle(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	data, err := s.base(request, vehicle.Nickname)
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Vehicle = vehicle
	data.Params = cleanParams(request.URL.Query())
	query := recordQuery(request, &vehicle.ID)
	data.Records, err = s.store.ListRecords(request.Context(), query)
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Types, err = s.store.ListServiceTypes(request.Context())
	if err != nil {
		s.fail(response, err)
		return
	}
	reminders, err := s.store.ListReminders(request.Context(), &vehicle.ID, false)
	if err != nil {
		s.fail(response, err)
		return
	}
	for _, schedule := range reminders {
		data.Reminders = append(data.Reminders, reminder.Evaluate(schedule, day(s.now())))
	}
	if isHTMX(request) {
		s.renderNamed(response, "vehicle", "records-section", data)
		return
	}
	s.render(response, 200, "vehicle", data)
}

func (s *Server) getVehicle(request *http.Request) (model.Vehicle, error) {
	id, err := uuid.Parse(chi.URLParam(request, "vehicleID"))
	if err != nil {
		return model.Vehicle{}, repository.ErrNotFound
	}
	return s.store.GetVehicle(request.Context(), id)
}
