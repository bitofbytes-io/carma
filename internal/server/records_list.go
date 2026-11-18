package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func (s *Server) allRecords(response http.ResponseWriter, request *http.Request) {
	data, err := s.base(request, "All records")
	if err != nil {
		s.fail(response, err)
		return
	}
	data.RecordsURL = "/records"
	data.Params = cleanParams(request.URL.Query())
	data.Records, err = s.store.ListRecords(request.Context(), recordQuery(request, nil))
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Types, err = s.store.ListServiceTypes(request.Context())
	if err != nil {
		s.fail(response, err)
		return
	}
	if isHTMX(request) {
		s.renderNamed(response, "records", "records-section", data)
		return
	}
	s.render(response, 200, "records", data)
}

func recordQuery(request *http.Request, vehicleID *uuid.UUID) model.RecordQuery {
	params := request.URL.Query()
	query := model.RecordQuery{
		VehicleID: vehicleID,
		Search:    strings.TrimSpace(params.Get("q")),
		Sort:      params.Get("sort"),
		Desc:      params.Get("direction") != "asc",
	}
	if id, err := uuid.Parse(params.Get("type")); err == nil {
		query.ServiceTypeID = &id
	}
	if date, err := time.Parse("2006-01-02", params.Get("from")); err == nil {
		query.From = &date
	}
	if date, err := time.Parse("2006-01-02", params.Get("to")); err == nil {
		query.To = &date
	}
	return query
}

func cleanParams(in url.Values) url.Values {
	params := url.Values{}
	for _, key := range []string{"q", "type", "from", "to", "sort", "direction"} {
		if value := in.Get(key); value != "" {
			params.Set(key, value)
		}
	}
	return params
}

// warrantyActive marks the records whose warranty still covers the vehicle
// today, judged at the vehicle's latest odometer. All records must belong to
// that vehicle.
func (s *Server) warrantyActive(records []model.Record, vehicle model.Vehicle) map[uuid.UUID]bool {
	today := s.today()
	active := map[uuid.UUID]bool{}
	for _, record := range records {
		if record.WarrantyActive(today, vehicle.LatestOdometer) {
			active[record.ID] = true
		}
	}
	return active
}
