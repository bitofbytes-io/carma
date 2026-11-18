package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/bitofbytes-io/carma/internal/metrics"
	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

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
		s.countUploads(uploadReceipt, metrics.ResultFailure, keys)
		deferUploadCleanup(record.ID, keys, err)
		s.fail(response, err)
		return
	}
	s.countUploads(uploadReceipt, metrics.ResultSuccess, keys)
	http.Redirect(response, request, "/records/"+record.ID.String(), http.StatusSeeOther)
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
	http.Redirect(response, request, "/records/"+record.ID.String(), http.StatusSeeOther)
}

func (s *Server) record(response http.ResponseWriter, request *http.Request) {
	record, attachments, err := s.getRecord(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	vehicle, err := s.store.GetVehicle(request.Context(), record.VehicleID)
	if err != nil {
		s.notFound(response, err)
		return
	}
	data, err := s.base(request, record.ServiceTypeName)
	if err != nil {
		s.fail(response, err)
		return
	}
	data.WarrantyActive = s.warrantyActive([]model.Record{record}, vehicle)
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
	redirectAfterPost(response, request, "/vehicles/"+record.VehicleID.String())
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
		http.Error(response, "A service type with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		s.fail(response, err)
		return
	}
	dest, _ := safeRedirectTarget(request.FormValue("return_to"))
	http.Redirect(response, request, dest, http.StatusSeeOther)
}

func (s *Server) getRecord(request *http.Request) (model.Record, []model.Attachment, error) {
	id, err := uuid.Parse(chi.URLParam(request, "recordID"))
	if err != nil {
		return model.Record{}, nil, repository.ErrNotFound
	}
	return s.store.GetRecord(request.Context(), id)
}
