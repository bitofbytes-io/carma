package server

import (
	"net/http"

	carmaexport "github.com/bitofbytes-io/carma/internal/export"
	"github.com/bitofbytes-io/carma/internal/model"
)

// vehicleHistoryPrint renders the buyer hand-off. It lists the same oldest-first
// service history as vehicleHistory, totaled by export.Summarize, plus open
// recalls. costs=hidden leaves prices out of the page.
func (s *Server) vehicleHistoryPrint(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	data, err := s.base(request, vehicle.Nickname+" service history")
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Vehicle = vehicle
	data.HideCosts = request.URL.Query().Get("costs") == "hidden"
	data.Records, err = s.store.ListRecords(request.Context(), model.RecordQuery{VehicleID: &vehicle.ID, Sort: "date"})
	if err != nil {
		s.fail(response, err)
		return
	}
	data.History = carmaexport.Summarize(data.Records)
	data.Generated = s.today()
	data.Recalls, err = s.store.ListRecalls(request.Context(), &vehicle.ID, false)
	if err != nil {
		s.fail(response, err)
		return
	}
	s.render(response, http.StatusOK, "history-print", data)
}
