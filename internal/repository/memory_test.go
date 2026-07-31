package repository

import (
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestRecordBaselineOrderAndFiltering(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	now := time.Now()
	u, _ := m.UpsertUser(ctx, model.User{ID: uuid.New(), Email: "a@x", OAuthProvider: "google", OAuthSubject: "1", CreatedAt: now})
	v, _ := m.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Shared", CreatedAt: now})
	types, _ := m.ListServiceTypes(ctx)
	typ := types[0]
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r1 := model.Record{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), VehicleID: v.ID, ServiceTypeID: typ.ID, CreatedBy: u.ID, OccurredOn: date, Vendor: "Alpha", CreatedAt: now}
	r2 := r1
	r2.ID = uuid.MustParse("00000000-0000-0000-0000-000000000002")
	r2.CreatedAt = now.Add(time.Second)
	r2.Vendor = "Beta"
	_, _ = m.CreateRecord(ctx, r1, nil)
	_, _ = m.CreateRecord(ctx, r2, nil)
	rows, e := m.ListRecords(ctx, model.RecordQuery{VehicleID: &v.ID, Search: "beta"})
	if e != nil || len(rows) != 1 || rows[0].ID != r2.ID {
		t.Fatalf("rows=%v err=%v", rows, e)
	}
	rm, _ := m.UpsertReminder(ctx, model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: typ.ID, Enabled: true, IntervalMonths: func() *int { x := 1; return &x }(), CreatedAt: now})
	got, _ := m.ListReminders(ctx, &v.ID, false)
	if len(got) != 1 || got[0].ID != rm.ID || got[0].Baseline.ID != r2.ID {
		t.Fatalf("bad baseline: %+v", got)
	}
}

func TestRecordOrderingNullsLastAndDeterministicTies(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	now := time.Now()
	u, _ := m.UpsertUser(ctx, model.User{ID: uuid.New(), Email: "order@x", OAuthProvider: "google", OAuthSubject: "order", CreatedAt: now})
	v, _ := m.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Order", CreatedAt: now})
	types, _ := m.ListServiceTypes(ctx)
	typ := types[0]
	low, high := int64(10), int64(20)
	ids := []uuid.UUID{uuid.MustParse("00000000-0000-0000-0000-000000000001"), uuid.MustParse("00000000-0000-0000-0000-000000000002"), uuid.MustParse("00000000-0000-0000-0000-000000000003")}
	values := []*int64{nil, &low, &high}
	for i, id := range ids {
		_, _ = m.CreateRecord(ctx, model.Record{ID: id, VehicleID: v.ID, ServiceTypeID: typ.ID, CreatedBy: u.ID, OccurredOn: time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC), OdometerMiles: values[i], CostCents: values[i], CreatedAt: now}, nil)
	}
	for _, field := range []string{"mileage", "cost"} {
		for _, desc := range []bool{false, true} {
			rows, _ := m.ListRecords(ctx, model.RecordQuery{VehicleID: &v.ID, Sort: field, Desc: desc})
			if rows[len(rows)-1].ID != ids[0] {
				t.Fatalf("%s desc=%v did not put NULL last: %v", field, desc, rows)
			}
		}
	}
	// Equal values fall back to date DESC, created_at DESC, id DESC.
	_, _ = m.UpdateRecord(ctx, model.Record{ID: ids[1], VehicleID: v.ID, ServiceTypeID: typ.ID, OccurredOn: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), OdometerMiles: &high, CostCents: &high, UpdatedAt: now})
	rows, _ := m.ListRecords(ctx, model.RecordQuery{VehicleID: &v.ID, Sort: "mileage", Desc: true})
	if rows[0].ID != ids[2] {
		t.Fatalf("tie ordering not deterministic: %v", rows)
	}
}

func TestPostgresOrderClauseMatchesMemoryContract(t *testing.T) {
	for _, tc := range []struct {
		q    model.RecordQuery
		want string
	}{
		{model.RecordQuery{Sort: "mileage"}, "r.odometer_miles ASC NULLS LAST,r.occurred_on DESC,r.created_at DESC,r.id DESC"},
		{model.RecordQuery{Sort: "cost", Desc: true}, "r.cost_cents DESC NULLS LAST,r.occurred_on DESC,r.created_at DESC,r.id DESC"},
		{model.RecordQuery{}, "r.occurred_on DESC,r.created_at DESC,r.id DESC"},
	} {
		got := recordOrder(tc.q)
		if got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
		if strings.Contains(got, "NULLS FIRST") {
			t.Fatal(got)
		}
	}
}
func TestCustomTypeUniqueCaseInsensitive(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	_, e := m.CreateServiceType(ctx, model.ServiceType{ID: uuid.New(), Name: "My Service"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = m.CreateServiceType(ctx, model.ServiceType{ID: uuid.New(), Name: "my service"})
	if e != ErrConflict {
		t.Fatalf("got %v", e)
	}
}

func TestGetAttachmentReturnsRecordContextAndNotFound(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	now := time.Now()
	u, _ := m.UpsertUser(ctx, model.User{ID: uuid.New(), Email: "attachment@x", OAuthProvider: "google", OAuthSubject: "attachment", CreatedAt: now})
	v, _ := m.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Attachment Car", CreatedAt: now})
	types, _ := m.ListServiceTypes(ctx)
	r := model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, CreatedBy: u.ID, OccurredOn: now, CreatedAt: now}
	a := model.Attachment{ID: uuid.New(), RecordID: r.ID, OriginalFilename: "receipt.pdf", StorageKey: "ab/key.pdf"}
	_, _ = m.CreateRecord(ctx, r, []model.Attachment{a})
	gotRecord, gotAttachment, err := m.GetAttachment(ctx, a.ID)
	if err != nil || gotRecord.ID != r.ID || gotRecord.VehicleName != v.Nickname || gotAttachment.ID != a.ID {
		t.Fatalf("record=%+v attachment=%+v err=%v", gotRecord, gotAttachment, err)
	}
	if _, _, err = m.GetAttachment(ctx, uuid.New()); err != ErrNotFound {
		t.Fatalf("not found err=%v", err)
	}
}
