package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/bitofbytes-io/carma/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestRecallNotificationsPostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store := testdb.Store(t)
	var _ repository.ReminderDigestStore = store

	year := 2019
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	newVehicle := func(nickname, vin string) model.Vehicle {
		t.Helper()
		vehicle, err := store.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: nickname, Year: &year, Make: "Subaru", Model: "Outback", VIN: vin, CreatedAt: now, UpdatedAt: now}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return vehicle
	}
	outback := newVehicle("Outback", "4S4BSANC5K3000000")
	archived := newVehicle("Archived", "")
	unnotified := func() []string {
		t.Helper()
		recalls, err := store.ListUnnotifiedRecalls(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var campaigns []string
		for _, recall := range recalls {
			campaigns = append(campaigns, recall.CampaignNumber)
		}
		return campaigns
	}

	// The first check is the baseline: its campaigns count as notified.
	baseline := []model.Recall{{CampaignNumber: "19V493000"}, {CampaignNumber: "21V587000"}}
	for _, vehicle := range []model.Vehicle{outback, archived} {
		if _, err := store.SaveRecallCheck(ctx, vehicle, baseline, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := unnotified(); len(got) != 0 {
		t.Fatalf("baseline campaigns listed as new: %v", got)
	}

	// Campaigns a later check adds are new, unless dismissed or the vehicle is
	// archived.
	if err := store.ArchiveVehicle(ctx, archived.ID); err != nil {
		t.Fatal(err)
	}
	later := append(baseline, model.Recall{CampaignNumber: "26V100000", Component: "BRAKES"}, model.Recall{CampaignNumber: "26V200000"})
	for _, vehicle := range []model.Vehicle{outback, archived} {
		if _, err := store.SaveRecallCheck(ctx, vehicle, later, now.Add(7*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetRecallDismissed(ctx, outback.ID, "26V200000", &now); err != nil {
		t.Fatal(err)
	}
	recalls, err := store.ListUnnotifiedRecalls(ctx)
	if err != nil || len(recalls) != 1 || recalls[0].CampaignNumber != "26V100000" || recalls[0].VehicleID != outback.ID ||
		recalls[0].VehicleName != "Outback" || recalls[0].VehicleVIN != "4S4BSANC5K3000000" || recalls[0].Component != "BRAKES" {
		t.Fatalf("unnotified = %+v err=%v", recalls, err)
	}

	// Reserving a digest marks its recalls; releasing the unsent claim
	// unmarks them, and a completed digest keeps them marked.
	key := model.RecallKey{VehicleID: outback.ID, CampaignNumber: "26V100000"}
	monday := time.Date(2026, time.October, 19, 0, 0, 0, 0, time.UTC)
	digest := model.ReminderDigest{ID: uuid.New(), WeekStart: monday, ReservedAt: now, Recipients: []string{"user@example.com"}, Recalls: []model.RecallKey{key}}
	if reserved, err := store.ReserveReminderDigest(ctx, digest); err != nil || !reserved {
		t.Fatalf("reserved=%v err=%v", reserved, err)
	}
	if got := unnotified(); len(got) != 0 {
		t.Fatalf("reserved recall still new: %v", got)
	}
	// A restored recall that was never announced is new again, but a losing
	// reservation for the same week marks nothing.
	if err := store.SetRecallDismissed(ctx, outback.ID, "26V200000", nil); err != nil {
		t.Fatal(err)
	}
	loser := model.ReminderDigest{ID: uuid.New(), WeekStart: monday, ReservedAt: now, Recipients: digest.Recipients, Recalls: []model.RecallKey{{VehicleID: outback.ID, CampaignNumber: "26V200000"}}}
	if reserved, err := store.ReserveReminderDigest(ctx, loser); err != nil || reserved {
		t.Fatalf("duplicate week reserved=%v err=%v", reserved, err)
	}
	if got := unnotified(); len(got) != 1 || got[0] != "26V200000" {
		t.Fatalf("after losing reservation = %v", got)
	}
	if err := store.ReleaseReminderDigest(ctx, digest.ID); err != nil {
		t.Fatal(err)
	}
	if got := unnotified(); len(got) != 2 {
		t.Fatalf("after release = %v", got)
	}
	digest.ID = uuid.New()
	if reserved, err := store.ReserveReminderDigest(ctx, digest); err != nil || !reserved {
		t.Fatalf("re-reserved=%v err=%v", reserved, err)
	}
	if err := store.CompleteReminderDigest(ctx, digest.ID, "<digest@example.com>", now); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseReminderDigest(ctx, digest.ID); err != nil {
		t.Fatal(err)
	}
	if got := unnotified(); len(got) != 1 || got[0] != "26V200000" {
		t.Fatalf("after completed digest = %v", got)
	}
	// Later checks keep the notification, even when NHTSA drops an announced
	// campaign and lists it again.
	if _, err := store.SaveRecallCheck(ctx, outback, later, now.Add(14*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := unnotified(); len(got) != 1 || got[0] != "26V200000" {
		t.Fatalf("after another check = %v", got)
	}
	if _, err := store.SaveRecallCheck(ctx, outback, []model.Recall{{CampaignNumber: "19V493000"}}, now.Add(21*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if added, err := store.SaveRecallCheck(ctx, outback, later, now.Add(28*24*time.Hour)); err != nil || added != 3 {
		t.Fatalf("relisted added=%d err=%v", added, err)
	}
	if got := unnotified(); len(got) != 1 || got[0] != "26V200000" {
		t.Fatalf("relisted campaigns announced again: %v", got)
	}

	// A model change starts a new baseline.
	outback.Model = "Forester"
	if _, _, err := store.UpdateVehicle(ctx, outback); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveRecallCheck(ctx, outback, []model.Recall{{CampaignNumber: "18V001000"}}, now.Add(15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := unnotified(); len(got) != 0 {
		t.Fatalf("new model's baseline listed as new: %v", got)
	}
}

func TestRecallNotificationMigrationBackfill(t *testing.T) {
	ctx := context.Background()
	connection, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()
	down, err := migrations.FS.ReadFile("008_recall_notifications.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	var vehicleID string
	if err = connection.QueryRow(ctx, `INSERT INTO vehicles(nickname) VALUES('Outback') RETURNING id::text`).Scan(&vehicleID); err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, `INSERT INTO vehicle_recalls(vehicle_id,campaign_number,first_seen_at) VALUES($1,'19V493000','2026-10-01T00:00:00Z')`, vehicleID); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, connection, migrations.FS, time.UTC); err != nil {
		t.Fatalf("reapply 008: %v", err)
	}
	var notifiedAt time.Time
	var digestID *string
	if err = connection.QueryRow(ctx, `SELECT notified_at,digest_id::text FROM recall_notifications WHERE vehicle_id=$1 AND campaign_number='19V493000'`, vehicleID).Scan(&notifiedAt, &digestID); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC); !notifiedAt.Equal(want) || digestID != nil {
		t.Fatalf("notified_at=%v digest=%v, want %v and no digest", notifiedAt, digestID, want)
	}
}

func TestRecallDismissalSurvivesRelistingPostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store := testdb.Store(t)
	year := 2019
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	vehicle, err := store.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Outback", Year: &year, Make: "Subaru", Model: "Outback", CreatedAt: now, UpdatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(at time.Time, campaigns ...string) {
		t.Helper()
		var recalls []model.Recall
		for _, campaign := range campaigns {
			recalls = append(recalls, model.Recall{CampaignNumber: campaign})
		}
		if _, err := store.SaveRecallCheck(ctx, vehicle, recalls, at); err != nil {
			t.Fatal(err)
		}
	}
	unnotified := func() []string {
		t.Helper()
		recalls, err := store.ListUnnotifiedRecalls(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var campaigns []string
		for _, recall := range recalls {
			campaigns = append(campaigns, recall.CampaignNumber)
		}
		return campaigns
	}
	check(now, "19V493000")
	check(now.Add(7*24*time.Hour), "19V493000", "26V100000")
	dismissedAt := now.Add(8 * 24 * time.Hour)
	if err = store.SetRecallDismissed(ctx, vehicle.ID, "26V100000", &dismissedAt); err != nil {
		t.Fatal(err)
	}

	// NHTSA drops the dismissed campaign and lists it again: it stays
	// dismissed and is not emailed.
	check(now.Add(14*24*time.Hour), "19V493000")
	check(now.Add(21*24*time.Hour), "19V493000", "26V100000")
	if got := unnotified(); len(got) != 0 {
		t.Fatalf("relisted dismissed campaign is new: %v", got)
	}
	all, err := store.ListRecalls(ctx, &vehicle.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var relisted *model.Recall
	for i := range all {
		if all[i].CampaignNumber == "26V100000" {
			relisted = &all[i]
		}
	}
	if relisted == nil || relisted.DismissedAt == nil || !relisted.DismissedAt.Equal(dismissedAt) {
		t.Fatalf("relisted campaign = %+v", relisted)
	}

	// Restoring it makes it eligible again, also after another relisting.
	if err = store.SetRecallDismissed(ctx, vehicle.ID, "26V100000", nil); err != nil {
		t.Fatal(err)
	}
	check(now.Add(28*24*time.Hour), "19V493000")
	check(now.Add(35*24*time.Hour), "19V493000", "26V100000")
	if got := unnotified(); len(got) != 1 || got[0] != "26V100000" {
		t.Fatalf("restored campaign after relisting = %v", got)
	}
}

func TestRecallDismissalMigrationBackfill(t *testing.T) {
	ctx := context.Background()
	connection, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()
	down, err := migrations.FS.ReadFile("009_recall_dismissals.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	var vehicleID string
	if err = connection.QueryRow(ctx, `INSERT INTO vehicles(nickname) VALUES('Outback') RETURNING id::text`).Scan(&vehicleID); err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, `INSERT INTO vehicle_recalls(vehicle_id,campaign_number,dismissed_at) VALUES
		($1,'19V493000','2026-10-01T00:00:00Z'),($1,'21V587000',NULL)`, vehicleID); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, connection, migrations.FS, time.UTC); err != nil {
		t.Fatalf("reapply 009: %v", err)
	}
	var campaign string
	var dismissedAt time.Time
	if err = connection.QueryRow(ctx, `SELECT campaign_number,dismissed_at FROM recall_dismissals WHERE vehicle_id=$1`, vehicleID).Scan(&campaign, &dismissedAt); err != nil {
		t.Fatalf("expected exactly the dismissed campaign: %v", err)
	}
	if want := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC); campaign != "19V493000" || !dismissedAt.Equal(want) {
		t.Fatalf("backfilled %s at %v", campaign, dismissedAt)
	}
}
