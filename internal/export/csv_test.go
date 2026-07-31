package export

import (
	"bytes"
	"encoding/csv"
	"github.com/bitofbytes-io/carma/internal/model"
	"strings"
	"testing"
	"time"
)

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
	for _, prefix := range []string{"=", "+", "-", "@"} {
		value := prefix + "danger"
		records = append(records, model.Record{VehicleName: value, OccurredOn: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: value, Vendor: value, Notes: value, CreatedByName: value})
	}
	records = append(records, model.Record{VehicleName: "Outback", OccurredOn: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Oil change", Vendor: "DIY", Notes: "ordinary text", CreatedByName: "Daniel"})
	var b bytes.Buffer
	if err := CSV(&b, records); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(b.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i, prefix := range []string{"=", "+", "-", "@"} {
		for _, column := range []int{0, 2, 5, 6, 8} {
			want := "'" + prefix + "danger"
			if rows[i+1][column] != want {
				t.Fatalf("row %d column %d got %q want %q", i+1, column, rows[i+1][column], want)
			}
		}
	}
	normal := rows[len(rows)-1]
	want := []string{"Outback", "Oil change", "DIY", "ordinary text", "Daniel"}
	for i, column := range []int{0, 2, 5, 6, 8} {
		if normal[column] != want[i] {
			t.Fatalf("normal column %d changed to %q", column, normal[column])
		}
	}
}
