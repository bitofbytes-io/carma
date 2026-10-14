package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestVehicleSpendPostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	scopedURL := testdb.URL(t)
	connection, err := pgx.Connect(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()
	store, err := repository.NewPostgres(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	userID, vehicleID, otherVehicleID, emptyVehicleID, tiresID, oilID, washID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,oauth_provider,oauth_subject,email) VALUES($1,'test','spend','driver@example.com')`, []any{userID}},
		{`INSERT INTO vehicles(id,nickname) VALUES($1,'Wagon'),($2,'Other'),($3,'Empty')`, []any{vehicleID, otherVehicleID, emptyVehicleID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Spend tires'),($2,'Spend oil'),($3,'Spend wash')`, []any{tiresID, oilID, washID}},
		{`INSERT INTO records(vehicle_id,service_type_id,occurred_on,created_by,cost_cents) VALUES
			($1,$2,'2025-12-31',$5,60000),
			($1,$3,'2025-06-01',$5,7500),
			($1,$3,'2026-01-01',$5,8000),
			($1,$3,'2026-07-30',$5,8250),
			($1,$4,'2026-03-01',$5,NULL),
			($1,$2,'2027-01-01',$5,100),
			($6,$2,'2026-02-02',$5,99999)`, []any{vehicleID, tiresID, oilID, washID, userID, otherVehicleID}},
	} {
		if _, err := connection.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	summary, err := store.VehicleSpend(ctx, vehicleID, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if summary.YearToDateCents != 16250 || summary.LifetimeCents != 83850 || summary.PricedRecords != 5 {
		t.Fatalf("totals=%+v", summary)
	}
	if len(summary.Types) != 2 {
		t.Fatalf("types=%+v (unpriced service types must be left out)", summary.Types)
	}
	tires, oil := summary.Types[0], summary.Types[1]
	if tires.Name != "Spend tires" || tires.Records != 2 || tires.YearToDateCents != 0 || tires.LifetimeCents != 60100 {
		t.Fatalf("tires=%+v", tires)
	}
	if oil.Name != "Spend oil" || oil.Records != 3 || oil.YearToDateCents != 16250 || oil.LifetimeCents != 23750 {
		t.Fatalf("oil=%+v", oil)
	}

	empty, err := store.VehicleSpend(ctx, emptyVehicleID, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || empty.PricedRecords != 0 || empty.LifetimeCents != 0 || empty.Types != nil {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}
