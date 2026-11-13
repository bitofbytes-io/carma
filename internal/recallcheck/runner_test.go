package recallcheck

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/nhtsa"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

type fakeStore struct {
	due           []model.Vehicle
	staleBefore   time.Time
	saved         map[uuid.UUID][]model.Recall
	lockAvailable bool
	saveErr       error
	unlockCalls   int
}

func (f *fakeStore) ListRecallCheckDue(_ context.Context, staleBefore time.Time) ([]model.Vehicle, error) {
	f.staleBefore = staleBefore
	return f.due, nil
}

func (f *fakeStore) SaveRecallCheck(_ context.Context, vehicle model.Vehicle, recalls []model.Recall, _ time.Time) (int, error) {
	if f.saveErr != nil {
		return 0, f.saveErr
	}
	if f.saved == nil {
		f.saved = map[uuid.UUID][]model.Recall{}
	}
	added := 0
	for _, recall := range recalls {
		known := false
		for _, previous := range f.saved[vehicle.ID] {
			known = known || previous.CampaignNumber == recall.CampaignNumber
		}
		if !known {
			added++
		}
	}
	f.saved[vehicle.ID] = recalls
	return added, nil
}

func (f *fakeStore) TryRecallCheckLock(context.Context) (func(context.Context) error, bool, error) {
	if !f.lockAvailable {
		return nil, false, nil
	}
	return func(context.Context) error { f.unlockCalls++; return nil }, true, nil
}

type fakeSource struct {
	recalls map[string][]nhtsa.Recall
	err     map[string]error
	calls   []string
}

func (f *fakeSource) RecallsForVehicle(_ context.Context, vehicleMake, model, trim string, year int) ([]nhtsa.Recall, error) {
	key := vehicleMake + "/" + model
	if trim != "" {
		key += "/" + trim
	}
	f.calls = append(f.calls, key)
	return f.recalls[key], f.err[key]
}

func vehicle(vehicleMake, vehicleModel string, year int) model.Vehicle {
	return model.Vehicle{ID: uuid.New(), Make: vehicleMake, Model: vehicleModel, Year: &year}
}

