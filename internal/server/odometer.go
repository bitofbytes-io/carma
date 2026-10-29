package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/ui"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// addOdometerReading records a quick mileage check-in from the dashboard or
// vehicle page. A reading below the latest mileage needs confirm=1.
func (s *Server) addOdometerReading(response http.ResponseWriter, request *http.Request) {
	id, err := uuid.Parse(chi.URLParam(request, "vehicleID"))
	if err != nil {
		http.NotFound(response, request)
		return
	}
	if err = request.ParseForm(); err != nil {
		http.Error(response, "Invalid form", http.StatusBadRequest)
		return
	}
	form := ui.OdometerForm{Return: "dashboard", Value: strings.TrimSpace(request.FormValue("odometer_miles"))}
	if request.FormValue("return") == "vehicle" {
		form.Return = "vehicle"
	}
	miles, err := model.ParseOdometerMiles(form.Value)
	if err != nil {
		if form.Vehicle, err = s.store.GetVehicle(request.Context(), id); err != nil {
			s.notFound(response, err)
			return
		}
		form.Error = "Mileage must be a nonnegative whole number."
		s.odometerFormError(response, request, form, http.StatusBadRequest)
		return
	}
	vehicle, err := s.store.AddOdometerReading(request.Context(), model.OdometerReading{
		ID:            uuid.New(),
		VehicleID:     id,
		ReadOn:        s.today(),
		OdometerMiles: miles,
		CreatedBy:     middleware.User(request).ID,
		CreatedAt:     s.now(),
	}, request.FormValue("confirm") == "1")
	if errors.Is(err, repository.ErrOdometerBelowLatest) {
		form.Vehicle = vehicle
		form.Confirm = true
		form.Error = fmt.Sprintf("That's lower than the latest %d mi. Save anyway to keep it in the history; the vehicle stays at %d mi.", *vehicle.LatestOdometer, *vehicle.LatestOdometer)
		s.odometerFormError(response, request, form, http.StatusConflict)
		return
	}
	if err != nil {
		s.notFound(response, err)
		return
	}
	if form.Return == "vehicle" {
		redirectAfterPost(response, request, "/vehicles/"+id.String())
		return
	}
	if !isHTMX(request) {
		http.Redirect(response, request, "/", http.StatusSeeOther)
		return
	}
	var data pageData
	if data.OdometerForm.Vehicle, err = s.dashboardVehicle(request.Context(), vehicle); err != nil {
		s.fail(response, err)
		return
	}
	data.OdometerForm.Return = "dashboard"
	if err = s.loadAttention(request.Context(), &data); err != nil {
		s.fail(response, err)
		return
	}
	s.renderNamed(response, "dashboard", "odometer-update", data)
}

// odometerFormError shows a rejected mileage update. htmx only swaps successful
// responses, so its inline error is sent with 200; other browsers get a page.
func (s *Server) odometerFormError(response http.ResponseWriter, request *http.Request, form ui.OdometerForm, status int) {
	if isHTMX(request) {
		if form.Return == "vehicle" {
			s.renderNamed(response, "vehicle", "odometer-form", form)
			return
		}
		vehicle, err := s.dashboardVehicle(request.Context(), form.Vehicle)
		if err != nil {
			s.fail(response, err)
			return
		}
		form.Vehicle = vehicle
		s.renderNamed(response, "dashboard", "vehicle-card", form)
		return
	}
	data, err := s.base(request, "Update mileage")
	if err != nil {
		s.fail(response, err)
		return
	}
	data.OdometerForm = form
	s.render(response, status, "odometer", data)
}

// dashboardVehicle reloads vehicle as the dashboard lists it, with its last
// record, falling back to vehicle when it is not listed there.
func (s *Server) dashboardVehicle(ctx context.Context, vehicle model.Vehicle) (model.Vehicle, error) {
	vehicles, err := s.store.ListVehicles(ctx, false)
	if err != nil {
		return vehicle, err
	}
	for _, listed := range vehicles {
		if listed.ID == vehicle.ID {
			return listed, nil
		}
	}
	return vehicle, nil
}
