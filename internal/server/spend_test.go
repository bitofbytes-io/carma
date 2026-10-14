package server

import (
	"strings"
	"testing"
)

func TestVehiclePageShowsSpendSummary(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	page := f.do(t, "GET", "/vehicles/"+v.ID.String(), nil, "")
	if strings.Contains(page.Body.String(), `id="spending"`) {
		t.Fatal("spend card shown for a vehicle without costs")
	}
	types, _ := f.store.ListServiceTypes(t.Context())
	for _, fields := range []map[string]string{
		{"occurred_on": "2026-03-02", "cost": "450.50"},
		{"occurred_on": "2025-11-14", "cost": "89.00"},
		{"occurred_on": "2026-01-20"},
	} {
		fields["service_type_id"] = types[0].ID.String()
		b, ct := formBody(t, fields, "", nil)
		if w := f.do(t, "POST", "/vehicles/"+v.ID.String()+"/records", b, ct); w.Code != 303 {
			t.Fatalf("record: %d %s", w.Code, w.Body.String())
		}
	}
	// The fixture's today is Jul 31, 2026, so the 2025 record only counts toward all time.
	page = f.do(t, "GET", "/vehicles/"+v.ID.String()+"?q=nomatch", nil, "")
	body := page.Body.String()
	for _, want := range []string{`id="spending"`, "2026 so far</dt><dd>$450.50", "All time</dt><dd>$539.50", "Records with a cost</dt><dd>2", "<td>" + types[0].Name + "</td><td class=\"number\">2</td><td class=\"number\">$450.50</td><td class=\"number\">$539.50</td>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("vehicle page missing %q: %s", want, body)
		}
	}
}
