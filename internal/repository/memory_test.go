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

func TestMemoryVehicleOdometerAndReminderSnapshotSemantics(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	current := int64(1000)
	v, err := m.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Snapshot", CurrentOdometer: &current, CreatedAt: now, UpdatedAt: now})
	if err != nil || v.LatestOdometer == nil || *v.LatestOdometer != current {
		t.Fatalf("created vehicle=%+v err=%v", v, err)
	}
	types, _ := m.ListServiceTypes(ctx)
	months := 6
	created := now.Add(-time.Hour)
	rm, err := m.UpsertReminder(ctx, model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMonths: &months, StartingOdometer: v.LatestOdometer, Enabled: true, CreatedAt: created, UpdatedAt: created})
	if err != nil || rm.StartingOdometer == nil || *rm.StartingOdometer != current {
		t.Fatalf("created reminder=%+v err=%v", rm, err)
	}

	changedStart := int64(9000)
	later := now.Add(time.Hour)
	updated, err := m.UpsertReminder(ctx, model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMonths: &months, StartingOdometer: &changedStart, Enabled: false, CreatedAt: later, UpdatedAt: later})
	if err != nil || !updated.CreatedAt.Equal(created) || updated.StartingOdometer == nil || *updated.StartingOdometer != current || updated.Enabled {
		t.Fatalf("updated reminder did not preserve baselines: %+v err=%v", updated, err)
	}

	recordMileage := int64(2500)
	_, _ = m.CreateRecord(ctx, model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[1].ID, OccurredOn: now, OdometerMiles: &recordMileage, CreatedAt: now}, nil)
	gotVehicle, err := m.GetVehicle(ctx, v.ID)
	if err != nil || gotVehicle.CurrentOdometer == nil || *gotVehicle.CurrentOdometer != current || gotVehicle.LatestOdometer == nil || *gotVehicle.LatestOdometer != recordMileage {
		t.Fatalf("effective vehicle mileage=%+v err=%v", gotVehicle, err)
	}
}

func TestMemoryInitializesLegacyReminderOdometerOnce(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	v, _ := m.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Legacy", CreatedAt: now, UpdatedAt: now})
	types, _ := m.ListServiceTypes(ctx)
	miles := int64(5000)
	_, _ = m.UpsertReminder(ctx, model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[0].ID, IntervalMiles: &miles, Enabled: true, CreatedAt: now, UpdatedAt: now})
	recordMileage := int64(50000)
	_, _ = m.CreateRecord(ctx, model.Record{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: types[1].ID, OccurredOn: now, OdometerMiles: &recordMileage, CreatedAt: now}, nil)

	first := int64(42000)
	v.CurrentOdometer = &first
	v.UpdatedAt = now.Add(time.Hour)
	if _, err := m.UpdateVehicle(ctx, v); err != nil {
		t.Fatal(err)
	}
	second := int64(43000)
	v.CurrentOdometer = &second
	v.UpdatedAt = now.Add(2 * time.Hour)
	if _, err := m.UpdateVehicle(ctx, v); err != nil {
		t.Fatal(err)
	}
	rows, err := m.ListReminders(ctx, &v.ID, true)
	if err != nil || len(rows) != 1 || rows[0].StartingOdometer == nil || *rows[0].StartingOdometer != recordMileage || rows[0].LatestOdometer == nil || *rows[0].LatestOdometer != recordMileage {
		t.Fatalf("legacy reminder=%+v err=%v", rows, err)
	}
}

