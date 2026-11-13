package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/nhtsa"
)

func TestVehiclePageAlwaysShowsRecallStatus(t *testing.T) {
	f := setup(t)
	vehicle := createVehicle(t, f)
	path := "/vehicles/" + vehicle.ID.String()
	if page := f.do(t, "GET", path, nil, "").Body.String(); !strings.Contains(page, "Safety recalls") || !strings.Contains(page, "Not checked yet") || strings.Contains(page, "No open recalls") {
		t.Fatalf("before a check: %s", page)
	}
	if _, err := f.store.SaveRecallCheck(t.Context(), vehicle, nil, f.s.now()); err != nil {
		t.Fatal(err)
	}
	if page := f.do(t, "GET", path, nil, "").Body.String(); !strings.Contains(page, "No open recalls") || !strings.Contains(page, "Last checked Jul 31, 2026.") || strings.Contains(page, "Not checked yet") {
		t.Fatalf("after a check with none found: %s", page)
	}
	// Editing the model invalidates the check, as it does the recall list.
	body, contentType := formBody(t, map[string]string{"nickname": "Dan's Outback", "year": "2019", "make": "Subaru", "model": "Legacy"}, "", nil)
	if w := f.do(t, "POST", path, body, contentType); w.Code != http.StatusSeeOther {
		t.Fatalf("update: %d", w.Code)
	}
	if page := f.do(t, "GET", path, nil, "").Body.String(); !strings.Contains(page, "Not checked yet") {
		t.Fatalf("after the model changed: %s", page)
	}
}

type fakeRecallChecker struct {
	mu       sync.Mutex
	vehicles []model.Vehicle
	err      error
}

func (f *fakeRecallChecker) CheckVehicle(_ context.Context, vehicle model.Vehicle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vehicles = append(f.vehicles, vehicle)
	return f.err
}

func TestSavingAVehiclePersistsTrimAndChecksRecallsInTheBackground(t *testing.T) {
	f := setup(t)
	checker := &fakeRecallChecker{}
	f.s.EnableRecallCheck(checker)
	fields := map[string]string{"nickname": "Benz", "year": "2024", "make": "Mercedes-Benz", "model": "E-Class", "trim": " E450 4MATIC "}
	body, contentType := formBody(t, fields, "", nil)
	if w := f.do(t, "POST", "/vehicles", body, contentType); w.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	f.s.recallChecks.Wait()
	vehicles, err := f.store.ListVehicles(t.Context(), false)
	if err != nil || len(vehicles) != 1 || vehicles[0].Trim != "E450 4MATIC" {
		t.Fatalf("stored vehicles=%+v err=%v", vehicles, err)
	}
	if len(checker.vehicles) != 1 || checker.vehicles[0].ID != vehicles[0].ID || checker.vehicles[0].Trim != "E450 4MATIC" || checker.vehicles[0].Model != "E-Class" {
		t.Fatalf("check after create = %+v", checker.vehicles)
	}
	if page := f.do(t, "GET", "/vehicles/"+vehicles[0].ID.String()+"/edit", nil, "").Body.String(); !strings.Contains(page, `name="trim" value="E450 4MATIC"`) {
		t.Fatalf("edit form missing trim: %s", page)
	}

	// An edit checks again, with the edited fields, and a failed check does not
	// fail the save.
	checker.err = errors.New("HTTP 503")
	fields["trim"] = "E 450"
	body, contentType = formBody(t, fields, "", nil)
	if w := f.do(t, "POST", "/vehicles/"+vehicles[0].ID.String(), body, contentType); w.Code != http.StatusSeeOther {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	f.s.recallChecks.Wait()
	if len(checker.vehicles) != 2 || checker.vehicles[1].Trim != "E 450" {
		t.Fatalf("check after update = %+v", checker.vehicles)
	}

	// Archived vehicles are not checked.
	if w := f.do(t, "POST", "/vehicles/"+vehicles[0].ID.String()+"/archive", nil, ""); w.Code != http.StatusSeeOther {
		t.Fatalf("archive: %d", w.Code)
	}
	f.s.recallChecks.Wait()
	if len(checker.vehicles) != 2 {
		t.Fatalf("archiving checked recalls: %+v", checker.vehicles)
	}
}

type blockingRecallChecker struct {
	started chan struct{}
	err     error
}

func (b *blockingRecallChecker) CheckVehicle(ctx context.Context, _ model.Vehicle) error {
	close(b.started)
	<-ctx.Done()
	b.err = ctx.Err()
	return b.err
}

func TestDrainRecallChecksWaitsThenCancels(t *testing.T) {
	f := setup(t)
	f.s.EnableRecallCheck(&fakeRecallChecker{})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	body, contentType := formBody(t, map[string]string{"nickname": "Benz", "year": "2024", "make": "Mercedes-Benz", "model": "E-Class"}, "", nil)
	if w := f.do(t, "POST", "/vehicles", body, contentType); w.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", w.Code)
	}
	f.s.DrainRecallChecks(ctx) // a quick check is simply waited for

	blocking := &blockingRecallChecker{started: make(chan struct{})}
	f.s.EnableRecallCheck(blocking)
	body, contentType = formBody(t, map[string]string{"nickname": "Other", "year": "2024", "make": "Mercedes-Benz", "model": "E-Class"}, "", nil)
	if w := f.do(t, "POST", "/vehicles", body, contentType); w.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", w.Code)
	}
	<-blocking.started
	expired, expire := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer expire()
	f.s.DrainRecallChecks(expired) // a stuck check is canceled once the drain deadline passes
	if !errors.Is(blocking.err, context.Canceled) {
		t.Fatalf("blocked check ended with %v, want it canceled", blocking.err)
	}
}

func TestVINDecodeFillsTrim(t *testing.T) {
	f := setup(t)
	year := 2024
	f.s.EnableVINDecode(&fakeVINDecoder{vehicle: nhtsa.Vehicle{Make: "Mercedes-Benz", Model: "E-Class", Trim: "E450 4MATIC", Year: &year}})
	_, body := postVINDecode(t, f, url.Values{"vin": {"W1KLF6BB7VA343747"}})
	if !strings.Contains(body, `name="trim" value="E450 4MATIC"`) {
		t.Fatalf("decoded trim missing: %s", body)
	}
}
