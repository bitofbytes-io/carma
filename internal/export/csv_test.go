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
