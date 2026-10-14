package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/nhtsa"
	"github.com/bitofbytes-io/carma/internal/ui"
	"github.com/go-chi/chi/v5"
)

// VINDecoder looks up a vehicle's make, model, and year from its VIN.
type VINDecoder interface {
	DecodeVIN(ctx context.Context, vin string) (nhtsa.Vehicle, error)
}

// EnableVINDecode adds the "Decode VIN" button to the vehicle form.
func (s *Server) EnableVINDecode(decoder VINDecoder) {
	s.vinDecoder = decoder
}

// loadAttention fills the dashboard's "Needs attention" lists: overdue and
// due-soon reminders, and vehicles with open recalls.
func (s *Server) loadAttention(ctx context.Context, data *pageData) error {
	var err error
	if data.Attention, err = s.attention(ctx); err != nil {
		return err
	}
	recalls, err := s.store.ListRecalls(ctx, nil, false)
	if err != nil {
		return err
	}
	data.RecallAlerts = ui.GroupRecalls(recalls)
	return nil
}

// decodeVIN answers the vehicle form's "Decode VIN" button with the identity
// fields filled in from NHTSA, keeping what was typed when decoding fails.
func (s *Server) decodeVIN(response http.ResponseWriter, request *http.Request) {
	if s.vinDecoder == nil {
		http.Error(response, "Not found", http.StatusNotFound)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 64<<10)
	if err := request.ParseForm(); err != nil {
		http.Error(response, "Invalid form", http.StatusBadRequest)
		return
	}
	data := pageData{VINDecode: true}
	data.Vehicle.VIN = strings.TrimSpace(request.PostFormValue("vin"))
	data.Vehicle.Make = strings.TrimSpace(request.PostFormValue("make"))
	data.Vehicle.Model = strings.TrimSpace(request.PostFormValue("model"))
	data.Vehicle.LicensePlate = strings.TrimSpace(request.PostFormValue("license_plate"))
	if year, err := strconv.Atoi(strings.TrimSpace(request.PostFormValue("year"))); err == nil {
		data.Vehicle.Year = &year
	}
	decoded, err := s.vinDecoder.DecodeVIN(request.Context(), data.Vehicle.VIN)
	switch {
	case errors.Is(err, nhtsa.ErrInvalidVIN), errors.Is(err, nhtsa.ErrNotDecoded):
		data.VINError = true
		data.VINMessage = err.Error() + "."
	case err != nil:
		slog.Warn("VIN decode failed", "error", err)
		data.VINError = true
		data.VINMessage = "NHTSA's VIN lookup is not responding. Try again later or enter the details yourself."
	default:
		data.Vehicle.VIN = nhtsa.NormalizeVIN(data.Vehicle.VIN)
		data.Vehicle.Make, data.Vehicle.Model = decoded.Make, decoded.Model
		if decoded.Year != nil {
			data.Vehicle.Year = decoded.Year
		}
		data.VINMessage = strings.TrimSpace(fmt.Sprintf("Filled in from NHTSA: %s %s %s.", yearText(decoded.Year), decoded.Make, decoded.Model))
		if decoded.Warning != "" {
			data.VINMessage += " NHTSA noted: " + decoded.Warning
		}
	}
	s.renderNamed(response, "vehicle-form", "vehicle-identity", data)
}

func yearText(year *int) string {
	if year == nil {
		return ""
	}
	return strconv.Itoa(*year)
}

func (s *Server) dismissRecall(response http.ResponseWriter, request *http.Request) {
	now := s.now().UTC()
	s.setRecallDismissed(response, request, &now)
}

func (s *Server) restoreRecall(response http.ResponseWriter, request *http.Request) {
	s.setRecallDismissed(response, request, nil)
}

func (s *Server) setRecallDismissed(response http.ResponseWriter, request *http.Request, dismissedAt *time.Time) {
	vehicle, err := s.getVehicle(request)
	if err != nil {
		s.notFound(response, err)
		return
	}
	if err = s.store.SetRecallDismissed(request.Context(), vehicle.ID, chi.URLParam(request, "campaign"), dismissedAt); err != nil {
		s.notFound(response, err)
		return
	}
	redirectAfterPost(response, request, "/vehicles/"+vehicle.ID.String()+"#recalls")
}