func newTestRunner(store *fakeStore, source *fakeSource, now time.Time) *Runner {
	runner := NewRunner(store, source, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.now = func() time.Time { return now }
	return runner
}

func TestRunSavesRecallsAndContinuesPastFailures(t *testing.T) {
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	outback, civic := vehicle("Subaru", "Outback", 2019), vehicle("Honda", "Civic", 2005)
	received := time.Date(2019, time.June, 26, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{due: []model.Vehicle{civic, outback}, lockAvailable: true}
	source := &fakeSource{
		recalls: map[string][]nhtsa.Recall{"Subaru/Outback": {{CampaignNumber: "19V493000", Component: "STRUCTURE", ReportReceived: &received, ParkIt: true}}},
		err:     map[string]error{"Honda/Civic": errors.New("HTTP 503")},
	}
	report, err := newTestRunner(store, source, now).Run(context.Background())
	if err == nil || report.Checked != 1 || report.Failed != 1 || report.New != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if !store.staleBefore.Equal(now.Add(-CheckAge)) || store.unlockCalls != 1 {
		t.Fatalf("staleBefore=%v unlockCalls=%d", store.staleBefore, store.unlockCalls)
	}
	saved := store.saved[outback.ID]
	if len(saved) != 1 || saved[0].VehicleID != outback.ID || saved[0].CampaignNumber != "19V493000" || !saved[0].ParkIt || !saved[0].ReportReceivedOn.Equal(received) {
		t.Fatalf("saved=%+v", saved)
	}
	if _, ok := store.saved[civic.ID]; ok {
		t.Fatal("a failed fetch must not record a check")
	}

	// A second run finds nothing new.
	store.due = []model.Vehicle{outback}
	if report, err = newTestRunner(store, source, now).Run(context.Background()); err != nil || report.Checked != 1 || report.New != 0 {
		t.Fatalf("second run report=%+v err=%v", report, err)
	}
}

func TestCheckVehicleChecksOneVehicleWithItsTrim(t *testing.T) {
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	benz := vehicle("Mercedes-Benz", "E-Class", 2024)
	benz.Trim = "E450 4MATIC"
	store := &fakeStore{}
	source := &fakeSource{recalls: map[string][]nhtsa.Recall{"Mercedes-Benz/E-Class/E450 4MATIC": {{CampaignNumber: "24V224000"}, {CampaignNumber: "26V281000"}}}}
	if err := newTestRunner(store, source, now).CheckVehicle(context.Background(), benz); err != nil {
		t.Fatal(err)
	}
	if len(store.saved[benz.ID]) != 2 || store.unlockCalls != 0 {
		t.Fatalf("saved=%+v unlockCalls=%d", store.saved, store.unlockCalls)
	}

	incomplete := vehicle("Subaru", "", 2019)
	noYear := model.Vehicle{ID: uuid.New(), Make: "Subaru", Model: "Outback"}
	for _, v := range []model.Vehicle{incomplete, noYear} {
		if err := newTestRunner(store, source, now).CheckVehicle(context.Background(), v); err != nil {
			t.Fatal(err)
		}
	}
	if len(source.calls) != 1 {
		t.Fatalf("incomplete vehicles were looked up: %v", source.calls)
	}

	source.err = map[string]error{"Mercedes-Benz/E-Class/E450 4MATIC": errors.New("HTTP 503")}
	store.saved = nil
	if err := newTestRunner(store, source, now).CheckVehicle(context.Background(), benz); err == nil || len(store.saved) != 0 {
		t.Fatalf("failed lookup: err=%v saved=%v", err, store.saved)
	}
}

func TestCheckVehicleDropsAResultForAVehicleEditedMeanwhile(t *testing.T) {
	store := &fakeStore{saveErr: repository.ErrStaleVehicle}
	source := &fakeSource{recalls: map[string][]nhtsa.Recall{"Subaru/Outback": {{CampaignNumber: "19V493000"}}}}
	if err := newTestRunner(store, source, time.Now()).CheckVehicle(context.Background(), vehicle("Subaru", "Outback", 2019)); err != nil {
		t.Fatalf("a stale result is dropped, not an error: %v", err)
	}
	store.saveErr = errors.New("connection lost")
	if err := newTestRunner(store, source, time.Now()).CheckVehicle(context.Background(), vehicle("Subaru", "Outback", 2019)); err == nil {
		t.Fatal("a failed save should fail the check")
	}
}

type decodingSource struct {
	*fakeSource
	decoded nhtsa.Vehicle
	err     error
	vins    []string
}

func (d *decodingSource) DecodeVIN(_ context.Context, vin string) (nhtsa.Vehicle, error) {
	d.vins = append(d.vins, vin)
	return d.decoded, d.err
}

func TestRunDecodesTheTrimOfVehiclesSavedWithoutOne(t *testing.T) {
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	benz := vehicle("Mercedes-Benz", "E-Class", 2024)
	benz.VIN = "w1klf6bb7va343747"
	source := &decodingSource{
		fakeSource: &fakeSource{recalls: map[string][]nhtsa.Recall{"Mercedes-Benz/E-Class/E450 4MATIC": {{CampaignNumber: "26V281000"}}}},
		decoded:    nhtsa.Vehicle{Trim: "E450 4MATIC"},
	}
	store := &fakeStore{due: []model.Vehicle{benz}, lockAvailable: true}
	runner := NewRunner(store, source, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.now = func() time.Time { return now }
	if report, err := runner.Run(context.Background()); err != nil || report.Checked != 1 || report.New != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if len(source.vins) != 1 || source.vins[0] != "W1KLF6BB7VA343747" {
		t.Fatalf("decoded VINs = %v", source.vins)
	}

	// Neither a trim already saved nor a missing or undecodable VIN is decoded
	// again; a lookup failure fails the check so it is retried.
	withTrim, noVIN, undecodable := benz, benz, benz
	withTrim.Trim = "E450 4MATIC"
	noVIN.VIN = ""
	source.err = nhtsa.ErrNotDecoded
	for _, v := range []model.Vehicle{withTrim, noVIN, undecodable} {
		if err := runner.CheckVehicle(context.Background(), v); err != nil {
			t.Fatal(err)
		}
	}
	if len(source.vins) != 2 {
		t.Fatalf("decoded VINs = %v", source.vins)
	}
	source.err = errors.New("HTTP 503")
	if err := runner.CheckVehicle(context.Background(), undecodable); err == nil {
		t.Fatal("a failed VIN lookup should fail the check")
	}
}

func TestRunSkipsWhenAnotherReplicaHoldsTheLock(t *testing.T) {
	store := &fakeStore{due: []model.Vehicle{vehicle("Subaru", "Outback", 2019)}}
	source := &fakeSource{}
	report, err := newTestRunner(store, source, time.Now()).Run(context.Background())
	if err != nil || !report.LockContended || len(source.calls) != 0 {
		t.Fatalf("report=%+v err=%v calls=%v", report, err, source.calls)
	}
}
