package server

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) allRecords(response http.ResponseWriter, request *http.Request) {
	data, err := s.base(request, "All records")
	if err != nil {
		s.fail(response, err)
		return
	}
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

func (s *Server) newRecord(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	data, err := s.recordFormData(request, vehicle, model.Record{OccurredOn: day(s.now())}, false)
	if err != nil {
		s.fail(response, err)
		return
	}
	s.render(response, 200, "record-form", data)
}

func (s *Server) editRecord(response http.ResponseWriter, request *http.Request) {
	record, _, err := s.getRecord(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	vehicle, err := s.store.GetVehicle(request.Context(), record.VehicleID)
	if err != nil {
		s.notFound(response, err)
		return
	}
	data, err := s.recordFormData(request, vehicle, record, true)
	if err != nil {
		s.fail(response, err)
		return
	}
	s.render(response, 200, "record-form", data)
}

func (s *Server) recordFormData(request *http.Request, vehicle model.Vehicle, record model.Record, editing bool) (pageData, error) {
	data, err := s.base(request, "Record")
	if err != nil {
		return data, err
	}
	data.Vehicle = vehicle
	data.Record = record
	data.Editing = editing
	data.Types, err = s.store.ListServiceTypes(request.Context())
	return data, err
}

func (s *Server) createRecord(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	if err = s.parseMultipart(response, request, 16<<20); err != nil {
		s.multipartError(response, err)
		return
	}
	record, msg := recordFromForm(request)
	record.ID = uuid.New()
	record.VehicleID = vehicle.ID
	record.CreatedBy = middleware.User(request).ID
	record.CreatedAt = s.now()
	record.UpdatedAt = record.CreatedAt
	if msg != "" {
		s.recordFormError(response, request, vehicle, record, false, msg)
		return
	}
	attachments, keys, err := s.saveUploads(request, record.ID, "receipts")
	if err != nil {
		s.cleanup(request, keys)
		s.recordFormError(response, request, vehicle, record, false, err.Error())
		return
	}
	if _, err = s.store.CreateRecord(request.Context(), record, attachments); err != nil {
		s.cleanup(request, keys)
		s.fail(response, err)
		return
	}
	http.Redirect(response, request, "/records/"+record.ID.String(), 303)
}

func (s *Server) updateRecord(response http.ResponseWriter, request *http.Request) {
	old, _, err := s.getRecord(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	if err = request.ParseForm(); err != nil {
		http.Error(response, "Invalid form", 400)
		return
	}
	record, msg := recordFromForm(request)
	record.ID = old.ID
	record.VehicleID = old.VehicleID
	record.CreatedBy = old.CreatedBy
	record.CreatedAt = old.CreatedAt
	record.UpdatedAt = s.now()
	vehicle, err := s.store.GetVehicle(request.Context(), record.VehicleID)
	if err != nil {
		s.notFound(response, err)
		return
	}
	if msg != "" {
		s.recordFormError(response, request, vehicle, record, true, msg)
		return
	}
	if _, err = s.store.UpdateRecord(request.Context(), record); err != nil {
		s.fail(response, err)
		return
	}
	http.Redirect(response, request, "/records/"+record.ID.String(), 303)
}

func recordFromForm(request *http.Request) (model.Record, string) {
	record := model.Record{Vendor: strings.TrimSpace(request.FormValue("vendor")), Notes: strings.TrimSpace(request.FormValue("notes"))}
	var err error
	if record.OccurredOn, err = time.Parse("2006-01-02", request.FormValue("occurred_on")); err != nil {
		return record, "Date is required."
	}
	if record.ServiceTypeID, err = uuid.Parse(request.FormValue("service_type_id")); err != nil {
		return record, "Service type is required."
	}
	if raw := strings.TrimSpace(request.FormValue("odometer")); raw != "" {
		n, err := strconv.ParseInt(strings.ReplaceAll(raw, ",", ""), 10, 64)
		if err != nil || n < 0 {
			return record, "Odometer must be a non-negative whole number."
		}
		record.OdometerMiles = &n
	}
	if raw := strings.TrimSpace(request.FormValue("cost")); raw != "" {
		n, err := parseCents(raw)
		if err != nil {
			return record, "Cost must be a non-negative amount with at most two decimals."
		}
		record.CostCents = &n
	}
	return record, ""
}

func parseCents(raw string) (int64, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ReplaceAll(raw, ",", ""), "$"))
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, errors.New("bad cost")
	}
	if len(parts) == 2 && len(parts[1]) > 2 {
		return 0, errors.New("bad cost")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, errors.New("bad cost")
	}
	frac := int64(0)
	if len(parts) == 2 {
		file := parts[1]
		if len(file) == 1 {
			file += "0"
		}
		if file != "" {
			frac, err = strconv.ParseInt(file, 10, 64)
			if err != nil {
				return 0, err
			}
		}
	}
	if whole > (math.MaxInt64-frac)/100 {
		return 0, errors.New("bad cost")
	}
	return whole*100 + frac, nil
}

func (s *Server) recordFormError(response http.ResponseWriter, request *http.Request, vehicle model.Vehicle, record model.Record, editing bool, msg string) {
	data, err := s.recordFormData(request, vehicle, record, editing)
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Error = msg
	s.render(response, 400, "record-form", data)
}

func (s *Server) record(response http.ResponseWriter, request *http.Request) {
	record, attachments, err := s.getRecord(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	data, err := s.base(request, record.ServiceTypeName)
	if err != nil {
		s.fail(response, err)
		return
	}
	data.Record = record
	data.Attachments = attachments
	s.render(response, 200, "record", data)
}

func (s *Server) deleteRecord(response http.ResponseWriter, request *http.Request) {
	record, _, err := s.getRecord(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	keys, err := s.store.DeleteRecord(request.Context(), record.ID)
	if err != nil {
		s.notFound(response, err)
		return
	}
	s.cleanup(request, keys)
	http.Redirect(response, request, "/vehicles/"+record.VehicleID.String(), 303)
}

func (s *Server) createServiceType(response http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		http.Error(response, "Invalid form", 400)
		return
	}
	name := strings.TrimSpace(request.FormValue("name"))
	if name == "" || len(name) > 100 {
		http.Error(response, "Type name is required and must be under 100 characters", 400)
		return
	}
	_, err := s.store.CreateServiceType(request.Context(), model.ServiceType{ID: uuid.New(), Name: name, CreatedAt: s.now()})
	if errors.Is(err, repository.ErrConflict) {
		http.Error(response, "A service type with that name already exists", 409)
		return
	}
	if err != nil {
		s.fail(response, err)
		return
	}
	dest, _ := safeRedirectTarget(request.FormValue("return_to"))
	http.Redirect(response, request, dest, 303)
}

func (s *Server) getRecord(request *http.Request) (model.Record, []model.Attachment, error) {
	id, err := uuid.Parse(chi.URLParam(request, "recordID"))
	if err != nil {
		return model.Record{}, nil, repository.ErrNotFound
	}
	return s.store.GetRecord(request.Context(), id)
}
