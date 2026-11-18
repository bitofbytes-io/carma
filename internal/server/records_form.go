package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

func (s *Server) newRecord(response http.ResponseWriter, request *http.Request) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	record, logAgainFrom, err := s.draftRecord(request, vehicle)
	if err != nil {
		s.notFound(response, err)
		return
	}
	data, err := s.recordFormData(request, vehicle, record, false)
	if err != nil {
		s.fail(response, err)
		return
	}
	data.LogAgainFrom = logAgainFrom
	s.render(response, http.StatusOK, "record-form", data)
}

// draftRecord is the unsaved new-record form. Without from, only the date is
// filled. With from, service type, vendor, cost, and notes are copied from
// that vehicle's record; the date stays today and the odometer is the latest
// mileage the vehicle page shows. Receipts are not copied, and the source row
// is only read.
func (s *Server) draftRecord(request *http.Request, vehicle model.Vehicle) (model.Record, string, error) {
	draft := model.Record{OccurredOn: s.today()}
	if strings.TrimSpace(request.URL.Query().Get("from")) == "" {
		return draft, "", nil
	}
	source, err := s.repeatSource(request, vehicle.ID)
	if err != nil {
		return model.Record{}, "", err
	}
	draft.ServiceTypeID = source.ServiceTypeID
	draft.Vendor = source.Vendor
	draft.Notes = source.Notes
	draft.CostCents = copyInt64(source.CostCents)
	draft.OdometerMiles = copyInt64(vehicle.LatestOdometer)
	return draft, source.ID.String(), nil
}

// repeatSource loads the record named by from when it belongs to vehicleID.
func (s *Server) repeatSource(request *http.Request, vehicleID uuid.UUID) (model.Record, error) {
	id, err := uuid.Parse(strings.TrimSpace(request.URL.Query().Get("from")))
	if err != nil {
		return model.Record{}, repository.ErrNotFound
	}
	source, _, err := s.store.GetRecord(request.Context(), id)
	if err != nil {
		return model.Record{}, err
	}
	if source.VehicleID != vehicleID {
		return model.Record{}, repository.ErrNotFound
	}
	return source, nil
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
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
	if err != nil {
		return data, err
	}
	data.Vendors, err = s.store.ListVendors(request.Context())
	return data, err
}

func recordFromForm(request *http.Request) (model.Record, string) {
	record := model.Record{Vendor: strings.TrimSpace(request.FormValue("vendor")), Notes: strings.TrimSpace(request.FormValue("notes"))}
	msg := ""
	var err error
	if record.OccurredOn, err = time.Parse("2006-01-02", request.FormValue("occurred_on")); err != nil {
		msg = "Date is required."
	}
	if id, err := uuid.Parse(request.FormValue("service_type_id")); err != nil {
		if msg == "" {
			msg = "Service type is required."
		}
	} else {
		record.ServiceTypeID = id
	}
	if raw := strings.TrimSpace(request.FormValue("odometer")); raw != "" {
		n, err := model.ParseOdometerMiles(strings.ReplaceAll(raw, ",", ""))
		if err != nil {
			if msg == "" {
				msg = "Odometer must be a non-negative whole number."
			}
		} else {
			record.OdometerMiles = &n
		}
	}
	if raw := strings.TrimSpace(request.FormValue("cost")); raw != "" {
		n, err := model.ParseCents(raw)
		if err != nil {
			if msg == "" {
				msg = "Cost must be a non-negative amount with at most two decimals."
			}
		} else {
			record.CostCents = &n
		}
	}
	if raw := strings.TrimSpace(request.FormValue("warranty_until")); raw != "" {
		date, err := time.Parse("2006-01-02", raw)
		if err != nil {
			if msg == "" {
				msg = "Warranty end date must be a valid date."
			}
		} else {
			record.WarrantyUntilOn = &date
			// A missing or invalid service date already set msg.
			if !record.OccurredOn.IsZero() && msg == "" && model.ValidateWarranty(record.OccurredOn, &date) != nil {
				msg = "Warranty end date cannot be before the service date."
			}
		}
	}
	if raw := strings.TrimSpace(request.FormValue("warranty_miles")); raw != "" {
		n, err := model.ParseOdometerMiles(strings.ReplaceAll(raw, ",", ""))
		if err != nil {
			if msg == "" {
				msg = "Warranty mileage must be a non-negative whole number."
			}
		} else {
			record.WarrantyUntilMiles = &n
		}
	}
	return record, msg
}

func (s *Server) recordFormError(response http.ResponseWriter, request *http.Request, vehicle model.Vehicle, record model.Record, editing bool, msg string) {
	data, err := s.recordFormData(request, vehicle, record, editing)
	if err != nil {
		s.fail(response, err)
		return
	}
	if !editing && strings.TrimSpace(request.URL.Query().Get("from")) != "" {
		if source, err := s.repeatSource(request, vehicle.ID); err == nil {
			data.LogAgainFrom = source.ID.String()
		}
	}
	data.Error = msg
	s.render(response, 400, "record-form", data)
}