func TestPostgresLegacySnapshotUsesEffectiveOdometerQuery(t *testing.T) {
	query := strings.ToLower(initializeReminderOdometerSQL)
	for _, want := range []string{
		"starting_odometer_miles=greatest($2::bigint",
		"select max(odometer_miles) from records where vehicle_id=$1",
		"starting_odometer_miles is null",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("legacy snapshot query missing %q: %s", want, query)
		}
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
	for _, tc := range []struct {
		name string
		q    model.RecordQuery
		want []uuid.UUID
	}{
		{name: "date ascending", q: model.RecordQuery{VehicleID: &v.ID, Sort: "date"}, want: []uuid.UUID{ids[0], ids[1], ids[2]}},
		{name: "date descending", q: model.RecordQuery{VehicleID: &v.ID, Sort: "date", Desc: true}, want: []uuid.UUID{ids[2], ids[1], ids[0]}},
		{name: "unknown ascending request", q: model.RecordQuery{VehicleID: &v.ID, Sort: "unknown"}, want: []uuid.UUID{ids[2], ids[1], ids[0]}},
		{name: "unknown descending request", q: model.RecordQuery{VehicleID: &v.ID, Sort: "unknown", Desc: true}, want: []uuid.UUID{ids[2], ids[1], ids[0]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := m.ListRecords(ctx, tc.q)
			if err != nil || len(rows) != len(tc.want) {
				t.Fatalf("rows=%v err=%v", rows, err)
			}
			for i, want := range tc.want {
				if rows[i].ID != want {
					t.Fatalf("row %d = %s want %s", i, rows[i].ID, want)
				}
			}
		})
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
		{model.RecordQuery{Sort: "unrecognized"}, "r.occurred_on DESC,r.created_at DESC,r.id DESC"},
		{model.RecordQuery{Sort: "unrecognized", Desc: true}, "r.occurred_on DESC,r.created_at DESC,r.id DESC"},
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

func TestEscapeLikePatternTreatsSearchMetacharactersLiterally(t *testing.T) {
	for input, want := range map[string]string{
		`ordinary`:  `ordinary`,
		`100%`:      `100\%`,
		`code_name`: `code\_name`,
		`path\file`: `path\\file`,
		`%_\`:       `\%\_\\`,
	} {
		if got := escapeLikePattern(input); got != want {
			t.Fatalf("escapeLikePattern(%q)=%q want=%q", input, got, want)
		}
	}
	if got := strings.Count(recordFilter, `ESCAPE E'\\'`); got != 3 {
		t.Fatalf("record filter has %d explicit LIKE escape clauses", got)
	}
}

func TestMemorySearchTreatsSQLWildcardsLiterally(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	now := time.Now()
	user, _ := m.UpsertUser(ctx, model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "literal-search", Email: "literal-search@example.com", CreatedAt: now})
	vehicle, _ := m.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Search", CreatedAt: now})
	types, _ := m.ListServiceTypes(ctx)
	literal := model.Record{ID: uuid.New(), VehicleID: vehicle.ID, ServiceTypeID: types[0].ID, CreatedBy: user.ID, OccurredOn: now, Notes: `literal % underscore_ slash\ marker`, CreatedAt: now}
	decoy := literal
	decoy.ID = uuid.New()
	decoy.Notes = "literal percent underscoreX slashX marker"
	_, _ = m.CreateRecord(ctx, literal, nil)
	_, _ = m.CreateRecord(ctx, decoy, nil)

	for _, search := range []string{"%", "_", `\`} {
		rows, err := m.ListRecords(ctx, model.RecordQuery{VehicleID: &vehicle.ID, Search: search})
		if err != nil || len(rows) != 1 || rows[0].ID != literal.ID {
			t.Fatalf("literal search %q rows=%+v err=%v", search, rows, err)
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

func TestUpdateRecordPreservesVehicleAndValidatesStoredReference(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	now := time.Now()
	u, _ := m.UpsertUser(ctx, model.User{ID: uuid.New(), Email: "update@x", OAuthProvider: "google", OAuthSubject: "update", CreatedAt: now})
	originalVehicle, _ := m.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Original", CreatedAt: now})
	otherVehicle, _ := m.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Other", CreatedAt: now})
	types, _ := m.ListServiceTypes(ctx)
	record := model.Record{ID: uuid.New(), VehicleID: originalVehicle.ID, ServiceTypeID: types[0].ID, CreatedBy: u.ID, OccurredOn: now, CreatedAt: now}
	_, _ = m.CreateRecord(ctx, record, nil)

	record.VehicleID = otherVehicle.ID
	record.Notes = "updated"
	updated, err := m.UpdateRecord(ctx, record)
	if err != nil || updated.VehicleID != originalVehicle.ID || updated.VehicleName != originalVehicle.Nickname {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}

	m.mu.Lock()
	delete(m.vehicles, originalVehicle.ID)
	m.mu.Unlock()
	if _, err = m.UpdateRecord(ctx, record); err != ErrNotFound {
		t.Fatalf("missing stored vehicle err=%v", err)
	}
}

func TestUpsertUserPreservesIdentityAndLinksCaseInsensitiveEmail(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	created := time.Now().UTC().Add(-time.Hour)
	legacy, err := m.UpsertUser(ctx, model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "legacy-subject", Email: "linked@example.com", DisplayName: "Legacy", CreatedAt: created, UpdatedAt: created, LastLoginAt: created})
	if err != nil {
		t.Fatal(err)
	}

	identityUpdate, err := m.UpsertUser(ctx, model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "legacy-subject", Email: "renamed@example.com", DisplayName: "Identity Update", CreatedAt: time.Now(), UpdatedAt: time.Now(), LastLoginAt: time.Now()})
	if err != nil || identityUpdate.ID != legacy.ID || identityUpdate.CreatedAt != legacy.CreatedAt {
		t.Fatalf("identity update: legacy=%+v updated=%+v err=%v", legacy, identityUpdate, err)
	}

	linked, err := m.UpsertUser(ctx, model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "linked-subject", Email: "RENAMED@example.com", DisplayName: "Email Link", CreatedAt: time.Now(), UpdatedAt: time.Now(), LastLoginAt: time.Now()})
	if err != nil || linked.ID != legacy.ID || linked.CreatedAt != legacy.CreatedAt {
		t.Fatalf("email link: legacy=%+v linked=%+v err=%v", legacy, linked, err)
	}
	oldIdentity, err := m.FindUserByOAuth(ctx, "google", "legacy-subject")
	if err != nil || oldIdentity != nil {
		t.Fatalf("old identity remains: user=%+v err=%v", oldIdentity, err)
	}
	newIdentity, err := m.FindUserByOAuth(ctx, "google", "linked-subject")
	if err != nil || newIdentity == nil || newIdentity.ID != legacy.ID {
		t.Fatalf("new identity missing: user=%+v err=%v", newIdentity, err)
	}
}

func TestUpsertUserRejectsIdentityEmailCollisionWithoutMutation(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	now := time.Now().UTC()
	identityUser, err := m.UpsertUser(ctx, model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "identity-subject", Email: "identity@example.com", DisplayName: "Identity User", CreatedAt: now, UpdatedAt: now, LastLoginAt: now})
	if err != nil {
		t.Fatal(err)
	}
	emailUser, err := m.UpsertUser(ctx, model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "email-subject", Email: "collision@example.com", DisplayName: "Email User", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second), LastLoginAt: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}

	candidate := model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: identityUser.OAuthSubject, Email: "COLLISION@example.com", DisplayName: "Must Not Persist", CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second), LastLoginAt: now.Add(2 * time.Second)}
	if _, err = m.UpsertUser(ctx, candidate); err != ErrConflict {
		t.Fatalf("collision error=%v want=%v", err, ErrConflict)
	}
	if len(m.users) != 2 {
		t.Fatalf("collision changed user count to %d", len(m.users))
	}
	gotIdentity, err := m.FindUserByOAuth(ctx, identityUser.OAuthProvider, identityUser.OAuthSubject)
	if err != nil || gotIdentity == nil || *gotIdentity != identityUser {
		t.Fatalf("identity user mutated: got=%+v want=%+v err=%v", gotIdentity, identityUser, err)
	}
	gotEmail, err := m.FindUserByEmail(ctx, emailUser.Email)
	if err != nil || gotEmail == nil || *gotEmail != emailUser {
		t.Fatalf("email user mutated: got=%+v want=%+v err=%v", gotEmail, emailUser, err)
	}
	if got, err := m.FindUserByEmail(ctx, candidate.Email); err != nil || got == nil || got.ID != emailUser.ID {
		t.Fatalf("case-insensitive email lookup changed: user=%+v err=%v", got, err)
	}
}
