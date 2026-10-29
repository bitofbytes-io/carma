package server

import (
	"bytes"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	carmaexport "github.com/bitofbytes-io/carma/internal/export"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func (s *Server) vehicleCSV(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	s.writeCSV(response, request, recordQuery(request, &vehicle.ID), "carma-"+slug(vehicle.Nickname)+"-"+s.now().Format("2006-01-02")+".csv")
}

func (s *Server) vehicleXLSX(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	s.writeXLSX(response, request, historyExportQuery(request, &vehicle.ID), "carma-"+slug(vehicle.Nickname)+"-"+s.now().Format("2006-01-02")+".xlsx")
}

// vehicleHistory renders the printable, sale-ready service history: every
// record oldest first with its totals. The browser's print dialog saves it as a
// PDF.
func (s *Server) vehicleHistory(response http.ResponseWriter, request *http.Request) {
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
	data.Records, err = s.store.ListRecords(request.Context(), carmaexport.Query(&vehicle.ID))
	if err != nil {
		s.fail(response, err)
		return
	}
	data.History = carmaexport.Summarize(data.Records)
	data.Generated = s.today()
	s.render(response, http.StatusOK, "history", data)
}

func (s *Server) globalCSV(response http.ResponseWriter, request *http.Request) {
	s.writeCSV(response, request, recordQuery(request, nil), "carma-all-records-"+s.now().Format("2006-01-02")+".csv")
}

func (s *Server) globalXLSX(response http.ResponseWriter, request *http.Request) {
	s.writeXLSX(response, request, historyExportQuery(request, nil), "carma-all-records-"+s.now().Format("2006-01-02")+".xlsx")
}

// historyExportQuery is the printable history list with the records view's
// search, service type, and date filters applied. Sort stays oldest first.
func historyExportQuery(request *http.Request, vehicleID *uuid.UUID) model.RecordQuery {
	query := carmaexport.Query(vehicleID)
	view := recordQuery(request, vehicleID)
	query.Search = view.Search
	query.ServiceTypeID = view.ServiceTypeID
	query.From = view.From
	query.To = view.To
	return query
}

func (s *Server) writeCSV(response http.ResponseWriter, request *http.Request, query model.RecordQuery, name string) {
	rows, err := s.store.ListRecords(request.Context(), query)
	if err != nil {
		s.fail(response, err)
		return
	}
	response.Header().Set("Content-Type", "text/csv; charset=utf-8")
	response.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	_, _ = response.Write([]byte{0xEF, 0xBB, 0xBF})
	if err = carmaexport.CSV(response, rows); err != nil {
		slog.Error("csv export", "error", err)
	}
}

func (s *Server) writeXLSX(response http.ResponseWriter, request *http.Request, query model.RecordQuery, name string) {
	rows, err := s.store.ListRecords(request.Context(), query)
	if err != nil {
		s.fail(response, err)
		return
	}
	var body bytes.Buffer
	if err = carmaexport.XLSX(&body, rows); err != nil {
		s.fail(response, err)
		return
	}
	response.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	response.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	if _, err = response.Write(body.Bytes()); err != nil {
		slog.Error("xlsx export", "error", err)
	}
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	dash := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			builder.WriteRune(character)
			dash = false
		} else if !dash {
			builder.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}
