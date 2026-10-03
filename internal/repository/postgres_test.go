package repository

import (
	"strings"
	"testing"

	"github.com/bitofbytes-io/carma/internal/model"
)

func TestPostgresLegacySnapshotUsesEffectiveOdometerQuery(t *testing.T) {
	query := strings.ToLower(initializeReminderOdometerSQL)
	for _, want := range []string{
		"starting_odometer_miles=greatest($2::bigint",
		"select max(odometer_miles) from records where vehicle_id=$1",
		"starting_odometer_pending=false",
		"and starting_odometer_pending",
		"starting_odometer_miles is null",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("legacy snapshot query missing %q: %s", want, query)
		}
	}
}

func TestPostgresReminderUpsertPreservesBaselineStateQuery(t *testing.T) {
	query := strings.ToLower(upsertReminderSQL)
	parts := strings.Split(query, "on conflict")
	if len(parts) != 2 {
		t.Fatalf("missing conflict clause: %s", query)
	}
	for _, forbidden := range []string{"starting_odometer_miles=excluded", "starting_odometer_pending=excluded", "created_at=excluded"} {
		if strings.Contains(parts[1], forbidden) {
			t.Fatalf("upsert conflict overwrites baseline state with %q: %s", forbidden, query)
		}
	}
	if !strings.Contains(query, "returning id,created_at,starting_odometer_miles,starting_odometer_pending") {
		t.Fatalf("upsert does not return preserved state: %s", query)
	}
}

func TestPostgresOrderClausePutsNullsLastWithDeterministicTies(t *testing.T) {
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
