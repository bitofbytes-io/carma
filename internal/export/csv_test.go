package export

import (
	"bytes"
	"encoding/csv"
	"github.com/bitofbytes-io/carma/internal/model"
	"math"
	"strings"
	"testing"
	"time"
)

func TestCSVFormatsCentsExactly(t *testing.T) {
	for _, tc := range []struct {
		cents int64
		want  string
	}{
		{cents: 0, want: "0.00"},
		{cents: 5, want: "0.05"},
		{cents: 1234, want: "12.34"},
		{cents: math.MaxInt64, want: "92233720368547758.07"},
	} {
		if got := formatCents(tc.cents); got != tc.want {
			t.Fatalf("formatCents(%d)=%q want=%q", tc.cents, got, tc.want)
		}
		var output bytes.Buffer
		if err := CSV(&output, []model.Record{{OccurredOn: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), CostCents: &tc.cents}}); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(strings.NewReader(output.String())).ReadAll()
		if err != nil || len(rows) != 2 || rows[1][4] != tc.want {
			t.Fatalf("CSV cents=%d rows=%v err=%v", tc.cents, rows, err)
		}
	}
}

func TestCSVParsesAndMatchesRows(t *testing.T) {
	v := int64(42)
	rows := []model.Record{{VehicleName: "Outback", OccurredOn: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Oil change", OdometerMiles: &v, Notes: "comma, quote \" ok", AttachmentCount: 1, CreatedByName: "A"}}
	var b bytes.Buffer
	if e := CSV(&b, rows); e != nil {
		t.Fatal(e)
	}
	got, e := csv.NewReader(strings.NewReader(b.String())).ReadAll()
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 || got[1][0] != "Outback" || got[1][6] != rows[0].Notes {
		t.Fatalf("%v", got)
	}
}

func TestCSVNeutralizesSpreadsheetFormulaText(t *testing.T) {
	var records []model.Record
	var hazardous []string
	for _, whitespace := range []string{"", " ", "\t", "\r", "\n", " \t\r\n", "\u00a0"} {
		for _, prefix := range []string{"=", "+", "-", "@"} {
			value := whitespace + prefix + "danger"
			hazardous = append(hazardous, value)
			records = append(records, model.Record{VehicleName: value, OccurredOn: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: value, Vendor: value, Notes: value, CreatedByName: value})
		}
	}
	records = append(records, model.Record{VehicleName: " Outback", OccurredOn: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: "\tOil change", Vendor: "\rDIY", Notes: "\nordinary text", CreatedByName: "\u00a0Daniel"})
	var b bytes.Buffer
	if err := CSV(&b, records); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(b.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i, value := range hazardous {
		for _, column := range []int{0, 2, 5, 6, 8} {
			// encoding/csv intentionally removes CR immediately before LF while reading.
			want := "'" + strings.ReplaceAll(value, "\r\n", "\n")
			if rows[i+1][column] != want {
				t.Fatalf("row %d column %d got %q want %q", i+1, column, rows[i+1][column], want)
			}
		}
	}
	normal := rows[len(rows)-1]
	want := []string{" Outback", "\tOil change", "\rDIY", "\nordinary text", "\u00a0Daniel"}
	for i, column := range []int{0, 2, 5, 6, 8} {
		if normal[column] != want[i] {
			t.Fatalf("normal column %d changed to %q", column, normal[column])
		}
	}
}
