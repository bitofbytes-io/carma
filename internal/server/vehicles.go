package server

import (
	"context"
	"net/http"

	"github.com/bitofbytes-io/carma/internal/metrics"
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
	created, err := s.store.CreateVehicle(request.Context(), vehicle, reminders)
	if err != nil {
		s.countUploads(uploadVehiclePhoto, metrics.ResultFailure, []string{photoKey})
		deferPhotoCleanup(vehicle.ID, photoKey, err)
		s.fail(response, err)
		return
	}
	s.countUploads(uploadVehiclePhoto, metrics.ResultSuccess, []string{photoKey})
	s.checkRecallsSoon(created)
	http.Redirect(response, request, "/vehicles/"+vehicle.ID.String(), http.StatusSeeOther)
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
	updated, replacedKey, err := s.store.UpdateVehicle(request.Context(), vehicle)
	if err != nil {
		s.countUploads(uploadVehiclePhoto, metrics.ResultFailure, []string{photoKey})
		deferPhotoCleanup(vehicle.ID, photoKey, err)
		s.fail(response, err)
		return
	}
	if replacedKey != "" {
		_ = s.assets.Delete(request.Context(), replacedKey)
	}
	s.countUploads(uploadVehiclePhoto, metrics.ResultSuccess, []string{photoKey})
	s.checkRecallsSoon(updated)
	http.Redirect(response, request, "/vehicles/"+vehicle.ID.String(), http.StatusSeeOther)
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

func (s *Server) restoreVehicle(response http.ResponseWriter, request *http.Request) {
	id, err := uuid.Parse(chi.URLParam(request, "vehicleID"))
	if err != nil {
		http.NotFound(response, request)
		return
	}
	if err = s.store.RestoreVehicle(request.Context(), id); err != nil {
		s.notFound(response, err)
		return
	}
	redirectAfterPost(response, request, "/")
}

func (s *Server) getVehicle(request *http.Request) (model.Vehicle, error) {
	id, err := uuid.Parse(chi.URLParam(request, "vehicleID"))
	if err != nil {
		return model.Vehicle{}, repository.ErrNotFound
	}
	return s.store.GetVehicle(request.Context(), id)
}
