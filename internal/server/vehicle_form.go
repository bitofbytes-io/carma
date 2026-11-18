package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/bitofbytes-io/carma/internal/ui"
	"github.com/google/uuid"
)

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

func vehicleFromForm(request *http.Request) (model.Vehicle, string) {
	vehicle := model.Vehicle{
		Nickname:     strings.TrimSpace(request.FormValue("nickname")),
		Make:         strings.TrimSpace(request.FormValue("make")),
		Model:        strings.TrimSpace(request.FormValue("model")),
		Trim:         strings.TrimSpace(request.FormValue("trim")),
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
		n, err := model.ParseOdometerMiles(raw)
		if err != nil {
			return vehicle, "Current odometer must be a nonnegative whole number."
		}
		vehicle.CurrentOdometer = &n
	}
	return vehicle, ""
}
