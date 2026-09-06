package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) remindersPage(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	data, err := s.base(request, "Reminders")
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Vehicle = vehicle
	data.Types, err = s.store.ListServiceTypes(request.Context())
	if err != nil {
		s.fail(response, err)
		return
	}
	reminders, err := s.store.ListReminders(request.Context(), &vehicle.ID, true)
	if err != nil {
		s.fail(response, err)
		return
	}
	for _, schedule := range reminders {
		data.Reminders = append(data.Reminders, reminder.Evaluate(schedule, day(s.now())))
	}
	s.render(response, 200, "reminders", data)
}

func (s *Server) upsertReminder(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	if err = request.ParseForm(); err != nil {
		http.Error(response, "Invalid form", 400)
		return
	}
	serviceTypeID, err := uuid.Parse(request.FormValue("service_type_id"))
	if err != nil {
		http.Error(response, "Service type is required", 400)
		return
	}
	months, monthsErr := optionalPositiveInt(request.FormValue("months"))
	miles, milesErr := optionalPositiveInt64(request.FormValue("miles"))
	if monthsErr != nil || milesErr != nil || (months == nil && miles == nil) {
		http.Error(response, "Set a positive month or mileage interval", 400)
		return
	}
	now := s.now()
	savedReminder := model.Reminder{
		ID:               uuid.New(),
		VehicleID:        vehicle.ID,
		ServiceTypeID:    serviceTypeID,
		IntervalMonths:   months,
		IntervalMiles:    miles,
		StartingOdometer: vehicle.LatestOdometer,
		Enabled:          request.FormValue("enabled") == "true",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if _, err = s.store.UpsertReminder(request.Context(), savedReminder); err != nil {
		s.fail(response, err)
		return
	}
	if isHTMX(request) {
		s.renderReminderList(response, request, vehicle)
		return
	}
	http.Redirect(response, request, "/vehicles/"+vehicle.ID.String()+"/reminders", 303)
}

func (s *Server) deleteReminder(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(request, "reminderID"))
	if err != nil {
		http.NotFound(response, request)
		return
	}
	rows, err := s.store.ListReminders(request.Context(), &vehicle.ID, true)
	if err != nil {
		s.fail(response, err)
		return
	}
	found := false
	for _, row := range rows {
		if row.ID == id {
			found = true
			break
		}
	}
	if !found {
		http.NotFound(response, request)
		return
	}
	if err = s.store.DeleteReminder(request.Context(), id); err != nil {
		s.notFound(response, err)
		return
	}
	if isHTMX(request) {
		s.renderReminderList(response, request, vehicle)
		return
	}
	http.Redirect(response, request, "/vehicles/"+vehicle.ID.String()+"/reminders", http.StatusSeeOther)
}

func (s *Server) renderReminderList(response http.ResponseWriter, request *http.Request, vehicle model.Vehicle) {
	data, err := s.base(request, "Reminders")
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Vehicle = vehicle
	data.Types, err = s.store.ListServiceTypes(request.Context())
	if err != nil {
		s.fail(response, err)
		return
	}
	rows, err := s.store.ListReminders(request.Context(), &vehicle.ID, true)
	if err != nil {
		s.fail(response, err)
		return
	}
	for _, row := range rows {
		data.Reminders = append(data.Reminders, reminder.Evaluate(row, day(s.now())))
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err = s.ui.RenderNamed(response, "reminders", "reminder-list", data); err != nil {
		s.fail(response, err)
	}
}

func optionalPositiveInt(raw string) (*int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return nil, errors.New("positive")
	}
	return &n, nil
}

func optionalPositiveInt64(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return nil, errors.New("positive")
	}
	return &n, nil
}
