package server

import (
	"log/slog"
	"mime"
	"net/http"
	"strings"

	carmaexport "github.com/bitofbytes-io/carma/internal/export"
	"github.com/bitofbytes-io/carma/internal/model"
)

func (s *Server) vehicleCSV(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	s.writeCSV(response, request, recordQuery(request, &vehicle.ID), "carma-"+slug(vehicle.Nickname)+"-"+s.now().Format("2006-01-02")+".csv")
}

func (s *Server) globalCSV(response http.ResponseWriter, request *http.Request) {
	s.writeCSV(response, request, recordQuery(request, nil), "carma-all-records-"+s.now().Format("2006-01-02")+".csv")
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
