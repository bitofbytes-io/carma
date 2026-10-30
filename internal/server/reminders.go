package server

import (
	"context"
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
	data, err := s.reminderPageData(request, vehicle)
	if err != nil {
		s.fail(response, err)
		return
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
	http.Redirect(response, request, "/vehicles/"+vehicle.ID.String()+"/reminders", http.StatusSeeOther)
}

func (s *Server) snoozeReminder(response http.ResponseWriter, request *http.Request) {
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
	if err = request.ParseForm(); err != nil {
		http.Error(response, "Invalid form", http.StatusBadRequest)
		return
	}
	rows, err := s.store.ListReminders(request.Context(), &vehicle.ID, true)
	if err != nil {
		s.fail(response, err)
		return
	}
	var current *model.Reminder
	for i := range rows {
		if rows[i].ID == id {
			current = &rows[i]
			break
		}
	}
	if current == nil {
		http.NotFound(response, request)
		return
	}
	window, err := reminder.ResolveSnooze(request.FormValue("preset"), request.FormValue("until_on"), request.FormValue("until_miles"), s.today(), current.LatestOdometer)
	if err != nil {
		message, ok := snoozeFormError(err)
		if !ok {
			s.fail(response, err)
			return
		}
		s.writeReminders(response, request, vehicle, message, id)
		return
	}
	if err = s.store.SetReminderSnooze(request.Context(), vehicle.ID, id, window.UntilOn, window.UntilMiles, s.now()); err != nil {
		s.notFound(response, err)
		return
	}
	if isHTMX(request) {
		s.renderReminderList(response, request, vehicle)
		return
	}
	http.Redirect(response, request, "/vehicles/"+vehicle.ID.String()+"/reminders", http.StatusSeeOther)
}

func snoozeFormError(err error) (string, bool) {
	switch {
	case errors.Is(err, reminder.ErrSnoozeMileageUnknown):
		return "Set a current mileage before snoozing by miles.", true
	case errors.Is(err, reminder.ErrSnoozeDate):
		return "Choose a snooze date after today.", true
	case errors.Is(err, reminder.ErrSnoozeMiles):
		return "Snooze mileage must be a positive whole number.", true
	case errors.Is(err, reminder.ErrSnoozeMilesLow):
		return "Snooze mileage must be above the current reading.", true
	case errors.Is(err, reminder.ErrSnoozeEmpty):
		return "Choose a date or a mileage.", true
	case errors.Is(err, reminder.ErrSnoozePreset):
		return "Choose a snooze option.", true
	default:
		return "", false
	}
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
	s.writeReminders(response, request, vehicle, "", uuid.Nil)
}

// writeReminders renders the reminder list. A form error on a full-page POST
// is a 400 document; htmx receives the list partial so the message can swap in.
func (s *Server) writeReminders(response http.ResponseWriter, request *http.Request, vehicle model.Vehicle, formError string, snoozeID uuid.UUID) {
	data, err := s.reminderPageData(request, vehicle)
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Error = formError
	data.SnoozeReminderID = snoozeID
	if formError != "" && !isHTMX(request) {
		s.render(response, http.StatusBadRequest, "reminders", data)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err = s.ui.RenderNamed(response, "reminders", "reminder-list", data); err != nil {
		s.fail(response, err)
	}
}

func (s *Server) reminderPageData(request *http.Request, vehicle model.Vehicle) (pageData, error) {
	data, err := s.base(request, "Reminders")
	if err != nil {
		return data, err
	}
	data.Vehicle = vehicle
	data.Types, err = s.store.ListServiceTypes(request.Context())
	if err != nil {
		return data, err
	}
	rows, err := s.store.ListReminders(request.Context(), &vehicle.ID, true)
	if err != nil {
		return data, err
	}
	data.Reminders, err = s.evaluateReminders(request.Context(), rows)
	return data, err
}

// evaluateReminders evaluates reminders on today, projecting mileage intervals
// from each vehicle's recent mileage history.
func (s *Server) evaluateReminders(ctx context.Context, reminders []model.Reminder) ([]reminder.Result, error) {
	today := s.today()
	points, err := s.store.ListMileagePoints(ctx, reminder.ProjectionVehicles(reminders), today.AddDate(0, -reminder.EstimateWindowMonths, 0))
	if err != nil {
		return nil, err
	}
	return reminder.EvaluateAll(reminders, points, today), nil
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
