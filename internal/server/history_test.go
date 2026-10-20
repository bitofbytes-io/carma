package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestVehicleHistoryListsRecordsOldestFirstWithTotals(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	for _, fields := range []map[string]string{
		{"occurred_on": "2026-05-02", "odometer": "61000", "cost": "450.50", "vendor": "Tire Barn", "notes": "four new tires"},
		{"occurred_on": "2025-11-14", "odometer": "55000", "cost": "89.00", "vendor": "Jiffy Lube", "notes": "<b>synthetic</b>"},
		{"occurred_on": "2026-01-20", "vendor": "Driveway"},
	} {
		fields["service_type_id"] = types[0].ID.String()
		filename, file := "", []byte(nil)
		if fields["vendor"] == "Jiffy Lube" {
			filename, file = "receipt.pdf", []byte("%PDF-1.7\nreceipt")
		}
		b, ct := formBody(t, fields, filename, file)
		if w := f.do(t, "POST", "/vehicles/"+v.ID.String()+"/records", b, ct); w.Code != 303 {
			t.Fatalf("record: %d %s", w.Code, w.Body.String())
		}
	}

	w := f.do(t, "GET", "/vehicles/"+v.ID.String()+"/history", nil, "")
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, body)
	}
	for _, want := range []string{"2019 Subaru Outback", "Prepared Jul 31, 2026", "Total spent", "$539.50", "Receipts on file</dt><dd>1<", "Service records</dt><dd>3 · Nov 14, 2025 – May 2, 2026", "data-print-history", "&lt;b&gt;synthetic&lt;/b&gt;"} {
		if !strings.Contains(body, want) {
			t.Fatalf("history missing %q: %s", want, body)
		}
	}
	first, second, third := strings.Index(body, "Jiffy Lube"), strings.Index(body, "Driveway"), strings.Index(body, "Tire Barn")
	if first < 0 || !(first < second && second < third) {
		t.Fatalf("records not oldest first: %d %d %d", first, second, third)
	}

	vehiclePage := f.do(t, "GET", "/vehicles/"+v.ID.String(), nil, "")
	if !strings.Contains(vehiclePage.Body.String(), `href="/vehicles/`+v.ID.String()+`/history"`) {
		t.Fatal("vehicle page does not link to the printable history")
	}
	if missing := f.do(t, "GET", "/vehicles/"+uuid.NewString()+"/history", nil, ""); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown vehicle status=%d", missing.Code)
	}
}

func TestVehicleHistoryWithoutRecords(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	w := f.do(t, "GET", "/vehicles/"+v.ID.String()+"/history", nil, "")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "No service records yet.") || !strings.Contains(body, "Total spent</dt><dd>—") {
		t.Fatalf("status=%d body=%s", w.Code, body)
	}
}
