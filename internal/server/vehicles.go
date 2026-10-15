package server

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/ui"
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
	if err = s.loadAttention(request.Context(), &data); err != nil {
		s.fail(response, err)
		return
	}
	s.render(response, http.StatusOK, "dashboard", data)
}

// attention returns the dashboard's overdue and due-soon reminders.
func (s *Server) attention(ctx context.Context) ([]reminder.Result, error) {
	reminders, err := s.store.ListReminders(ctx, nil, false)
	if err != nil {
		return nil, err
	}
	evaluated, err := s.evaluateReminders(ctx, reminders)
	if err != nil {
		return nil, err
	}
	var results []reminder.Result
	for _, result := range evaluated {
		if result.Status == reminder.Due || result.Status == reminder.Soon {
			results = append(results, result)
		}
	}
	return results, nil
}

func (s *Server) archivedVehicles(response http.ResponseWriter, request *http.Request) {
	data, err := s.base(request, "Archived vehicles")
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Vehicles, err = s.store.ListVehicles(request.Context(), true)
	if err != nil {
		s.fail(response, err)
		return
	}
	s.render(response, http.StatusOK, "archived", data)
}

func (s *Server) newVehicle(response http.ResponseWriter, request *http.Request) {
	data, err := s.base(request, "Add vehicle")
	if err != nil {
		s.fail(response, err)
		return
	}
	data.ReminderPresets = ui.ReminderPresets(nil)
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
	if err := s.parseMultipart(response, request, 8<<20); err != nil {
		s.multipartError(response, err)
		return
	}
	vehicle, validation := vehicleFromForm(request)
	vehicle.ID = uuid.New()
	vehicle.CreatedAt = s.now()
	vehicle.UpdatedAt = vehicle.CreatedAt
	today := s.today()
	vehicle.CurrentOdometerOn = &today
	reminders, err := s.presetReminders(request.Context(), vehicle, request.Form["reminder_preset"])
	if err != nil {
		s.fail(response, err)
		return
	}
	photoKey, validation := s.savePhoto(request, validation)
	if validation != "" {
		s.vehicleFormError(response, request, vehicle, false, validation)
		return
	}
	vehicle.PhotoKey = photoKey
	if _, err := s.store.CreateVehicle(request.Context(), vehicle, reminders); err != nil {
		deferPhotoCleanup(vehicle.ID, photoKey, err)
		s.fail(response, err)
		return
	}
	http.Redirect(response, request, "/vehicles/"+vehicle.ID.String(), 303)
}

func (s *Server) updateVehicle(response http.ResponseWriter, request *http.Request) {
	if err := s.parseMultipart(response, request, 8<<20); err != nil {
		s.multipartError(response, err)
		return
	}
	old, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	vehicle, validation := vehicleFromForm(request)
	vehicle.ID = old.ID
	vehicle.CreatedAt = old.CreatedAt
	vehicle.UpdatedAt = s.now()
	vehicle.UpdatedBy = middleware.User(request).ID
	today := s.today()
	vehicle.CurrentOdometerOn = &today
	photoKey, validation := s.savePhoto(request, validation)
	if validation != "" {
		vehicle.PhotoKey = old.PhotoKey
		s.vehicleFormError(response, request, vehicle, true, validation)
		return
	}
	// An empty key keeps whatever photo is stored now, which may be newer than
	// the one loaded above.
	vehicle.PhotoKey = photoKey
	_, replacedKey, err := s.store.UpdateVehicle(request.Context(), vehicle)
	if err != nil {
		deferPhotoCleanup(vehicle.ID, photoKey, err)
		s.fail(response, err)
		return
	}
	if replacedKey != "" {
		_ = s.assets.Delete(request.Context(), replacedKey)
	}
	http.Redirect(response, request, "/vehicles/"+vehicle.ID.String(), 303)
}

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
		return "", "Photo must be JPEG, PNG, WebP, or HEIC and within the upload limit."
	}
	if !strings.HasPrefix(object.ContentType, "image/") {
		validation = "Vehicle photo must be an image."
	}
	if validation != "" {
		_ = s.assets.Delete(request.Context(), object.Key)
		return "", validation
	}
	return object.Key, ""
}

func (s *Server) vehicleFormError(response http.ResponseWriter, request *http.Request, vehicle model.Vehicle, editing bool, validation string) {
	data, _ := s.base(request, "Vehicle")
	data.Editing = editing
	data.Vehicle = vehicle
	data.Error = validation
	if !editing {
		data.ReminderPresets = ui.ReminderPresets(request.Form["reminder_preset"])
	}
	s.render(response, 400, "vehicle-form", data)
}

// presetReminders builds the starter reminders checked on the new-vehicle
// form. Like reminders added later, they start from the vehicle's creation
// date and mileage; when that mileage is unknown, mileage presets wait for the
// first reading. Unknown presets and missing service types are skipped.
func (s *Server) presetReminders(ctx context.Context, vehicle model.Vehicle, selected []string) ([]model.Reminder, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	types, err := s.store.ListServiceTypes(ctx)
	if err != nil {
		return nil, err
	}
	var reminders []model.Reminder
	added := map[uuid.UUID]bool{}
	for _, name := range selected {
		preset, ok := reminder.FindPreset(name)
		if !ok {
			continue
		}
		for _, serviceType := range types {
			if !strings.EqualFold(serviceType.Name, preset.ServiceType) || added[serviceType.ID] {
				continue
			}
			added[serviceType.ID] = true
			saved := model.Reminder{
				ID:               uuid.New(),
				VehicleID:        vehicle.ID,
				ServiceTypeID:    serviceType.ID,
				StartingOdometer: vehicle.CurrentOdometer,
				Enabled:          true,
				CreatedAt:        vehicle.CreatedAt,
				UpdatedAt:        vehicle.CreatedAt,
			}
			if preset.Months > 0 {
				months := preset.Months
				saved.IntervalMonths = &months
			}
			if preset.Miles > 0 {
				miles := preset.Miles
				saved.IntervalMiles = &miles
				// Without a starting odometer, the first mileage entered by an
				// edit or check-in becomes the baseline.
				saved.StartingOdometerPending = vehicle.CurrentOdometer == nil
			}
			reminders = append(reminders, saved)
		}
	}
	return reminders, nil
}

// deferPhotoCleanup keeps a new photo whose vehicle write failed: the write may
// have committed even when its result was not received. Orphan cleanup checks
// references before pruning retained uploads.
func deferPhotoCleanup(vehicleID uuid.UUID, photoKey string, err error) {
	if photoKey != "" {
		slog.Warn("vehicle photo cleanup deferred", "vehicle_id", vehicleID, "storage_key", photoKey, "error", err)
	}
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
	data.RecordsURL = "/vehicles/" + vehicle.ID.String()
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
	if data.Reminders, err = s.evaluateReminders(request.Context(), reminders); err != nil {
		s.fail(response, err)
		return
	}
	recalls, err := s.store.ListRecalls(request.Context(), &vehicle.ID, true)
	if err != nil {
		s.fail(response, err)
		return
	}
	for _, recall := range recalls {
		if recall.DismissedAt == nil {
			data.Recalls = append(data.Recalls, recall)
		} else {
			data.DismissedRecalls = append(data.DismissedRecalls, recall)
		}
	}
	if isHTMX(request) {
		s.renderNamed(response, "vehicle", "records-section", data)
		return
	}
	// The spend card is outside the records section, so filter requests skip it.
	today := s.today()
	data.SpendYear = today.Year()
	data.Spend, err = s.store.VehicleSpend(request.Context(), vehicle.ID, today)
	if err != nil {
		s.fail(response, err)
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
