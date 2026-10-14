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
	"github.com/google/uuid"
)

type fakeStore struct {
	due           []model.Vehicle
	staleBefore   time.Time
	saved         map[uuid.UUID][]model.Recall
	lockAvailable bool
	unlockCalls   int
}

func (f *fakeStore) ListRecallCheckDue(_ context.Context, staleBefore time.Time) ([]model.Vehicle, error) {
	f.staleBefore = staleBefore
	return f.due, nil
}

func (f *fakeStore) SaveRecallCheck(_ context.Context, vehicle model.Vehicle, recalls []model.Recall, _ time.Time) (int, error) {
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

func (f *fakeSource) Recalls(_ context.Context, vehicleMake, model string, year int) ([]nhtsa.Recall, error) {
	key := vehicleMake + "/" + model
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

func TestRunSkipsWhenAnotherReplicaHoldsTheLock(t *testing.T) {
	store := &fakeStore{due: []model.Vehicle{vehicle("Subaru", "Outback", 2019)}}
	source := &fakeSource{}
	report, err := newTestRunner(store, source, time.Now()).Run(context.Background())
	if err != nil || !report.LockContended || len(source.calls) != 0 {
		t.Fatalf("report=%+v err=%v calls=%v", report, err, source.calls)
	}
}
