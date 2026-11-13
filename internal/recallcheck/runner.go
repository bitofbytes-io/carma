// Package recallcheck refreshes each vehicle's NHTSA safety recall campaigns.
package recallcheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/nhtsa"
	"github.com/bitofbytes-io/carma/internal/repository"
)

const (
	// DefaultInterval is how often the server looks for vehicles to check.
	DefaultInterval = 6 * time.Hour
	// CheckAge is how long a vehicle's recall list is trusted before it is
	// checked again.
	CheckAge = 7 * 24 * time.Hour
)

// Source lists NHTSA recall campaigns for a vehicle and model year.
type Source interface {
	RecallsForVehicle(ctx context.Context, vehicleMake, model, trim string, year int) ([]nhtsa.Recall, error)
}

type Report struct {
	LockContended        bool
	Checked, Failed, New int
}

type Runner struct {
	store  repository.RecallCheckStore
	source Source
	logger *slog.Logger
	now    func() time.Time
}

func NewRunner(store repository.RecallCheckStore, source Source, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{store: store, source: source, logger: logger, now: time.Now}
}

// Run checks every vehicle whose recall list is older than CheckAge or was
// fetched for a different year, make, or model. A failed vehicle is logged and
// retried on the next run; the others are still saved.
func (r *Runner) Run(ctx context.Context) (report Report, runErr error) {
	unlock, acquired, err := r.store.TryRecallCheckLock(ctx)
	if err != nil {
		return report, err
	}
	if !acquired {
		report.LockContended = true
		return report, nil
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, unlock(unlockContext))
	}()
	vehicles, err := r.store.ListRecallCheckDue(ctx, r.now().Add(-CheckAge))
	if err != nil {
		return report, fmt.Errorf("list vehicles due a recall check: %w", err)
	}
	var failures []error
	for _, vehicle := range vehicles {
		if ctx.Err() != nil {
			failures = append(failures, ctx.Err())
			break
		}
		added, err := r.check(ctx, vehicle)
		if err != nil {
			report.Failed++
			failures = append(failures, err)
			r.logger.Error("recall check failed", "vehicle_id", vehicle.ID, "error", err)
			continue
		}
		report.Checked++
		report.New += added
		if added > 0 {
			r.logger.Info("new recalls found", "vehicle_id", vehicle.ID, "count", added)
		}
	}
	if report.Checked > 0 || report.Failed > 0 {
		r.logger.Info("recall check finished", "checked", report.Checked, "failed", report.Failed, "new", report.New)
	}
	return report, errors.Join(failures...)
}

// CheckVehicle checks one vehicle now, such as right after it is added or
// edited, instead of waiting for the next Run. A vehicle without a year, make,
// and model is skipped. It does not take Run's lock: saving a check is one
// transaction, so a concurrent Run at worst repeats the same check.
func (r *Runner) CheckVehicle(ctx context.Context, vehicle model.Vehicle) error {
	if vehicle.Year == nil || strings.TrimSpace(vehicle.Make) == "" || strings.TrimSpace(vehicle.Model) == "" {
		return nil
	}
	added, err := r.check(ctx, vehicle)
	if err != nil {
		return err
	}
	if added > 0 {
		r.logger.Info("new recalls found", "vehicle_id", vehicle.ID, "count", added)
	}
	return nil
}

// VINDecoder looks up what a VIN decodes to. A Source that is also a
// VINDecoder supplies the trim for vehicles saved without one.
type VINDecoder interface {
	DecodeVIN(ctx context.Context, vin string) (nhtsa.Vehicle, error)
}

// trimFor returns the trim to look recalls up with: the vehicle's own, or for a
// vehicle saved before trims were stored, the one its VIN decodes to. A VIN
// that cannot be decoded is not an error; the lookup just has no trim.
func (r *Runner) trimFor(ctx context.Context, vehicle model.Vehicle) (string, error) {
	decoder, ok := r.source.(VINDecoder)
	vin := nhtsa.LookupVIN(vehicle.VIN)
	if vehicle.Trim != "" || !ok || vin == "" {
		return vehicle.Trim, nil
	}
	decoded, err := decoder.DecodeVIN(ctx, vin)
	if errors.Is(err, nhtsa.ErrNotDecoded) || errors.Is(err, nhtsa.ErrInvalidVIN) {
		return "", nil
	}
	return decoded.Trim, err
}

func (r *Runner) check(ctx context.Context, vehicle model.Vehicle) (int, error) {
	trim, err := r.trimFor(ctx, vehicle)
	if err != nil {
		return 0, fmt.Errorf("decode VIN for vehicle %s: %w", vehicle.ID, err)
	}
	found, err := r.source.RecallsForVehicle(ctx, vehicle.Make, vehicle.Model, trim, *vehicle.Year)
	if err != nil {
		return 0, fmt.Errorf("fetch recalls for vehicle %s: %w", vehicle.ID, err)
	}
	recalls := make([]model.Recall, 0, len(found))
	for _, recall := range found {
		recalls = append(recalls, model.Recall{
			VehicleID:        vehicle.ID,
			CampaignNumber:   recall.CampaignNumber,
			Component:        recall.Component,
			Summary:          recall.Summary,
			Consequence:      recall.Consequence,
			Remedy:           recall.Remedy,
			ReportReceivedOn: recall.ReportReceived,
			ParkIt:           recall.ParkIt,
			ParkOutside:      recall.ParkOutside,
		})
	}
	added, err := r.store.SaveRecallCheck(ctx, vehicle, recalls, r.now())
	if errors.Is(err, repository.ErrStaleVehicle) {
		// Edited while NHTSA was queried; the edit's own check covers it.
		r.logger.Info("recall check dropped: vehicle changed", "vehicle_id", vehicle.ID)
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("save recalls for vehicle %s: %w", vehicle.ID, err)
	}
	return added, nil
}
