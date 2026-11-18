package server

import (
	"net/http"
)

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
	if data.RecallsCheckedAt, err = s.store.RecallCheckedAt(request.Context(), vehicle.ID); err != nil {
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
	data.WarrantyActive = s.warrantyActive(data.Records, vehicle)
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
